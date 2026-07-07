# Juggler as a Unified Model Registry — Design

Date: 2026-07-07
Status: approved (brainstorm with Sasha, session live-catalpa)

## Problem

`juggler` (`cmd/juggler`, `internal/juggler`) is today a control plane for
exactly one kind of thing: locally-spawned `llama-server` children, tracked
in-memory and addressed by alias. It has no concept of a remote endpoint.
Meanwhile clown's named-profile registry (`internal/profile`, shipped this
session for OpenRouter — `docs/plans/2026-07-06-openrouter-profiles-design.md`)
stores gateway endpoints (url + token) *inline*, duplicating what should be
a single "where do I find model X" answer.

The motivating want: a unified place to register **any** model — local GGUF
or remote gateway — so that both a clown session's main model and its
Task-tool subagent's model can be assigned "as I see fit," from one registry,
instead of hand-wiring env vars per case.

A hard constraint surfaced during design and worth stating up front: **Claude
Code is a single process with one process-wide `ANTHROPIC_BASE_URL` /
`ANTHROPIC_AUTH_TOKEN`.** `CLAUDE_CODE_SUBAGENT_MODEL` /
`ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL` only ever select a different
*model name* against that one shared endpoint — never a different endpoint
per subagent (confirmed against `man/man7/claude-code-env.7`: these are two
disjoint sections, API/auth vars have no subagent-scoped variant). Full
"main session on the frontier API, subagent on a local juggler model"
routing needs a frontend that does its own per-role dispatch — out of scope
here, tracked as the eventual "trapeze-native" migration (not designed in
this document; ringmaster/troupe/clown-protocol becoming native to a custom
frontend harness is a separate, larger design).

## Decision summary

1. **juggler becomes the model registry.** It already owns "local models"
   (GGUF files, spawn/track/reap via `llama-server`). It gains "remote
   models" — static endpoints with no process — as a second model *kind* in
   the same catalog, exposed over its existing UDS JSON-RPC control socket.
2. **clown keeps owning role mapping.** `internal/profile` (the
   `clown profile` TUI, the clownfile `[profile].profile` pin, `--profile`)
   is not replaced. A profile's `Model` field — which already exists and
   already doubles as "the model name" for the (never-wired-up)
   `backend:"local"` case — becomes the field clown resolves *through*
   juggler when there's no inline `URL`/`Token`.
3. **Subagent-model routing reuses the existing `Env` map**, not a new
   field. `Profile.Env["CLAUDE_CODE_SUBAGENT_MODEL"]` gains juggler-aware
   resolution: if its value names a registered juggler model, clown checks
   that model's endpoint against the main model's endpoint and either wires
   it (same endpoint) or fails loudly at launch (different endpoint — the
   Claude Code constraint above). A value that isn't a known juggler model
   name is treated as a literal model slug, exactly as today.
4. **Dual-architecture / rollback.** Inline `URL`/`Token` on a profile stay
   as a fully-supported path, checked first. The OpenRouter profile shipped
   this session keeps working completely untouched. Juggler resolution is
   additive — only profiles with no inline URL/Token, or subagent-model
   values that name a juggler model, take the new path.

## Section 1 — juggler: model registry, RPC surface

**New model catalog**, the union of two kinds:

- **Local** — unchanged: auto-discovered by scanning
  `~/.local/share/juggler/models/*.gguf` (today's `ListAvailableModels`
  logic). No registration step; the filename stem is the name.
- **Remote** — new: explicit entries in a new file,
  `~/.local/share/juggler/models.toml`:

  ```toml
  [[model]]
  name  = "claude-openrouter"
  kind  = "remote"
  style = "anthropic"              # which env-var shape a resolver should emit
  url   = "https://openrouter.ai/api"
  token = "${OPENROUTER_API_KEY}"  # resolved the same way clownfile.ResolveEnv works
  ```

New RPC methods on the existing control socket (`internal/juggler/rpc.go`,
alongside `StartInstance`/`StopInstance`/`ListInstances`/`GetInstance`/
`ListAvailableModels`):

| Method | Behavior |
|---|---|
| `ListModels` | Union of scanned-local + registered-remote entries. Backs both `juggler model list` and clown's picker. |
| `AddRemoteModel` / `RemoveRemoteModel` | Atomic read-modify-write of `models.toml` (mirrors `internal/profile/store.go`'s Save/Upsert/Remove pattern already shipped this session). |
| `ResolveModel(name)` | Remote: returns `{kind:"remote", url, token(resolved), style}`. Local: starts (or reuses) the instance via the existing `StartInstance` path and returns `{kind:"local", url}`. This is also the first real implementation of the `backend:"local"` profile case, which exists in the schema today but was never wired up (blocked on the still-stubbed `--provider juggler`, FDR-0011 phase 2) — `ResolveModel` is a much smaller vehicle for that one piece than FDR-0011's full backend-lifecycle plan. |

**CLI surface** (`cmd/juggler`): extend the existing subcommand family —
`juggler model list` (union view), `juggler model add <name> --url --token
--style`, `juggler model remove <name>`. Each is one RPC call + pretty-print,
matching every other juggler subcommand's shape.

## Section 2 — clown: resolving profiles through juggler

**`applyNamedProfile` gains one new branch.** Today, for a `gateway`
profile, it reads `profile.URL`/`profile.Token` directly and sets env
authoritatively. The new logic:

```
if profile.URL != "" && profile.Token != "" {
    // unchanged — inline path, today's behavior
} else if profile.Model != "" {
    resolved, err := jugglerClient.ResolveModel(profile.Model)
    // err: no daemon running / unknown model name — clear, actionable errors
    // (mirrors "unknown profile" listing available names)
    if resolved.Kind == "remote" {
        // same authoritative env-setting as the inline path, sourced from `resolved`
    } else { // "local"
        os.Setenv("ANTHROPIC_BASE_URL", resolved.URL)
        // no auth needed for a local llama-server
    }
}
```

**Subagent-model routing** (in the `Env` map application loop, unchanged
call site):

```
for k, v := range profile.Env {
    if os.Getenv(k) != "" { continue } // ambient wins, as today
    if isSubagentModelKey(k) {
        if m, ok := jugglerClient.TryResolveModel(v); ok {
            if m.URL != mainEndpoint.URL {
                return fmt.Errorf("subagent model %q resolves to a different endpoint (%s) than the main model (%s) — Claude Code shares one ANTHROPIC_BASE_URL per process, so this pairing isn't launchable yet (see docs/plans/2026-07-07-juggler-model-registry-design.md)", v, m.URL, mainEndpoint.URL)
            }
            v = m.Slug // the model string juggler resolved to, if it differs from the registered name
        }
        // else: v is a literal model slug, exactly as today
    }
    os.Setenv(k, clownfile.ResolveEnv(v))
}
```

`isSubagentModelKey` matches `CLAUDE_CODE_SUBAGENT_MODEL` and the
`ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL` family.

**Error handling summary:**
- juggler daemon unreachable → error pointing at `juggler daemon` / the
  home-manager module, not a raw dial failure.
- Unknown model name → error listing available models (`ListModels`).
- Local model fails to start → surface juggler's own `StartInstance` error
  verbatim.
- Subagent/main endpoint mismatch → hard error at launch (see above) rather
  than a silent misroute or a downstream auth failure.

## Testing

- **juggler**: unit tests for the remote-model registry (add/remove/list/
  resolve), following `rpc_test.go`'s existing pattern; the bats lane's
  fake-llama-server fixture covers local resolution end-to-end.
- **clown**: unit tests for the new `applyNamedProfile` branch against a
  fake juggler-client interface — inline URL/Token stays regression-tested
  and untouched; juggler-resolved gateway/local cases; the endpoint-mismatch
  hard-error case for subagent routing.
- **bats**: extend `juggler.bats` (or a new file) with an end-to-end
  resolve-a-registered-remote-model case.

## Rollback

Additive throughout. Don't reference a juggler model (keep using inline
`URL`/`Token`, or don't set a subagent-model env key) and nothing changes.
No existing profile — including the OpenRouter one shipped this session —
needs to change.

**Promotion criterion** for eventually deprecating inline `URL`/`Token` in
favor of juggler-only: one release of the juggler-resolution path in use
with no reported gaps. Not committed to in this design.

## Tuning levers

- **Remote-model registry file location**
  (`~/.local/share/juggler/models.toml`, current proposal, colocated with
  the GGUF `models/` dir) vs. `~/.config/juggler/`. Revisit if it causes
  user confusion about where to find/edit it.
- **Subagent endpoint-mismatch strictness** — hard error (current
  proposal) vs. warn-and-proceed. Revisit if users report it blocking
  setups they understand fine.
- **`ResolveModel` auto-start behavior for local models** — auto-start on
  demand (friendliest default, current proposal) vs. requiring an explicit
  `juggler start` first. Revisit if unexpected slow warm-ups on launch
  become annoying.

## Explicitly out of scope

- **Merging `internal/profile` and juggler's registry into one system.**
  Considered and rejected — clown keeps owning role mapping; juggler only
  owns "what models exist and how to reach them."
- **Making ringmaster/troupe/clown-protocol native to a non-Claude-Code
  frontend** (the "trapeze" vision — a personal crush fork, possibly
  exploring opencode too). This is the natural next step once this design
  lands, since juggler's registry is frontend-agnostic by construction, but
  it's a separate, much larger design (replaces clown's current
  env-var/`--plugin-dir`-injection model with native integration) and needs
  its own brainstorm.
- **FDR-0011's full `--backend` transport flag and auto-lifecycle
  (start/stop-on-exit prompts).** This design implements the one piece of
  FDR-0011 that was actually blocking (local-model resolution via
  `ResolveModel`), not the full flag/prompt UX FDR-0011 describes.

## References

- `docs/plans/2026-07-06-openrouter-profiles-design.md` (the profile
  registry and OpenRouter gateway work this design builds on)
- `docs/features/0011-clown-backend-circus-lifecycle.md` (FDR-0011 — related
  but broader scope; terminology there is stale post-rename, see the
  eng:doc-drift note from the prior session)
- `man/man7/claude-code-env.7`, `man/man7/juggler.7`, `man/man1/juggler.1`
