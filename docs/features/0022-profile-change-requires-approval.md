---
status: accepted-for-implementation
date: 2026-10-23
issue: clown#295
related: FDR 0005 (hats), FDR 0006 (single-entrypoint matrix), spinclass FDR 0032 / 0034, spinclass#407, piggy RFC 0012 (operator-act@piggy, namespace piggy-escalation-v1)
promotion-criteria: >
  The operator has answered every open question (decision 8 amended,
  decisions 9 to 12). experimental once the slice 3a work lands on master and spinclass
  launches with --profile, CLOWN_PROFILE_DIGEST and CLOWN_PROFILE_SESSION.
---

# Profile changes at runtime require user approval

> **Mostly implemented:** slices 1, 2 and 3a are in clown (see
> Implementation slices); slice 3 (the field diff) and the spinclass half
> are design. The Problem section describes clown before them. Claims not read in
> source are marked **UNVERIFIED**. clown owns profile resolution and the
> digest; spinclass owns session state, the re-pin right and the grant
> store; piggy owns the signing act. This document states clown's side and
> the seams, and does not decide the other repos' internals.

## Problem

A profile (`internal/profile`, resolved by `pkg/profilelookup`) fixes the
provider, model, URL, env, context-server keep-set and `base` a session
runs with. It can change under a session: the user edits `profiles.toml`,
a builtin changes in a clown upgrade, or a protocol bump changes every
canonical form. Today:

- spinclass pins the profile's content digest at spawn; `sc resume` refuses
  on a changed digest until a human runs `sc resume --repin-profile`
  (interactive confirmation, CLI-only). That leaves no signed record and
  cannot be approved from another surface (spinclass#407).
- `clown resume` launches with **no profile**: it never calls `parseFlags`,
  rejects `--profile`, and `providerExplicit=true` suppresses the clownfile
  pin and default (read from `cmd/clown/resume.go`; **UNVERIFIED** by
  running). clown stores no per-session profile (clown#295).

The rule to establish: *a session's effective profile never changes
silently, and a change needs approval that the launcher pins.*

What clown's check actually guarantees: it guards against operator
mistakes and silent drift (an edited `profiles.toml`, a clown upgrade). It
does not stop an agent that can run clown with an environment of its
choosing (it can set `CLOWN_PROFILE_DIGEST` itself) or write clown's state
(`$XDG_STATE_HOME/clown/session-names.jsonl`) or `profiles.toml` (it can
edit the pin or the profile). "An agent cannot approve its own profile
change" holds only when spinclass is the sole launcher, with a scrubbed
environment and a clown state dir and registry the agent cannot write
(an agent-scope unit).

## What "change at runtime" means

clown applies a profile exactly once, at launch. There is no mid-session
switch and this design does not add one. "Runtime change" means: the
profile a session was launched with, as recorded, differs from what that
name resolves to now, at the next launch/resume of that session; or the
operator asks to move a session to a different profile. Both are the same
event: a **profile transition** `(name, old digest) -> (name', new digest)`.

## What is approved

The approved object is the transition, bound by digest, never a bare name:

- session identity (spinclass session key / harness session id);
- old and new profile name and `DigestSHA256` (`profilelookup.Resolved`);
- a human-readable diff of the canonical fields (provider, backend, model,
  url, token_set, env keys, context servers/excluded, base). Env values
  and token are never shown (the canonical form carries them only as
  digests/booleans); the URL is shown, since it is in the digest verbatim
  (clown#292 tracks URL credentials).

clown provides a function returning the canonical-field diff of two
`Resolved` values, so the approver is shown what changed rather than two
opaque digests. Whether the approval text carries the full diff is limited
by piggy's operator-act text budget (spinclass reports ~15 rendered lines);
see decision 4.

## Who and what key approves

The human operator, by a piggy-signed grant: `operator-act@piggy`, slot 9C
(PIN-always, touch-always), namespace `piggy-escalation-v1`. This is the
mechanism spinclass#407 and FDR 0034 already point at. clown does not sign,
verify grants, or hold the allowed-signers file. Agents cannot produce a
grant (they cannot touch the card; `--repin-profile` stays CLI/TTY only
until the grant path replaces it), but clown cannot tell a grant-backed
`CLOWN_PROFILE_DIGEST` from one an agent set: see the guarantee under
Problem.

## How it is recorded

- **spinclass** records the authorizing grant on the session
  (`authorizing_grant`) and the new `profile_digest` (FDR 0034).
- **clown** records, per session, the profile **name and digest it was
  launched with** (new field in `internal/sessions`, clown#295 option 1),
  so `clown resume` works without spinclass. This is a *claim*, not an
  authorization: a mismatch is only ever resolved by an approval, never by
  clown updating its own record.
- clown appends a different pin for a pinned session only when the launch
  presents `CLOWN_PROFILE_DIGEST` matching the resolved profile (below);
  otherwise it records the same pin or none. That is a rule clown follows,
  not a protection: anyone who can write the state dir can append a record.
- A session with no pin yet (one launched before pinning existed, or
  without a profile) is pinned to whatever its next launch applies: trust
  on first use.

## What happens on resume

`clown resume <id>` (and key/URI forms):

1. Look up the session's recorded `(name, digest)`; absent -> today's
   behavior (no profile), documented.
2. Explicit `--profile` / `CLOWN_PROFILE`: accepted by resume; treated as a
   requested transition, not an override. If it resolves to the recorded
   digest it is a no-op; otherwise it needs approval.
3. Re-resolve the recorded name via `profilelookup.Resolve`. Same digest ->
   launch with it. Different digest, `SkewError`, `BaseError` or not found ->
   **fail closed**: refuse, print name, old/new digest and the field diff,
   and say how to approve. Never fall back to "no profile".
4. Approval evidence: when spinclass launches clown it passes the profile
   as the flag `--profile X` and the pinned expectation in the environment:
   `CLOWN_PROFILE_DIGEST` (bare lowercase hex) and `CLOWN_PROFILE_SESSION`
   (the session it approves). It does not set `CLOWN_PROFILE`, which stays
   a user's own shell convenience. clown refuses to start if the resolved
   digest differs from the pinned one or the session is not the one being
   resumed. spinclass is meant to be the only
   party that re-pins, after verifying a grant; clown just enforces
   equality, and anyone who controls clown's environment can supply the
   digest (see the guarantee under Problem). Standalone (no spinclass), a
   mismatch is refused outright (decision 2).

This keeps the check at two layers: spinclass refuses before launch
(authoritative, grant-backed), clown refuses at launch (defense in depth,
and the only layer when spinclass is absent).

## How clown#295 and spinclass#407 fit

- #295 supplies the missing half: clown records and restores the profile on
  resume and enforces a pinned digest. It does not need the grant machinery;
  it only needs the `(name, digest)` pin and the diff function.
- #407 supplies the approval half: the digest-mismatch refusal becomes an
  escalation request (session, old/new digest, diff) approved by a card tap;
  spinclass verifies and records. After approval spinclass re-pins and
  launches clown with the new pin.
- Protocol bumps: the canonical form embeds `protocol`, so bumping
  `ProtocolVersion` changes every digest (**inference**, not tested) and
  would force an approval for every profiled session. Announce bumps to
  spinclass first (already the rule in AGENTS.md); decision 5 covers the
  approval.

## Non-goals

No mid-session profile switching. No change to digest canonicalization. No
clown-side signature verification or grant store. Not Hats (FDR 0005):
selecting plugins by profile is orthogonal.

## Decisions (operator, 2026-10-23, relayed by the coordinator)

1. spinclass records the card approval; clown only enforces the pinned
   digest and keeps no signed record.
2. Plain `clown resume` without spinclass refuses on any mismatch. There
   is no local confirmation path.
3. `clown resume --profile X` is a requested transition and needs approval.
4. The signed text shows both digests plus a field diff, within piggy's
   ~15-line budget.
5. One approval per profile name and old->new digest covers every session
   pinned to the old digest after an upgrade.
6. v1: every change is an escalation; a narrowing exemption comes later.
7. spinclass owns the markl purpose `clown-profile-digest-v1`.
8. The pin env var is `CLOWN_PROFILE_DIGEST`; clown fails closed when
   `CLOWN_PROFILE` is set without it.

   **Amended (recorded at the operator's request, 2026-10-23):** a resume with
   `CLOWN_PROFILE` set and no `CLOWN_PROFILE_DIGEST` is allowed only when
   it names the session's pin exactly (name and digest; never an
   `unpinnable` pin). Why: allowing the unchanged pin changes nothing, so
   no approval is skipped, and refusing it would break a resume that only
   restates the pin. Since a launcher approves with `--profile` plus the
   digest and clown removes `CLOWN_PROFILE` from every child's environment,
   this case now arises only from a user's own shell export.

Later answers (operator, 2026-10-23, relayed by the coordinator):

9. Resume forms clown cannot identify (`-c`/`--continue`, a bare
   `--resume`/`-r`, `--from-pr`) are refused when a profile is applied,
   pointing to `clown resume <id>` (slice 3a). Without a profile they pass
   through.
10. Approval is bound to a session: `CLOWN_PROFILE_SESSION`, when set, must
    equal the gated session id, and the approval covers only that
    session's records. Without it an approval stays unbound (current
    behavior, no warning) until spinclass sends it.
11. Trust on first use is accepted: a session with no pin is pinned, in the
    session-names sidecar, to the profile its first profiled launch
    applies.
12. Sidecar write shape is the condition for the narrow corrupt-line rule:
    every record is one whole line written by a single `write(2)` on an
    `O_APPEND` descriptor, and a truncated last line is reported as a
    warning naming the file, without failing. This is an atomic append (no
    interleaving between writers), not crash durability: there is no
    fsync, so a crash can lose a record or leave part of one, and a failed
    or short write (ENOSPC) can leave a fragment at the end. A record that
    follows a fragment is prefixed with a newline only if the separate
    end-of-file check (a read before the write, so it can race another
    writer) saw the fragment.

Supersedes the earlier text in "What happens on resume" step 4: standalone
resume has no local confirmation (decision 2), and the env names are final.

## Implementation slices

1. **Done.** clown#295: record `(name, digest)` per session; `clown resume`
   restores it, re-resolves, and fails closed on a mismatch (tests first).
   A profile `profilelookup` cannot vouch for is recorded with the digest
   `unpinnable`, which never matches.
2. **Done.** `CLOWN_PROFILE_DIGEST` enforcement at launch (decision 8) and
   `--profile` on resume as a requested transition (decision 3).
   `runWithFlags` checks a session's existing pin before appending a
   different one (`checkLaunchProfile`, `cmd/clown/profilegate.go`):
   - `CLOWN_PROFILE_DIGEST` set: the applied profile must resolve to that
     digest, on a fresh launch or a resume; a match is the approval. On a
     resume the new pin is recorded by the time the provider runs (before
     any `[attach]` re-exec; skipped under `--print-launch-plan`), and a
     failure to record it refuses the launch.
   - A launcher approves with the flag `--profile X` plus
     `CLOWN_PROFILE_DIGEST` and `CLOWN_PROFILE_SESSION`; a test pins this
     through `clown resume <id> --profile X` and
     `clown --profile X -- --resume <id>`.
   - `CLOWN_PROFILE_SESSION` set: it must equal the gated session id, and
     the approval covers only that session's records (decision 10).
   - Once read, `CLOWN_PROFILE`, `CLOWN_PROFILE_DIGEST` and
     `CLOWN_PROFILE_SESSION` are removed from clown's environment, so the
     provider, the `[attach]` re-exec child and nested clowns inherit
     neither a profile request nor an approval. Nothing downstream reads
     `CLOWN_PROFILE`: the re-exec child gets `--profile` in its argv, and
     the agent-scope unit's environment allow-list never carried it.
   - The gated session is the claude `--resume`/`-r <id>` (every `clown
     resume` form passes one), or a `--session-id <id>` that already has a
     pin, including one injected from `CLOWN_SESSION_ID`/`CLAUDE_SESSION_ID`
     (the check runs after `decideClaudeSession`).
   - Resume of a pinned session: the applied profile must equal the pin
     (name and digest); otherwise refused, including resuming with no
     profile. `clown resume --profile X` naming the pin is a no-op.
   - Resume with `CLOWN_PROFILE` set and no `CLOWN_PROFILE_DIGEST`: refused,
     unless it names the session's pin exactly (decision 8 as amended; only
     a user's own shell export can reach this).
   - Resume of an unpinned session: an explicit `--profile`/`CLOWN_PROFILE`
     needs approval; a clownfile pin or build default is allowed and pins
     the session (trust on first use, decision 11). That pin is also recorded early, by
     the time the provider runs, with the same refuse-on-failure rule
     (except for an unpinnable profile, whose pin would never match). The
     inner clown of an `[attach]` re-exec receives the resolved name as
     `--profile` even when it was defaulted, so it does not count its
     `--profile` as a request (the outer already ran the check); rules that
     compare against a pin still apply to it.
   - The write side never relies on the gate alone: every launch's record
     carries the launch pin only if the id has no pin, the same pin, or the
     launch was approved; otherwise (or if the pin cannot be read) it is
     a name-only record. This covers ids the gate does not check, such as
     an operator `identity.Key` that is not the claude id. An approved
     launch skips that comparison only for the session
     `CLOWN_PROFILE_SESSION` names; an unbound approval (no
     `CLOWN_PROFILE_SESSION`) still skips it for both `identity.Key` and the
     claude id. A failed ordinary write is reported on stderr and does not
     stop the launch.
   - Availability coupling: every fresh claude launch carries a
     `--session-id`, which the check looks up, so a session-names sidecar
     that exists but cannot be read refuses every resume and every fresh
     claude launch (fail closed).

   Provisional: an empty `CLOWN_PROFILE_DIGEST` counts as unset; the
   approved or first-use pin is written before the provider starts, so a
   launch that fails after the check still re-pins; a non-claude provider
   has no session continuity check; the `[attach]` inner clown is
   recognised by `--clown-attach-id`, which anyone launching clown can
   pass.

   - Sidecar write shape (decision 12): `sessions.RecordSession` writes
     each record as one line in a single `write(2)` on an `O_APPEND`
     descriptor (atomic append, no fsync); when its prior end-of-file read
     saw a fragment, it prefixes the newline in the same write. A truncated
     last line (`sessions.TruncatedTail`) is warned about once per launch on
     stderr, naming the file.
3a. **Done.** Resume forms clown cannot identify (decision 9): with a
   profile applied, claude `-c`/`--continue`, `--from-pr`, and a
   `--resume`/`-r` that does not name a session are refused with a message
   pointing to `clown resume <id>` (`unseenResumeForm`). A `--resume`/`-r`
   names a session only when its value is a UUID (claude's session ids
   are); no value, an empty value, a search term or a following flag opens
   claude's picker. One reader (`resumeArg`) backs `claudeResumeID`,
   `unseenResumeForm` and `isBareResume`, and `decideClaudeSession`, the
   `[attach]` mode and the group-id command (`claudeSessionRef`) use it, so
   none of them adopts a search term or a following flag as a session id;
   an explicit `--session-id` is still adopted next to a bare `--resume`.
   When the profile was only defaulted (clownfile pin or build default),
   the refusal adds that `--provider claude` turns defaulting off
   (`resolveProfileName`), so the picker still works without a profile.
3. The canonical-field diff function for the approval text (decision 4).

## Notes

- #295 is the resume issue; #293 (wake truncation) was a wrong number.
- spinclass FDR 0034's Tuning Levers table still says "warn" on digest
  mismatch, contradicting its decision 12 (refuse); relayed to the
  spinclass session.
