# Persist `--cheap-context` selections into named profiles

**Goal:** Let a `--cheap-context` MCP-server/tool selection be saved into
a named profile and replayed on a later launch without re-showing the
interactive picker.

**Architecture:** Two new `profile.Profile` fields (`ContextServers
[]string`, `ContextExcluded map[string][]string`), mirroring the
existing `Env map[string]string` precedent for structured, non-scalar
profile data. Applying a saved selection is NOT gated behind
`--cheap-context` — that flag's only remaining job is presenting the
interactive picker; a resolved `--profile` carrying a saved selection
activates replay on its own.

**Rollback:** Purely additive. A profile with no saved selection
(`ContextServers == nil`) behaves exactly as before this feature
existed; `--cheap-context` with no matching profile still shows the
picker unchanged.

---

## Context

`--cheap-context` (shipped separately, `docs/plans/` has no prior
design doc for it — see `cmd/clown/cheapcontext.go`'s own doc comments
for its history) runs an interactive picker after every discovered MCP
server has started, lets the user deselect/trim servers and tools, and
previously applied the result for that one launch only. Nothing was
remembered — re-running required re-picking from scratch every time.

Investigation before implementing found:

- clown already has a named-profile system (`internal/profile`,
  `cmd/clown/profileform.go`, `cmd/clown/profilecmd.go`; see
  `docs/plans/2026-04-23-profiles-design.md` and
  `docs/plans/2026-07-06-openrouter-profiles-design.md`) storing
  `(provider, backend, model, url, token, env)` combos in
  `~/.config/clown/profiles.toml`, applied via `--profile <name>` / a
  clownfile pin / an interactive picker before any provider dispatch.
- **Timing mismatch**: profile resolution + `applyNamedProfile` happens
  once near the top of `run()`, strictly before provider dispatch. It
  only ever sets env vars and prepends `--model` — pure launch-time
  input, no dependency on plugin discovery.
- A cheap-context selection is the opposite: it's the *output* of
  negotiating with already-running MCP servers, computed inside
  `runManaged` after `host.StartAll()` succeeds, keyed by live
  tool-catalog names that can drift across launches (different
  directory → different plugin set; a plugin update → renamed tools).
- `runClaude` (the path that reaches `runManaged`) did not receive
  `selectedProfile` at all before this change.

## Design

### 1. New `profile.Profile` fields

```go
type Profile struct {
    // ... existing fields ...

    // ContextServers and ContextExcluded are a saved --cheap-context
    // selection: ContextServers is the exact set of MCP server names to
    // keep (everything else discovered at launch is dropped);
    // ContextExcluded, keyed by a kept server's name, lists the tool
    // names to additionally exclude from that server. A saved server or
    // tool name absent from a later launch's live catalog is silently
    // skipped — the catalog is the source of truth.
    ContextServers  []string            `toml:"context_servers,omitempty"`
    ContextExcluded map[string][]string `toml:"context_excluded,omitempty"`
}
```

**Important invariant, discovered during code review**:
`ContextServers` must never be a non-nil, zero-length slice.
BurntSushi/toml's `omitempty` drops *any* zero-length slice on write
(nil or not), so an empty selection would silently vanish on save and
read back as `nil` — indistinguishable from "no saved selection at
all." `profile.Validate` rejects this shape for every caller;
`cmd/clown/cheapcontext.go`'s save prompt also refuses up front (purely
so the user doesn't answer two prompts before hitting the same
rejection). A "deselect every server" choice also has no meaningful
replay semantics regardless — there would be nothing left to apply
exclusions to.

### 2. Load-time application, hooked where cheap-context already runs

`applyNamedProfile`'s timing is unchanged — it stays a pure, early
env/flags step. The resolved profile is instead carried through
`parsedFlags.cheapContextProfile`, threaded through `runClaude` →
`runWithPluginHost` → `runManaged` the same way
`cheapContextActive`/`allDiscoveredDirs` already were.

`selectServers` (`cmd/clown/cheapcontext.go`) takes an optional `saved
*profile.Profile` parameter. `selectionFromSavedProfile` builds a
`selectionResult` directly from `saved.ContextServers`/
`ContextExcluded`, filtered against the current launch's live catalogs
— this is the one path where `selectServers` works without an
interactive TTY, since a replay has nothing left to prompt for.

**Activation, not just application**: `cheapContextShouldActivate`
(`cmd/clown/cheapcontext.go`) decides whether the whole
selection-application path in `runManaged` runs at all —
`--cheap-context` OR a resolved `--profile` with a non-nil
`ContextServers`. This was a deliberate late correction: an earlier
version of this feature required BOTH `--cheap-context` and `--profile
<name>` to replay a saved selection, which defeated the point of
persisting it — a user should be able to run just `clown --profile
<name>` and get the trimmed tool set silently.

### 3. Save-time prompt, hooked at picker confirmation

After a REAL (non-cancelled, non-replayed) picker confirmation in
`selectServers`, `promptSaveSelection` offers to persist the choice —
"Save this selection for reuse?" then "Save as which profile name?"
(any existing name is a valid target, updating that profile's saved
selection; a new name creates a minimal profile defaulting to the
claude/anthropic provider/backend). It reuses `saveProfileForm`'s
Validate → Upsert → Save → print sequence
(`cmd/clown/profileform.go`) rather than re-deriving it, via
`valuesFromProfile`/forcing `Confirm = true`.

### 4. Preserving unedited fields across `clown profile edit`

`profileFormValues` (`cmd/clown/profileform.go`) gained opaque
unexported passthrough fields (`env`, `contextServers`,
`contextExcluded`) so that editing a profile's visible fields
(name/provider/model/etc.) via `clown profile edit` doesn't silently
wipe them — the huh form has no widget for any of the three, but
`toProfile()` previously built a completely fresh struct on every save,
dropping whatever wasn't form-visible. This was already a latent bug
for `Env` before this feature; fixed for all three fields together.

### 5. `clown profile list` awareness

Gained a CONTEXT column showing `N server(s)` for a profile with a
saved selection, or `-` otherwise, so the persisted selection isn't
invisible from the list view.

## Verification

- Unit tests: TOML round-trip for the two new fields (including the
  empty-slice rejection), `selectionFromSavedProfile`'s filtering
  (present vs. stale server/tool names), `cheapContextShouldActivate`'s
  full truth table, the profile-edit field-preservation round-trip, and
  `promptSaveSelection`'s empty-selection guard (must fire without
  requiring a TTY, i.e. before any interactive prompt runs).
- Manual: `just build-juggler`, then interactively run
  `./result/bin/clown --verbose --cheap-context`, deselect some
  servers/tools, confirm the save prompt, and verify
  `./result/bin/clown --verbose --profile <name>` (no `--cheap-context`
  needed) replays the same trimmed tool set — checked via `/mcp` inside
  the launched session.
- `just build` / `just test-go` end to end.

## Explicitly out of scope

- Per-directory/clownfile-scoped cheap-context selections — a
  profile-scoped selection is global across directories, matching how
  profiles already work.
- Auto-detecting "this profile's saved selection looks stale, want to
  re-pick?" UX — missing entries are silently dropped; a warn-on-drift
  or fail-closed mode can be a follow-up if that proves confusing.
- Native `httpServers` plugins' per-tool filtering reaching the
  saved-selection path any differently than it does the live picker —
  clown#175 still governs that gap, unrelated to persistence.
- `promptSaveSelection` re-reading `profiles.toml` that `run()` already
  loaded once earlier in the same launch — real but low-severity
  redundant I/O, tracked as clown#178 rather than fixed inline.
