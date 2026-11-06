# clown#334: a user-only override for an unpinnable or changed profile on resume (design and test plan)

Status: **ruled 2026-11-05 (operator, via the coordinator): option A now,
C when the card path exists.** Implemented on rare-walnut as FDR 0022 slice
3e (`cmd/clown/resume.go`, `profilegate.go`, `main.go`,
`internal/alfa/sessions`; tests in `resume_override_test.go` and
`sessions/pinsource_test.go`), written under the memory HOLD of 2026-11-05/06
and not yet built, run or merged. The design below is as written before the
ruling; claims are **R** (read in code at 0e3d121) or **I** (inferred). Companion: FDR 0022
(decisions 2 and 8), FDR 0023, spinclass FDR 0032 (D11 card path), clown#337
(diagnostics, done), clown#340 (provenance, done).

## The problem

`clown resume` refuses a session whose pin is `unpinnable` or whose profile
now resolves to another digest (`resume.go` `resumeProfile`,
`profilegate.go` `checkLaunchProfile`). Since clown#337 and #340 the refusal
says what is wrong, which build pinned, and names the digest and the
approval form (`CLOWN_PROFILE_DIGEST` with `CLOWN_PROFILE_SESSION` and
`--profile`), and `clown profile digest <name>` prints the digest. What is
left is the act itself: a person at a terminal must hand-type two
environment variables, and nothing offers the approval where the refusal
is printed.

## What stands in the way (R)

- **FDR 0022 decision 2 (operator, 2026-10-23):** "Plain `clown resume`
  without spinclass refuses on any mismatch. There is no local confirmation
  path." The issue's proposal is a local confirmation path. Implementing it
  amends an operator decision, so it needs the operator.
- **Nothing tells a person's tty from an agent's pty.** `isInteractiveTerminal`
  checks that stdin and stdout are terminals; an agent's Bash tool can run
  `clown resume` under `script` or a pty and answer the prompt. FDR 0022
  already states that clown's check guards against mistakes and drift, not
  against an agent with an environment of its choosing: such an agent can set
  `CLOWN_PROFILE_DIGEST` itself today. A huh confirm neither widens nor
  narrows that: it is one more way to do what the environment variable
  already allows, but a more discoverable one.
- spinclass FDR 0032 D11 (per spinclass's reading, quoted in the issue): the
  only user-only step an agent cannot satisfy is a PIN and touch on the
  operator's card. Not built. The confirm cannot claim to be an authority
  boundary.

## Design (if ruled in)

1. **Where.** `launchResume` (`resume.go`), for the two profile refusals
   only: `resumeProfile` returns a typed error (`profileRefusal{pin, current
   *Resolved, text}`) instead of a plain one. Any other failure (an
   unreadable sidecar, an unknown profile, a `SkewError` that still holds)
   is printed and refused as today: without a current digest there is
   nothing to pin to, so the unpinnable case is offered a confirm only when
   the registry now vouches (`unpinnableReason`'s second branch).
2. **When offered.** Only when every one holds: stdin and stdout are
   terminals; `CLOWN_UNATTENDED` is unset (FDR 0023: an unattended session
   must never wait at a dialog, and must not approve anything); no
   `CLOWN_PROFILE_DIGEST` in the environment (a launcher's approval wins and
   the gate decides); and `--yes` was not passed. `-y` keeps today's refusal:
   a scripted resume must not approve a transition by accident, and the
   override has no flag form on purpose.
3. **What it shows.** The refusal text as today (name, old and new digest,
   `describeProfileChange`'s diff or `unpinnableReason`, the provenance
   line), then a huh confirm with the default **No**: "Re-pin session <id> to
   profile <name> at digest <current>? This records that you approved it at
   this terminal." Enter refuses.
4. **What it does on Yes.** The launch proceeds as an approved one bound to
   this session: `launchResume` passes the approval into the gate as
   `launcherProfileEnv{digest: current, session: id}` with a new
   `source: userConfirm` instead of exporting the variables (so nothing can
   inherit them and `consumeLauncherProfileEnv` stays the one reader of the
   environment). `checkLaunchProfile` is unchanged: a digest match is an
   approval; `pinEarly` records the pin before the provider runs.
5. **Distinguishable record (issue item 2).** `sessions.NameRecord` gains
   `profile_pin_source` (`omitempty`): `launcher` when the approval came from
   the environment, `user-confirm` from the prompt, absent for a
   trust-on-first-use or unchanged pin. `PinRecord` exposes it; older
   records read as absent. This is clown's sidecar, not the profile's
   canonical form: no `ProtocolVersion` change, no digest change.
6. **Labelled a speed bump.** The code comment, `clown-resume(1)` and an
   FDR 0022 amendment say: this confirm is a usability step for a person at
   a terminal, satisfiable by anything that can drive a pty, and not an
   authority boundary; the boundary is spinclass's grant (today) and FDR
   0032 D11's card path (when built), which this prompt will then call
   instead of deciding. Decision 2's text is amended to say so, and
   `describePinProvenance` can name a `user-confirm` pin in a later refusal.
7. **Not changed.** `CLOWN_PROFILE_DIGEST` and spinclass's approval path;
   `clown resume --profile X` (decision 3) stays a requested transition and
   gets the same confirm only when the operator rules so (it is the same
   event); a direct `clown -- --resume <id>` is not offered the prompt
   (`runWithFlags` has no resume UI; the refusal already points to
   `clown resume <id>`).

## The ruling needed (exact question for the operator)

FDR 0022 decision 2 says a plain `clown resume` has no local confirmation
path. clown#334 asks for one. clown cannot tell a person's terminal from a
pty an unconfined agent drives, so any confirm it offers can be answered by
such an agent (which can already set `CLOWN_PROFILE_DIGEST` itself). Which
of these:

- **A.** Amend decision 2: `clown resume` at a terminal, with
  `CLOWN_UNATTENDED` unset and no `-y`, may offer the confirm above and
  re-pin, recorded as `user-confirm`, documented as a speed bump and not an
  authority boundary (the issue's own suggestion).
- **B.** As A, but only for the `unpinnable` pin (the registry was never
  stamped at launch, so no approved digest ever existed), keeping a changed
  digest on the launcher-only path.
- **C.** Keep decision 2: no local confirm until FDR 0032 D11's card path
  exists; close #334 as "consume D11 when built", with #337's diagnostics as
  the resolution of the usability complaint.
- **D.** As A, with one more refusal: inside a spinclass session
  (`SPINCLASS_SESSION_ID` set) or an agent unit the confirm is not offered,
  so only a bare terminal outside any session sees it. Weak (the variable is
  unsettable), but it keeps the prompt out of every worker's reach by
  default.

## Test plan (unit, `cmd/clown` and `internal/alfa/sessions`; nothing run under the HOLD)

In `resume_profile_test.go`, with the confirm injected as a function and the
tty check as a variable:

1. Changed digest, tty, attended, no `-y`: the refusal text is printed, the
   confirm is shown with the current digest; **Yes** launches with
   `profileFromRecord`, the sidecar's last pin for the id is `(name,
   current)` with `profile_pin_source = user-confirm`, writer and protocol
   of this build; **No** exits 1 and the sidecar is unchanged.
2. `-y`: refused as today, no confirm called.
3. `CLOWN_UNATTENDED=1`: refused, no confirm; the text names the marker.
4. Not a tty (either fd): refused as today, no confirm.
5. `CLOWN_PROFILE_DIGEST` set: no confirm; the gate decides (existing
   approval tests unchanged).
6. Unpinnable pin, registry still not vouching: no confirm, the text is
   `unpinnableReason`'s first branch.
7. Unpinnable pin, registry now vouching: confirm offered at the digest
   `unpinnableReason` names; Yes pins it.
8. `clown resume --profile X` (decision 3): only if ruled in; otherwise a
   test pins that it stays refused without a confirm.
9. `profilegate_test.go`: an approval with `source: userConfirm` is bound
   to the resumed session (`approvedSession == id`), never unbound.
10. `sessions`: `PinRecordOf` returns the source; a record without the key
    reads as absent; a line with the key is still one `O_APPEND` write and
    under `MaxProfileFormBytes`.
11. `describePinProvenance` names a user-confirmed pin when present (text
    pinned by a golden).
12. Live check after the HOLD (PTY, built clown): edit a profile's env
    value, `clown resume <id>` shows the diff and the confirm; Enter refuses;
    `y` resumes and `clown resume <id>` a second time needs no confirm.
