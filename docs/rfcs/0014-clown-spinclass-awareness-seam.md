---
status: proposed
date: 2026-06-15
---

# clown ↔ spinclass awareness seam (`group-id` + presence)

## Abstract

This document specifies the lightly-coupled contract by which clown and an
external session orchestrator (spinclass) become aware of each other, with
neither binary hard-depending on the other. clown gains a native clownfile
`group-id` field whose value is supplied by environment interpolation
(`group-id = "${SPINCLASS_SESSION_ID}"`); clown itself contains no hardcoded
knowledge of any orchestrator. `group-id` keys clown's group chat channel,
its presence grouping, and a `{group}` resume-title surface. The seam is
bidirectional: the orchestrator exports a documented decoration environment
contract that sources `group-id` (orchestrator → clown), and clown publishes a
presence index that the orchestrator consumes for session liveness and listing
(clown → orchestrator). The only binding is one line of configuration; both a
bare clown (no orchestrator) and a bare orchestrator (non-clown harness) remain
fully functional.

## Introduction

Several clown capabilities are keyed off the identity of the *session* a clown
instance runs inside: the group chat channel that fans a message out to every
clown under one worktree (RFC-0013 §3.2), the presence listing that shows which
clowns are reachable (RFC-0013 §3.3), and the OSC-2 terminal title clown emits
on reattach (RFC-0013 §1.3). Today clown reads the orchestrator's session key
directly by name — `jobwake.GroupKey()` returns `os.Getenv("SPINCLASS_SESSION_ID")`
— which hardwires clown to spinclass and inverts the intended ownership: clown,
the consuming runtime, should not encode the producer's variable name.

A parallel decision (Sasha, 2026-06-15) settles the remaining ambiguity in the
clown/spinclass split: **spinclass exits the multiplexing business entirely.** A
spinclass session becomes just a worktree, an identity environment, and a place
to run shells; clown owns *all* attach/multiplexing/grouping, **including the
detached-spawn executor** — which resolves the open question left in RFC-0013
§1.3. The orchestrator's `sc start`/`sc resume` exec a plain `$SHELL`; its
`sc spawn` execs clown in spawn mode and relies on clown's `[attach].spawn` to
self-detach. This in turn makes the orchestrator's session-liveness probe a
*consumer* of clown's presence index rather than an inspector of an
orchestrator-owned multiplexer.

This RFC specifies clown's half of that seam normatively: the `group-id`
configuration field and its interpolation/config-only semantics; the surfaces
`group-id` keys; the presence index schema and freshness contract the
orchestrator queries; the detached-spawn executor wire; the flag-day cutover
ordering; and the bare-clown guarantee. The orchestrator's half — the
`SPINCLASS_*` decoration export contract and the consumer (liveness / list)
mechanism — is drafted by the spinclass peer and cross-referenced here and from
spinclass FDR-0017. The variable *names* that constitute the wire are stated
normatively in this document (§6); their producer-side set/strip semantics live
in the spinclass export contract.

**Scope.** This document covers *local* awareness: configuration-sourced
grouping, presence, and the local detached-spawn wire. Remote (ssh) attach
remains spinclass's `internal/remote` path (FDR-0011) and is out of scope.

## Requirements Language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in RFC 2119.

## Specification

### 1. Ownership and the lightly-coupled principle

clown owns: multiplexer attach/wrap (interactive **and** detached spawn,
RFC-0013 §1.3 as amended by this document), grouping (`group-id`), per-instance
identity (RFC-0013 §2), chat (RFC-0013 §3), and the presence index.

The orchestrator owns: worktree create/teardown, session-state tracking,
tombstone retention, the `SPINCLASS_*` decoration env export (§6), session
liveness and listing (which consume clown's presence index, §4), and remote
attach.

clown MUST NOT read any orchestrator-specific environment variable by name **in
code**. The sole coupling is a single configuration line binding `group-id` to an
interpolated environment reference (§2). That line is shipped in clown's
burned-in default clownfile (clown#146); a configuration default MAY name an
orchestrator variable because the reference degrades gracefully — it interpolates
to the empty string (ungrouped) when the variable is absent — so naming it in a
config default does not hard-couple clown to the orchestrator. Consequently:

- A clown launched with no `SPINCLASS_*` environment MUST run fully functional
  and ungrouped: the burned-in `group-id` interpolates empty (§8).
- An orchestrator launching a non-clown harness MUST remain functional; the
  presence-consuming liveness path (§4) is the clown-aware path only, with a
  generic fallback owned by the orchestrator.

### 2. The `group-id` configuration field

A new field `group-id` is added to the clownfile (RFC-0013 §1), a string,
discovered and cascaded by the same `$PWD → $HOME` ascent and per-key merge as
the rest of the clownfile, beneath the burned-in default base (RFC-0013 §1.1,
clown#146).

```toml
[attach]
group-id = "${SPINCLASS_SESSION_ID}"
```

> The field MAY be located in the `[attach]` table (its primary consumers are
> attach/group surfaces) or at top level; this document specifies `[attach]` as
> the location. Implementations MUST accept exactly one canonical location.

clown MUST ship this binding (`group-id = "${SPINCLASS_SESSION_ID}"`) in its
burned-in default clownfile (RFC-0013 §1.1, clown#146) — the lowest-precedence
cascade layer. Shipping it in clown's own default rather than requiring an
operator-supplied file means the release that removes the hardcoded read (§2.2)
also supplies the replacement binding atomically, eliminating the cutover gap
(§7). An operator clownfile MAY override `group-id` per key (e.g. a coarser
`"${SPINCLASS_REPO}"`, a prefix, or `""` to force ungrouped).

#### 2.1 Environment interpolation

The `group-id` value is subject to environment interpolation at clownfile load:

- clown MUST expand environment references of the form `${NAME}` in the
  `group-id` string, substituting the value of environment variable `NAME`.
- A reference to an unset or empty variable MUST expand to the empty string.
- Interpolation MUST compose within the surrounding string, so
  `group-id = "team-${SPINCLASS_SESSION_ID}"` and
  `group-id = "${SPINCLASS_REPO}"` are both valid and yield, respectively, a
  prefixed and a coarser group.
- clown MUST NOT interpret the *meaning* of any interpolated variable; it
  performs purely textual substitution. (This is what keeps clown
  orchestrator-agnostic: `SPINCLASS_SESSION_ID` appears only in a config string —
  clown's burned-in default and/or operator clownfiles — never in clown's code.)

clown MUST apply interpolation only to the `group-id` field. It MUST NOT
interpolate other clownfile fields under this document (the existing `[profile]`
and `[attach]` argv/template fields retain their current semantics).

#### 2.2 Config-only grouping (removal of the hardcoded read)

After interpolation, the resolved `group-id` is the session's **group key**.

- clown MUST derive the group key solely from the resolved `group-id`. The
  prior behavior of reading `SPINCLASS_SESSION_ID` directly
  (`jobwake.GroupKey()`) MUST be removed.
- When the resolved `group-id` is empty (unset, or empty after interpolation),
  the session is **ungrouped**: it has no group channel, an empty presence
  decoration, and `{group}` resolves empty (§3).
- The group key MUST NOT participate in per-instance routing-key resolution
  (RFC-0013 §2.3 is unchanged): it names a *group*, not the instance.

### 3. Surfaces keyed by `group-id`

The resolved group key replaces every current use of the directly-read
orchestrator session key:

1. **Group chat channel.** The group channel is `ChannelID(group-id)` (RFC-0009
   §2 channel derivation; RFC-0013 §3.2 group fan-out). When `group-id` is
   empty there is no group channel. (Replaces `ChannelID(GroupKey())` where
   `GroupKey()` read `SPINCLASS_SESSION_ID`.)
2. **Presence decoration.** The presence record's `decoration` field (§4) MUST
   be the resolved `group-id` (was `SPINCLASS_SESSION_ID`).
3. **`{group}` title placeholder.** A new placeholder `{group}` is added to the
   `[attach].resume-title` surface (RFC-0013 §1.3), resolving to the group key.

#### 3.1 `{group}` and the default resume title

- In `resume-title`, `{group}` MUST resolve to the resolved `group-id`.
- When `group-id` is empty, `{group}` MUST fall back to `{id}` (the per-instance
  key), never emit the literal text `{group}`.
- The burned-in default `resume-title` (clown#146) MUST become `"{group}"`, so
  that under an orchestrator the title shows the human-readable group (e.g.
  `<repo>/<branch>`) and a bare clown shows its per-instance key.
- `{group}` is available in `resume-title` only. It MUST NOT be valid in the
  `start`/`resume`/`spawn*` argv templates in this revision (the title is the
  only group-bearing surface clown emits); a surviving `{group}` in an argv
  template MUST be rejected as today (RFC-0013 §1.3 rule 2).

##### 3.1.1 Amendment: `sc/{group}/{id}` and clown-name-preferred `{id}` (clown#169)

- The burned-in default `resume-title` is further amended to
  `"sc/{group}/{id}"` — a literal `sc/` prefix (identifying the session as
  spinclass-owned) followed by the group and per-instance placeholders, e.g.
  `sc/clown/deft-elm/bozo` under a spinclass session, or `sc/bozo` for a bare
  clown (via `{group}`'s existing `{id}` fallback, §3.1).
- `{id}` in `resume-title` now prefers a resolved human-ergonomic clown-name
  (clown#169's mutex-allocated name pool, `internal/clownname`) over the raw
  per-instance UUID when one is allocated for the session, falling back to
  the UUID otherwise (e.g. for `--naked`, which skips allocation entirely).
  This preference is a caller-side substitution (`cmd/clown/attach.go`), not
  a change to `Attach.Title`'s own `{group}`/`{id}` substitution contract —
  `Title` remains a pure function over whatever strings it is given. (§3.1.2
  supersedes this last clause: `Title` later gained a `showID` parameter.)
- This is purely a *display* change: the substituted value never affects the
  per-instance routing key, the mux session name, or `Resolve`'s `{id}`
  substitution in argv templates (RFC-0013 §1.3), which continue to use the
  raw UUID.

##### 3.1.2 Amendment: git repo/branch `{group}` fallback + disambiguation-only `{id}` (clown#180)

Two corrections/extensions to §3.1/§3.1.1, all **title-display only** (none
affect the routing key, mux session name, presence `decoration`, or group
chat). Design record: `docs/features/0015-title-repo-fallback-and-id-dedup.md`.

- **Erratum to §3.1.1.** The old `Attach.Title` substituted the separate
  `{id}` placeholder in addition to `{group}`'s empty-group `{id}` fallback,
  so the burned-in `"sc/{group}/{id}"` produced a *duplicated* clown-name
  (`sc/bozo/bozo`) for a bare clown, not the `sc/bozo` §3.1.1 line describes.
  This is now fixed.
- **`{group}` gains a git fallback tier.** §3.1's rule ("when `group-id` is
  empty, `{group}` falls back to `{id}`") is refined into a three-tier
  cascade evaluated once per interactive `[attach]` wrap:
  1. the resolved `group-id` when non-empty (unchanged);
  2. else a best-effort `git rev-parse --show-toplevel` + `git branch
     --show-current` rendered as `<repo-basename>/<branch>` (or just
     `<repo-basename>` on a detached HEAD). This value is **title-display
     only** — it MUST NOT be written to `group-id`/`CLOWN_GROUP_ID`, the
     presence `decoration`, or any chat/group scope, so §2/§8's "empty
     outside spinclass ⇒ ungrouped" contract is unchanged and two unrelated
     bare clowns in one git repo do not become grouped;
  3. else empty (not spinclass, not a git repo) — falls back to `{id}` as
     before.
- **`{id}` is shown only when it disambiguates.** In the true no-group case
  (tier 3) `{id}` is always shown (it is the only identifying info). Under a
  real group (tier 1) or the git fallback (tier 2), `{id}` is shown only when
  2+ live sessions share that scope — counted over the presence index (§4) by
  `decoration` for tier 1, or by a new title-only `cwd` presence field for
  tier 2 (the git-fallback group is never in `decoration`). A solo session
  omits `{id}` and the `/{id}` separator it would introduce.
- **`Attach.Title` contract change.** `Title(id, group string, showID bool)`
  grew the `showID` parameter (superseding §3.1.1's "pure function, unchanged
  contract" clause). When `showID` is false the literal substring `"/{id}"`
  is dropped (not merely substituted empty), so no dangling separator
  survives; the `{group}` fallback and value substitution are otherwise as
  §3.1/§3.1.1. It remains a pure function of its arguments; the caller
  (`cmd/clown/attach.go`) computes `showID` from the presence-based
  disambiguation count.

### 4. Presence index (clown → orchestrator)

clown publishes a presence index that the orchestrator consumes for session
liveness and listing. The index is the normative product clown guarantees;
clown#137 is the implementation vehicle.

#### 4.1 Location and per-record schema

- The index is a directory of per-instance JSON files at
  `$XDG_STATE_HOME/clown/presence/`, mode `0700`, one file per instance named
  `<channelId>.json`.
- Each record MUST contain the following fields:

  | Field | JSON | Type | Meaning |
  |---|---|---|---|
  | SessionKey | `sessionKey` | string | the per-instance routing key (RFC-0013 §2) |
  | ChannelID | `channelId` | string | `ChannelID(sessionKey)` (RFC-0009 §2) |
  | Decoration | `decoration` | string (omitempty) | the resolved `group-id` (§3); empty ⇒ ungrouped |
  | Description | `description` | string (omitempty) | a human-readable label (e.g. `SPINCLASS_DESCRIPTION`) |
  | LastSeen | `lastSeen` | string | RFC 3339 (nanosecond) UTC timestamp of last refresh |

  Example:

  ```json
  {"sessionKey":"06f1...80a7","channelId":"10e0...473e","decoration":"clown/deft-elm","description":"fix attach defaults","lastSeen":"2026-06-15T10:26:56.116928322Z"}
  ```

#### 4.2 Freshness and lifecycle

- A live clown's monitor MUST refresh its own record's `lastSeen` on a periodic
  cadence; the write MUST be atomic (temp file + rename).
- A record whose `lastSeen` is older than the staleness window MUST be treated
  as dead by readers. The staleness window MUST be a comfortable multiple of the
  refresh cadence (the reference implementation uses **2 minutes**), so a couple
  of missed refreshes do not drop a live session.
- A clean monitor shutdown SHOULD remove its own record immediately rather than
  waiting out the staleness window.
- Presence is **best-effort**: a presence read/write failure MUST NOT break the
  monitor, chat, or any clown runtime path.

#### 4.3 Query contract for the orchestrator

The orchestrator answers "is this session alive" and renders its listing as
follows:

- **Liveness.** A session identified by group key `G` is live iff some presence
  record has `decoration == G` and a `lastSeen` within the staleness window.
- **Listing (1-to-many).** The set of clowns under a worktree is the set of
  records with `decoration == G`; this is the source for the orchestrator's
  one-worktree-many-clowns view.
- The orchestrator MUST obtain records either by reading the directory in §4.1
  directly, or via clown's `chat list` surface, which MUST emit the §4.1 record
  set as JSON (stale entries excluded). Both paths MUST yield records conforming
  to §4.1.
- The orchestrator's generic (non-clown) liveness fallback is owned by the
  orchestrator and is out of scope here.

### 5. Detached-spawn executor wire (orchestrator → clown spawn mode)

Per the 2026-06-15 decision, the detached-spawn executor moves to clown
(resolving the RFC-0013 §1.3 open question). The orchestrator's `sc spawn`
launches a detached worker by exec'ing clown in **spawn mode**; clown's
`[attach].spawn` template performs the detach. The wire is:

#### 5.1 Spawn-mode selection

- clown MUST provide an explicit, argument-based signal by which a launcher
  selects spawn mode for a fresh launch (distinct from the `start`/`resume`
  detection of RFC-0013 §1.3, which keys off forwarded `--resume`/`-r`/
  `--session-id`). This document specifies a hidden flag
  `--clown-attach=spawn`, consistent with the argument-not-environment hygiene
  of `--clown-attach-id` (clown#136).
- In spawn mode with a multiplexer enabled, clown MUST resolve the
  `[attach].spawn` template (not `start`/`resume`).

#### 5.2 Prompt-return guarantee

- clown's spawn-mode self-wrap MUST return control to its parent process
  promptly: the resolved `spawn` template MUST background the worker (e.g.
  `zmx attach {id} --detach {entry}`) and the outer clown process MUST exit
  shortly after launching it. clown MUST NOT block in the foreground in spawn
  mode.
- This guarantee exists so the orchestrator's blocking launch (`cmd.Run`)
  returns within its hello-handshake deadline; a foreground block would burn it.

#### 5.3 Worker boot guarantees

- The spawned inner worker (the `{entry}` clown running under the multiplexer)
  MUST boot with its working directory set to the worktree directory, and MUST
  fire its harness `SessionStart` hook, so the orchestrator's hello handshake
  completes.
- clown's spawn-mode re-exec MUST preserve the worker's orchestrator decoration
  environment (§6) unchanged — it MUST NOT strip or rename the decoration
  variables — so the inner worker resolves the correct `group-id` and keys its
  group channel and hello correctly.

### 6. The decoration environment contract (the wire)

The contract between orchestrator and clown is the set of decoration environment
variable **names** the orchestrator exports and clown's configuration may
interpolate (§2). These names are normative:

| Variable | Role |
|---|---|
| `SPINCLASS_SESSION_ID` | the session key; the canonical `group-id` source |
| `SPINCLASS_REPO` | repo component of the key (coarser grouping) |
| `SPINCLASS_BRANCH` | branch component (display hint) |
| `SPINCLASS_WORKTREE` | absolute worktree path |
| `SPINCLASS_DESCRIPTION` | human-readable session label (presence `description`) |

- clown MUST NOT reference these names in code (§1); they appear only in
  configuration — clown's burned-in default clownfile (§2) and/or operator
  clownfiles.
- The producer-side semantics — when each variable is set, that the
  orchestrator-owned variables are written last (authoritative over user env),
  and the `CLOWN_SESSION_ID`/`CLAUDE_SESSION_ID` strip rule (clown#169) — are
  specified by the spinclass export contract (see References) and are normative
  there. clown relies only on the names above being present in its environment
  when grouping is desired.

### 7. Cutover ordering

Removing the hardcoded `SPINCLASS_SESSION_ID` read (§2.2) would, on its own, be a
flag-day for the group chat channel: the instant clown stops reading
`SPINCLASS_SESSION_ID`, a deployment with no `group-id` set would lose its group
channel (group-addressed sends stop landing). Because clown ships the
`group-id = "${SPINCLASS_SESSION_ID}"` binding in its own burned-in default (§2),
that gap is eliminated:

- The clown release that removes the hardcoded read MUST also ship the burned-in
  default carrying the binding, so the removal and its replacement land
  **atomically in one release**. No operator action and no configuration-leads-
  removal ordering is required; an upgrading clown derives the same group key from
  its burned-in default that it previously derived from the hardcoded read.
- The orchestrator's removal of its multiplexer templates (it exits
  multiplexing, §1) MUST NOT precede the clown release that ships the `[attach]`
  self-wrap defaults (clown#146, already shipped), so there is never a window in
  which neither side wraps.
- This RFC specifies the config-only end state (no transitional in-code
  `SPINCLASS_SESSION_ID` fallback); the burned-in default makes one unnecessary.

### 8. Bare-clown and bare-orchestrator guarantees

- **Bare clown** (no `SPINCLASS_*` in the environment): the burned-in
  `group-id = "${SPINCLASS_SESSION_ID}"` interpolates to the empty string, so the
  session is ungrouped — no group channel, empty presence decoration,
  `resume-title` `{group}` falls back to the per-instance `{id}` (§3.1).
  Per-instance chat, presence (as a singleton), and attach all work. clown MUST
  run fully functional in this state.
- **Bare orchestrator** (harness ≠ clown): exports the decoration env (§6) as
  usual; with no clown there is no presence index, so the orchestrator's generic
  liveness fallback (orchestrator-owned) applies.

## Security Considerations

- **Environment interpolation is textual, not executable.** `group-id`
  interpolation (§2.1) performs string substitution of environment values into a
  single configuration string; it MUST NOT invoke a shell, expand command
  substitutions, or execute any argv. It is therefore not an injection vector
  beyond the value of the named variable itself. (The clownfile's argv templates
  remain the executable surface governed by RFC-0013 §1.3 trust rules; this
  document adds no new executable surface.)
- **Group key as a chat-routing capability.** The resolved `group-id` keys the
  group chat channel; any clown that resolves the same `group-id` joins that
  group and receives its fan-out messages. Operators MUST treat the `group-id`
  binding as a trust boundary: a clownfile discovered in an untrusted working
  directory could set `group-id` to join a group it should not. clown SHOULD
  apply the same clownfile-trust posture as RFC-0013 §1.3 (untrusted-ancestor
  clownfiles). Channel IDs are one-way hashes (`ChannelID`, RFC-0009 §2), so the
  group key is not recoverable from the channel id, but knowledge of the
  `group-id` value confers group membership.
- **Presence is host-local and best-effort.** The presence index lives under
  `$XDG_STATE_HOME/clown/presence/` at mode `0700` (§4) and MUST NOT be
  transmitted off-host. It carries a human-readable `description` and the
  decoration; these MUST be considered local-trust metadata, not secrets. A
  stale or missing presence record MUST degrade liveness to "unknown/dead", never
  to a false "alive".
- **Spawn-mode env passthrough.** §5.3 requires preserving the decoration env
  into the spawned worker. Implementations MUST continue to honor the
  `CLOWN_SESSION_ID`/`CLAUDE_SESSION_ID` strip rule (clown#169, owned by the
  orchestrator) so a spawned worker does not inherit and arm the launcher's
  routing channel.

## Conformance Testing

Conformance tests for clown's half of this specification live in
`zz-tests_bats/` and `internal/clownfile` / `internal/jobwake` Go tests.

Tests use binary injection via `bats-emo`:

    require_bin CLOWN_BIN clown

### Covered Requirements

| Requirement | Test File | Description |
|-------------|-----------|-------------|
| §2.1 env interpolation of `group-id` | `internal/clownfile` | `${NAME}` expands; unset ⇒ empty; composes within string |
| §2.2 config-only group key | `internal/jobwake` | group key derives from `group-id`; no `SPINCLASS_SESSION_ID` code read |
| §3 group-channel/decoration keying | `internal/jobwake` | group channel = `ChannelID(group-id)`; presence decoration = `group-id` |
| §3.1 `{group}` title + fallback | `internal/clownfile` | `{group}` → group key; empty ⇒ falls back to `{id}` |
| §4.1/§4.2 presence schema + staleness | `internal/jobwake` | record fields; stale entries dropped; atomic refresh |
| §4.3 `chat list` JSON | `zz-tests_bats` | `chat list` emits §4.1 records as JSON, stale excluded |
| §5.1/§5.2 spawn-mode + prompt-return | `zz-tests_bats` | `--clown-attach=spawn` resolves `spawn` template, returns promptly (stub mux) |
| §5.3 spawn env passthrough | `zz-tests_bats` | decoration env preserved into the spawned `{entry}` |
| §8 bare-clown ungrouped | `internal/jobwake`, `zz-tests_bats` | no decoration ⇒ no group channel, `{group}`→`{id}` |

## Compatibility

- **Supersedes the RFC-0013 §1.3 open question.** RFC-0013 §1.3 left the
  detached-spawn executor unresolved; §5 of this document resolves it (the
  executor moves to clown). RFC-0013 should be updated to reference this
  document for the spawn wire.
- **Migration is atomic, not a flag-day.** Existing deployments rely on clown
  reading `SPINCLASS_SESSION_ID` directly for grouping. Because clown ships the
  `group-id = "${SPINCLASS_SESSION_ID}"` binding in its own burned-in default
  (§2), a single clown release both removes the hardcoded read and supplies the
  replacement binding — an upgrading clown derives the same group key with no
  operator action (§7). The only remaining ordering constraint is that the
  orchestrator drops its multiplexer templates no earlier than clown's shipped
  `[attach]` defaults (clown#146, already shipped).
- **`resume-title` default change.** The burned-in default `resume-title`
  changes from `"{id}"` (clown#146) to `"{group}"` (§3.1) with `{id}` fallback;
  this is backward-compatible for bare clown (the fallback yields the prior
  value) and an improvement under an orchestrator (human-readable title instead
  of a minted UUID). Amended a second time (§3.1.1, clown#169) to
  `"sc/{group}/{id}"`, with `{id}` additionally preferring a resolved
  clown-name over the raw UUID — both changes are display-only and
  backward-compatible (a session with no allocated name falls back to the
  prior UUID behavior).
- **`resume-title` firing condition widened (clown#169).** RFC-0013 §1.3
  originally specified `resume-title` as emitted "immediately before a
  `resume` attach" only; it now also fires before a fresh `start` attach, so
  a new interactive session gets a terminal title too, not only a reattach.
  RFC-0013 §1.3's field description is updated to match.
- **`{group}` git fallback + disambiguation-only `{id}` (clown#180, §3.1.2).**
  A bare clown in a git repo now shows `sc/<repo>/<branch>` instead of just
  the clown-name, and the clown-name is shown only when 2+ live sessions share
  the scope. Backward-compatible and display-only: a session outside any git
  repo falls back to the prior `{id}` behavior, and none of it touches the
  routing key, `decoration`, or grouping. Also fixes the `sc/bozo/bozo`
  duplicate the pre-fix `Attach.Title` emitted for a bare clown.

## References

### Normative

- [RFC 2119] Key words for use in RFCs to Indicate Requirement Levels.
- [RFC-0009] Job-wakeup channel — `ChannelID`, channel derivation, group/
  broadcast delivery (`docs/rfcs/0009-job-wakeup-channel.md`).
- [RFC-0013] clownfile, per-instance identity, and clown-owned chat — the
  clownfile cascade (§1), per-instance identity (§2), chat and the group channel
  (§3), and the `[attach]` table (§1.3) this document extends
  (`docs/rfcs/0013-clownfile-per-instance-identity-and-chat-ownership.md`).
- [spinclass export contract] The orchestrator's `SPINCLASS_*` decoration export
  and consumer (liveness / `sc list`) half, co-authored: spinclass
  `docs/plans/2026-06-15-clown-spinclass-awareness-seam.md`, ratified via
  spinclass FDR-0017.

### Informative

- [spinclass FDR-0017] clown ⇆ spinclass session-attach, grouping, and chat
  ownership rescope — the orchestrator-side view.
- [clown#137] Presence index integration so spinclass can identify clowns within
  a session.
- [clown#145 / clown#146] The `[attach]` multiplexer self-wrap and its burned-in
  defaults.
- [clown#169] The `CLOWN_SESSION_ID`/`CLAUDE_SESSION_ID` strip rule.
