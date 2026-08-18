---
status: testing
date: 2026-08-18
promotion-criteria: a rebuild+restart of a live spinclass session on the fleet keeps its clown name (the twerk regression the operator reported no longer reproduces), observed across several sessions, with no name-collision or resume-by-name regressions
---

# Clown name bound to the harness session lineage

## Problem Statement

The clown name (clown#169: `bozo`, `krusty`, ...) shown in troupe presence
(`chat_list`'s `clownName`) did not stay bound to the underlying session.
Every launch minted a fresh name from the pool, so the SAME session was
observed as `krusty` then `coco` within one day, and names got reassigned
between sessions across restarts (operator-reported, corroborated again after
a fleet rebuild+restart on twerk, 2026-08-17/18).

This is a bug, not a cosmetic quirk, because a name that cycles cannot anchor
stable addressing. The planned session↔Snikket gateway (circus#163) addresses
DM puppets as `<repo>/<worktree>/<clown-name>` (e.g. `circus/clear-walnut/coco`)
and keys conversation threads on that triple. That is only stable if the name
is pinned to the session lineage: a restart/resume MUST keep the name, and only
a genuinely new session on the same worktree should mint a new one (which then
correctly surfaces as a new contact). Name reuse across concurrent sessions
also makes presence listings ambiguous for the operator.

## Interface

The clown name is now bound to the **harness session lineage**, keyed by
clown's per-session identity key (`flags.identity.Key`, `sessionIdentity`):

1. **Lineage key.** `identity.Key` is clown's canonical per-session routing
   key (RFC-0009 §2; RFC-0013 §2.3), resolved from
   `CLOWN_SESSION_ID → CLAUDE_SESSION_ID → freshly-minted UUID`. For the claude
   provider, `decideClaudeSession` has additionally UNIFIED it with the claude
   `--session-id` (RFC-0013 §2.1), so it is stable across a restart/resume of
   the same conversation and freshly minted only for a genuinely new session —
   exactly the lineage property this needs.

2. **Resolution consults the binding before minting.**
   `resolveClownName(attachedID, inheritedName, harnessSessionID)` now, before
   falling back to `clownname.Claim()`, looks the name up in the persistent
   session-names journal (`internal/sessions.NameOf(harnessSessionID)`). A hit
   reuses the previously-bound name; a miss mints a fresh one. The existing
   `[attach]`-inner-process reuse of the inherited `CLOWN_NAME` still takes
   precedence (an inner process trusts the outer's already-resolved name). A
   bound record containing `.` is rejected and skipped (clown#217), never
   resurrected into presence or room JIDs.

3. **Resolution moved after the session-id decision.** The
   `resolveClownName` call in `runWithFlags` moved from before to AFTER
   `decideClaudeSession`, so the name binds to the FINAL `identity.Key`. It
   still runs BEFORE the multiplexer re-exec, so the OSC-2 title bake and the
   `[attach]`-inner process inherit `CLOWN_NAME` exactly as before.

4. **The binding is written for every provider.** The session-names journal
   write (formerly claude-only, keyed by `resumeHintID` for clown#192
   resume-by-name) now records under `identity.Key` for ALL non-`--naked`
   providers. For claude it ALSO records under the claude conversation id when
   that differs from `identity.Key` (the non-UUID operator-key edge, where
   `decideClaudeSession` keeps the operator key as the channel but mints a
   separate `--session-id`), so `clown resume repo/worktree/<name>` still
   resolves a dead conversation by the id claude persisted it under.

**Harness-agnostic by construction.** Nothing above is claude-specific except
the `decideClaudeSession` unification and the resume-by-name compat write. Any
provider — now (codex, juggler, opencode, crush) or future — that carries a
stable session id into `identity.Key` (via `CLOWN_SESSION_ID`/`CLAUDE_SESSION_ID`
or its own equivalent) gets the same persistence for free. A provider whose id
is minted fresh each launch simply finds no prior record and mints a new name:
the correct degradation, since there is no stable lineage to bind to.

## Examples

```
# Genuinely new session (fresh identity.Key): mints from the pool.
session A, launch 1  -> identity.Key=id-A  -> Claim() -> "bozo", record(id-A,bozo)

# Restart/resume of the SAME session (same identity.Key): keeps the name.
session A, launch 2  -> identity.Key=id-A  -> NameOf(id-A)="bozo" -> "bozo"

# A different, concurrent session on the same worktree: its own new name.
session B, launch 1  -> identity.Key=id-B  -> Claim() -> "krusty"

# Provider with no stable session id (fresh UUID each launch): no persistence.
bare `clown --provider codex` (no CLOWN_SESSION_ID) -> fresh id each launch -> new name
```

## Limitations

**Persistence is exactly as strong as the harness's session-id stability.**
The fix binds to `identity.Key`; where that is a freshly-minted UUID each
launch (a bare `clown` outside spinclass with no `CLOWN_SESSION_ID`, or claude
`--continue`/`-c`, whose `resumeHintID` is empty and whose key may be
freshly minted) there is no stable lineage and the name is not preserved. The
fleet's primary paths ARE covered: spinclass sessions carry a stable
`CLOWN_SESSION_ID`, and `clown resume` / the `[attach]` multiplexer-resume
preserve the claude `--session-id`.

**The session-names journal is never pruned, and now grows on non-claude
launches too.** Every non-`--naked` launch writes an `id→name` record;
for an ephemeral non-claude launch with a fresh id, nothing later looks that
record up. Records are tiny and the file is append-only by design (clown#192),
so this is minor, but it is unbounded in launch count.

**Narrow re-collision window across a session's downtime.** clownname recycles
a name once its presence record goes stale (recycle-on-death, clown#169). If
session A dies, session B claims A's freed name during A's downtime, and A then
resumes, A reclaims its bound name unconditionally — so both are briefly live
under the same name until the next presence cycle. Reuse is unconditional by
design (the issue's intent is "the same session keeps its name"); perfectly
avoiding this would require reserving a dead session's name against recycling,
which contradicts clownname's model.

**Best-effort, never fails the launch.** Both the `NameOf` lookup and the
`RecordSessionName` write are best-effort (matching the presence-registration
and `clownname.Claim` contracts): any miss only degrades name persistence /
name-based resume for that one session, never the launch.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| lineage key | `identity.Key` (harness session id) | the one per-session id clown already threads to presence/monitor/producers; stable across restart, fresh per new session | a harness surfaces a better lineage id than its session id |
| reuse-vs-liveness on a bound hit | reuse unconditionally | the issue's intent is "keep the name"; the re-collision window is narrow | bound-name re-collisions become common enough to warrant a liveness check + generation bump on conflict |
| journal write scope | all non-`--naked` providers | harness-agnostic persistence | journal growth from ephemeral non-claude launches becomes a real cost (then gate the write on a stable-id signal) |

## More Information

- clown#216 — the bug this feature fixes (names cycle across restart).
- clown#169 / `internal/clownname` — the name pool + `Allocate`/`Claim`
  allocator whose output is bound here; unchanged except a doc note.
- clown#217 — the `.`-in-names ban; `clownname.Validate` guards a dotted bound
  record on this path.
- clown#192 / `internal/sessions` — the append-only session-names journal
  (`RecordSessionName`/`NameOf`) reused as the binding store; its
  resume-by-name consumer (`cmd/clown/resume_key.go`) is preserved via the
  claude compat write.
- RFC-0013 §2 — session identity / `--session-id` unification that makes
  `identity.Key` a stable lineage key for claude.
- FDR-0015 — the OSC-2 title's use of the clown-name (`{id}`), which this
  feature makes stable across restart.
- Implementation: `cmd/clown/main.go` (`resolveClownName`, the moved
  resolution block, the generalized journal write).
