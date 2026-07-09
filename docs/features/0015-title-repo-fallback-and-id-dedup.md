---
status: proposed
date: 2026-07-09
promotion-criteria: real usage across a few non-spinclass, non-git, and multi-session sessions with no title regressions or missing-context complaints
---

# OSC-2 title repo/branch fallback outside spinclass, and disambiguation-only clown-name

## Problem Statement

The burned-in `resume-title` template is `"sc/{group}/{id}"`: `{group}` is the
spinclass group-id (`repo/branch`), `{id}` is the clown-name (clown#169). Under
spinclass this reads well — `sc/clown/deft-elm/bozo`. Outside spinclass,
`{group}` is empty and falls back to `{id}`, but the OLD `Title()`
implementation then ALSO substituted the separate `{id}` placeholder with the
same value, producing a literal duplicate: `sc/bozo/bozo` instead of the
RFC-0014 §3.1.1-documented `sc/bozo`.

Fixing the duplicate alone would leave every non-spinclass title as just the
bare clown-name (`sc/bozo`), with no repo context — worse than the spinclass
case, which always shows `repo/branch`. And even where the clown-name IS
shown, it is often redundant: a solo session's title does not need its
clown-name to disambiguate anything, since there is nothing else to
disambiguate from.

## Interface

The title's `{group}` segment now resolves through a three-tier cascade,
evaluated once per interactive `[attach]` wrap (`maybeReexecMultiplexer`):

1. **Spinclass group-id** (`flags.groupID`, from `CLOWN_GROUP_ID` /
   `${SPINCLASS_SESSION_ID}` via the clownfile) — used as-is when non-empty.
2. **git repo/branch fallback** — when (1) is empty, clown runs `git
   rev-parse --show-toplevel` + `git branch --show-current` in the current
   working directory. On success this renders as `<repo-basename>/<branch>`
   (or just `<repo-basename>` when HEAD is detached / branch resolution
   fails). This value is TITLE-DISPLAY ONLY: it is never written to
   `flags.groupID`, `CLOWN_GROUP_ID`, presence `Decoration`, or any
   chat/group-messaging scope. Two unrelated bare clowns in the same git repo
   do not become chat/presence-grouped by this fallback — RFC-0014 §2's
   "empty outside spinclass" grouping contract is unchanged.
3. **None** — not in spinclass, not in a git repo (or `git` is unavailable):
   the resolved group stays `""`, exactly as before this feature.

The `{id}` segment (the clown-name) is shown only when it disambiguates:

- **True no-group case** (tier 3 above): `{id}` is ALWAYS shown — it is the
  only identifying information available, so it is never suppressed.
- **Tier 1 or 2** (a real spinclass group or a git-repo fallback): `{id}` is
  shown only when 2+ live clown sessions share that same group/cwd. A solo
  session's title omits it (and the redundant `/{id}` separator it would
  have introduced).
  - Tier 1's dedup counts live `jobwake.Presence` records by the existing
    `Decoration` field (already scoped to the real group-id).
  - Tier 2's dedup counts live `jobwake.Presence` records by a NEW `Cwd`
    field (added specifically for this — see Limitations), since the
    git-fallback group is never written to `Decoration`.

`internal/clownfile.Attach.Title(id, group string, showID bool) string` grew a
third parameter: when `showID` is false, the literal substring `"/{id}"` (not
just `"{id}"`) is dropped, so no dangling separator survives regardless of
where `{id}` sits in the template relative to `{group}`.

## Examples

```
# Spinclass session, solo (no other clown in this group):
title: sc/clown/deft-elm

# Spinclass session, 2+ clowns in the same group:
title: sc/clown/deft-elm/bozo

# Bare clown in a git repo outside spinclass, solo:
title: sc/clown/brave-banyan

# Bare clown in a git repo outside spinclass, 2+ clowns in the same cwd:
title: sc/clown/brave-banyan/bozo

# Bare clown, not in a git repo at all (e.g. /tmp):
title: sc/bozo
```

## Limitations

**The git-fallback tier adds a new, narrowly-scoped presence field
(`jobwake.Presence.Cwd`).** It exists ONLY to let the title's dedup count
"how many live sessions are in this exact working directory" — it is not a
general-purpose field, is not exposed in `clown presence list`'s default
human output, and must not be repurposed as a substitute for `Decoration` /
group-id. Mirrors the narrow, single-purpose addition pattern used for
`ClownName` (clown#179).

**Two subprocess calls per interactive `[attach]` wrap when ungrouped.** The
git fallback shells out to `git` twice (toplevel + branch) whenever
`flags.groupID` is empty. Best-effort: any failure (not a git repo, `git`
missing) degrades silently to the true no-group tier, matching the rest of
this subsystem's "never fail the launch over a cosmetic feature" contract
(`internal/clownname.Claim`'s doc comment states the same policy).

**Dedup is presence-based, so it inherits presence's own staleness window.**
A session that crashed without cleanup is still "live" for up to
`presenceStale` (2 minutes) after its last refresh, so a title computed
during that window may count a stale session and show `{id}` when, in
hindsight, there was really only one active session. Self-corrects on the
next presence refresh cycle; not worth tracking more precisely for a
cosmetic feature.

**Branch-rename does not retroactively update an already-emitted title.**
The title is computed once, at attach time. Renaming the branch mid-session
does not re-emit the OSC-2 sequence.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| dedup threshold | 2+ live sessions | matches "only show id when it disambiguates something" | users want the id shown even solo (e.g. for muscle-memory copy-paste into `clown --naked` or scripts) |
| presence staleness reused for dedup | 2 minutes (existing `presenceStale`) | avoids a second, title-specific staleness constant | dedup false-positives from stale sessions become noticeably common |

## More Information

- RFC-0014 §3.1/§3.1.1 — the `{group}`/`{id}` placeholder contract this
  feature amends (the duplicate-`{id}` bug and its fix are documented there
  as an erratum, not a re-spec).
- `internal/clownname`'s `Claim`/`Allocate` — the clown-name allocator whose
  output is `{id}` here; unaffected by this feature.
- Implementation: `cmd/clown/attach.go` (`maybeReexecMultiplexer`'s title
  block), `internal/clownfile/clownfile.go` (`Attach.Title`), and
  `code.linenisgreat.com/ringmaster/jobwake` (`Presence.Cwd`,
  `RegisterPresenceKey`).
