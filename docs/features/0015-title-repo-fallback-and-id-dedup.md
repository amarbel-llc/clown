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
evaluated once per launch, immediately after the `[attach]` wrap decision
(`emitSessionTitle`):

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

The `{id}` segment (the clown-name) is shown except where it disambiguates
nothing:

- **True no-group case** (tier 3 above): the clown-name is the only
  identifying information available, so it always appears — but exactly ONCE.
  When the template contains `{group}`, `{group}`'s own empty-group fallback
  is what renders it, so the separate `{id}` placeholder is suppressed and the
  burned-in `sc/{group}/{id}` renders `sc/bozo`, matching the Examples block
  below. When the template has no `{group}` to fall back (e.g. a bare `{id}`),
  nothing else would render the name, so `{id}` is kept. See the clown#229
  amendment below.
- **Tier 1** (a real spinclass group): `{id}` is ALWAYS shown. See the
  clown#230 amendment below — this reverses the original design.
- **Tier 2** (the git-repo fallback): `{id}` is ALWAYS shown. See the
  clown#234 amendment below — like tier 1, this reverses the original design,
  which showed it only when 2+ live sessions shared a cwd.

### Amendment (clown#230): tier 1 always shows `{id}`

As originally specified, tier 1 deduped exactly like tier 2, counting live
presence records by `Decoration`. **That threshold was unsatisfiable by
construction.** Under spinclass, `Decoration` is the group-id `<repo>/<branch>`
and spinclass creates one clown per worktree, so the scope is 1:1 with a clown:
the count is always 1, `showID` is always false, and the clown-name was stripped
from the title of *every* live fleet session. The worked example below for "2+
clowns in the same group" describes a configuration that does not occur.

This fires the dedup-threshold lever's own change signal (see Tuning Levers).
The title is the surface on which a session is identified, and bare clown-names
collide across concurrent sessions — a single presence snapshot had `bozo` live
twice in two different groups — so the fully-qualified
`sc/<repo>/<spinclass-session>/<clown-name>` form must always be complete.
Tier 1's dedup is therefore removed outright rather than made opt-out: there is
no configuration in which it would have carried information.

Tier 2 keeps its dedup unchanged. Its scope is a working directory, which
genuinely can hold one clown or several, so the count is informative there.

### Amendment (clown#231, clown#232): emitted from the inner clown, unconditionally by mode

The title was emitted pre-exec by the OUTER clown, and only for `ModeStart` /
`ModeResume`. Both halves were wrong:

- **Emission point.** Writing before handing the terminal to the multiplexer
  set the outer terminal's title only; nothing ever wrote an OSC sequence into
  the multiplexer session's pty, so the mux daemon held no title for any clown
  session — leaving a stale title on session switch and no title in its
  activity view. The title is now emitted AFTER the `[attach]` wrap decision,
  by whichever process goes on to run the provider: the inner clown inside the
  mux pty, or an un-wrapped clown running inline. A successful wrap never
  returns, so that is a single unambiguous call site.
- **Mode gate.** `ModeSpawn` was excluded as a detached-worker launch with no
  terminal to title. It has one — it just arrives later, when a human attaches
  to the durable session the spawn created. Since essentially every fleet
  session is spawned, essentially none got a title. The mode gate is removed;
  emission is gated only on this process having an interactive terminal
  (`CLOWN_ATTACH_FORCE=1` overrides, matching the wrap gate). A spawn's inner
  clown passes that check with a real pty and its outer, with `/dev/null` stdio,
  does not.

Emission is consequently no longer coupled to the re-wrap loop guard: the inner
attached clown declines to re-wrap AND emits the title.

`internal/clownfile.Attach.Title(id, group string, showID bool) string` grew a
third parameter: when `showID` is false, the literal substring `"/{id}"` (not
just `"{id}"`) is dropped, so no dangling separator survives regardless of
where `{id}` sits in the template relative to `{group}`.

### Amendment (clown#229): tier 3 shows the clown-name once, not twice

The Interface section originally said tier 3 shows `{id}` ALWAYS, and the caller
implemented that literally. It contradicted this record's own Examples block
(and RFC-0014 §3.1.1), both of which give `sc/bozo`: `Title` already falls
`{group}` back to the id when the group is empty, so also substituting the
separate `{id}` rendered the burned-in `sc/{group}/{id}` as `sc/bozo/bozo`.

The fix is in the caller (`emitSessionTitle`), not the renderer: `Title`'s
empty-group fallback stays, because a custom template using only `{group}` must
still carry the name. The caller simply stops forcing `{id}` once `{group}` has
already rendered it.

"Already rendered it" is the load-bearing part, and it is a property of the
TEMPLATE, not of the tier. A `{group}`-less template (a bare `{id}`, which
`TestEmitSessionTitlePrefersClownName` exercises) has no fallback to supply the
name, so suppressing `{id}` there would render the empty string — and since
emission is skipped for an empty title, such a session would get no title at
all. The caller therefore suppresses `{id}` in tier 3 only when the template
contains `{group}`.

### Amendment (clown#234): tier 2 always shows `{id}` too — no tier dedups

Tier 2 kept its dedup when clown#230 removed tier 1's, on the reasoning that a
working directory genuinely can hold one clown or several, so the count carries
information. In practice it meant a lone bare clown in a git repo rendered
`sc/<repo>/<branch>` with no clown-name — the operator noticed the omission and
asked for it back. That is the same change signal tier 1's lever fired, applied
to the surviving one: a title is how a session is identified, and bare
clown-names collide across concurrent sessions, so the id belongs in it whether
or not a second session happens to share the directory right now.

So `{id}` is now shown whenever a group resolved at all, tier 1 and tier 2
alike. Tier 3's clown#229 rule is unchanged — it still drops the separate `{id}`
when, and only when, the template's `{group}` already rendered the name.

Consequences: `titleDisambiguationNeeded` is deleted (nothing calls it),
`emitSessionTitle` no longer needs a `usingGitFallback` flag or a `Getwd`, and
computing a title now reads no presence records at all. `jobwake.Presence.Cwd`
was added solely to feed this dedup and is now unused by clown; it lives in
ringmaster, so its removal is that repo's decision.

**Testing note.** The tier-2 test that pinned the old behavior had never
actually run: `git` was absent from the `clown-go-test` sandbox, so it hit
`t.Skip` while the suite still reported `ok`. Inverting it alone would have
verified nothing. `git` is now a `nativeCheckInputs` of that derivation, and the
git-dependent tests build a throwaway repo in a temp dir rather than assuming
the ambient cwd is a checkout — which is what makes them run in the nix lane
(and hence in the pre-merge gate) at all.

## Examples

```
# Spinclass session (always — one clown per worktree is the only shape):
title: sc/clown/deft-elm/bozo

# Bare clown in a git repo outside spinclass — solo or not, since clown#234
# retired the last dedup:
title: sc/clown/brave-banyan/bozo

# Bare clown, not in a git repo at all (e.g. /tmp):
title: sc/bozo
```

## Limitations

**~~The git-fallback tier adds a new, narrowly-scoped presence field
(`jobwake.Presence.Cwd`).~~** RETIRED by clown#234. The field existed ONLY to
let the title's dedup count "how many live sessions are in this exact working
directory"; with tier 2's dedup gone, clown no longer reads it. The field still
exists in ringmaster (`jobwake.Presence.Cwd`) and is now unused by clown —
whether to remove it is ringmaster's call in its own repo, not clown's.

**Two subprocess calls per titled launch when ungrouped.** The git fallback
shells out to `git` twice (toplevel + branch) whenever `flags.groupID` is
empty. Best-effort: any failure (not a git repo, `git`
missing) degrades silently to the true no-group tier, matching the rest of
this subsystem's "never fail the launch over a cosmetic feature" contract
(`internal/clownname.Claim`'s doc comment states the same policy).

**~~Dedup is presence-based, so it inherits presence's own staleness
window.~~** RETIRED by clown#234: no tier dedups any more, so computing a title
reads no presence records and cannot be skewed by a stale one.

**Branch-rename does not retroactively update an already-emitted title.**
The title is computed once, at attach time. Renaming the branch mid-session
does not re-emit the OSC-2 sequence.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| ~~dedup threshold (tier 2)~~ | ~~2+ live sessions sharing a cwd~~ | RETIRED by clown#234 — the lever's own change signal ("users want the id shown even solo") fired; tier 2 now always shows the id | — |
| ~~dedup threshold (tier 1)~~ | ~~2+ live sessions~~ | RETIRED by clown#230 — the threshold was unsatisfiable under spinclass, so the id was never shown; tier 1 now always shows it | — |
| ~~presence staleness reused for dedup~~ | ~~2 minutes (existing `presenceStale`)~~ | RETIRED by clown#234 — no tier dedups any more, so the title reads no presence records at all | — |
| emission gate | this process has an interactive terminal (`CLOWN_ATTACH_FORCE=1` overrides) | the emitter is whichever process owns the terminal; keeps OSC bytes out of a redirected stderr | non-interactive runs turn out to want a title anyway, or a mux gives the inner process a pty that fails TTY detection |

## More Information

- RFC-0014 §3.1/§3.1.1 — the `{group}`/`{id}` placeholder contract this
  feature amends (the duplicate-`{id}` bug and its fix are documented there
  as an erratum, not a re-spec).
- `internal/clownname`'s `Claim`/`Allocate` — the clown-name allocator whose
  output is `{id}` here; unaffected by this feature.
- Implementation: `cmd/clown/attach.go` (`emitSessionTitle`, called from
  `runWithFlags` right after `maybeReexecMultiplexer`),
  `internal/clownfile/clownfile.go` (`Attach.Title`), and
  `code.linenisgreat.com/ringmaster/pkgs/jobwake` (`Presence.Cwd`,
  `RegisterPresenceKey`).
