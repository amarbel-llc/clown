---
status: exploring
date: 2026-10-23
issue: clown#295
related: FDR 0005 (hats), FDR 0006 (single-entrypoint matrix), spinclass FDR 0032 / 0034, spinclass#407, piggy RFC 0012 (operator-act@piggy, namespace piggy-escalation-v1)
promotion-criteria: >
  proposed once the operator has answered the open questions below;
  experimental once clown#295 lands behind the decisions made here.
---

# Profile changes at runtime require user approval

> **Design record only.** Nothing here is implemented. Claims not read in
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
silently, and an agent can never approve its own profile change.*

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
see question 4.

## Who and what key approves

The human operator, by a piggy-signed grant: `operator-act@piggy`, slot 9C
(PIN-always, touch-always), namespace `piggy-escalation-v1`. This is the
mechanism spinclass#407 and FDR 0034 already point at. clown does not sign,
verify grants, or hold the allowed-signers file. Agents hold no approval
authority (they cannot touch the card; `--repin-profile` stays CLI/TTY
only until the grant path replaces it).

## How it is recorded

- **spinclass** records the authorizing grant on the session
  (`authorizing_grant`) and the new `profile_digest` (FDR 0034).
- **clown** records, per session, the profile **name and digest it was
  launched with** (new field in `internal/sessions`, clown#295 option 1),
  so `clown resume` works without spinclass. This is a *claim*, not an
  authorization: a mismatch is only ever resolved by an approval, never by
  clown updating its own record.
- The clown record is updated to the new digest only when the launch
  presents evidence of approval (below) or the profile is unchanged.

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
4. Approval evidence: when spinclass launches clown it passes the pinned
   expectation (`CLOWN_PROFILE`, `CLOWN_PROFILE_DIGEST`; env, clown#295
   option 2, names provisional). clown refuses to start if the resolved
   digest differs from the pinned one. spinclass is the only party that can
   re-pin, after verifying a grant; clown just enforces equality. Standalone
   (no spinclass), approval is a local interactive confirmation showing the
   diff, which updates clown's own record; no signature (question 2).

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
  spinclass first (already the rule in AGENTS.md) and decide question 5.

## Non-goals

No mid-session profile switching. No change to digest canonicalization. No
clown-side signature verification or grant store. Not Hats (FDR 0005):
selecting plugins by profile is orthogonal.

## Questions for the operator

1. Is "profile transition approved by card, recorded by spinclass; clown
   only enforces a pinned digest" the right split, or should clown also
   keep its own signed record?
2. Without spinclass (plain `clown resume`), is a local interactive
   diff-confirmation enough, or must standalone resume refuse on any
   mismatch?
3. Should explicit `--profile` on `clown resume` be allowed as a requested
   transition (needs approval), or rejected outright?
4. What must the signed text carry: both digests only, or a field diff
   (budget ~15 rendered lines)?
5. Should a clown-upgrade-induced digest change (builtin or protocol bump)
   need the same per-session approval, or a once-per-upgrade operator
   approval covering all sessions?
6. Is a transition to a strictly *narrower* profile (fewer servers, same
   provider) a narrowing that needs no card, or always an escalation?
7. Who owns the markl purpose `clown-profile-digest-v1` (clown or
   spinclass)?
8. Env var names for the pin (`CLOWN_PROFILE_DIGEST`) and whether clown
   fails closed when `CLOWN_PROFILE` is set but the digest is absent.

## Notes

- clown#293 is about `job_message` wake-preview truncation, not profiles;
  the brief's reference to it may be a wrong number.
- spinclass FDR 0034's Tuning Levers table still says "warn" on digest
  mismatch, contradicting its decision 12 (refuse); spinclass's session
  owns that fix.
