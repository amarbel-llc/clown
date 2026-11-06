# clown#342: relaunch a session into or out of dontAsk (design and test plan)

Status: the clown side is implemented on rare-walnut (`cmd/clown/restart_posture.go`,
the restart files, `internal/alfa/sessionstatus`, FDR 0023; tests in
`restart_posture_test.go`), written under the memory HOLD of 2026-11-05/06
and not yet built, run or merged; the spinclass side waits for the operator's
go. Design written 2026-11-05 under the same HOLD; every claim is **R** (read
in code at 0e3d121) or **I** (inferred) unless marked otherwise. Companion: FDR 0023
(the posture), spinclass FDR 0037 (restart), FDR 0032 (rights), FDR 0035
(confinement). spinclass's positions below are from spinclass/light-cedar
(2026-11-05), proposals, not operator rulings.

## The problem

`dontAsk` is chosen at launch only (FDR 0023; operator, 2026-11-04). A
coordinator launched in a prompting mode stalled about 16 hours on one
dialog while its dontAsk workers kept working. The operator wants to put an
existing unconfined session into dontAsk before leaving and take it out on
return, keeping its identity and conversation.

## What the code gives us today (R)

- The posture is the environment variable `CLOWN_UNATTENDED=1`, read by the
  frontend at every launch and never recorded (`cmd/clown/unattended.go`,
  FDR 0023 "Env only, never recorded"). It becomes
  `permissions.defaultMode = "dontAsk"` in the `--settings` JSON and
  `CLOWN_UNATTENDED=1` in claude's settings `env`, which is how
  `clown-hook-allow`'s TaskStop rule learns it (`claudesettings.go:157-171`).
- spinclass's half of the posture (R per spinclass, read in its spawn code):
  for an unconfined worker it splices `--permission-mode dontAsk` into the
  spawn argv AND pins `CLOWN_UNATTENDED=1` into the session's pinned resume
  entry (`session.State.Entry.Env`), which `sc resume` relaunches from.
- A clown restart relaunches the same frontend by exec of the clown on PATH
  with `--resume <principal>` (`restart_relaunch.go`). Its environment is
  **clown's own launch environment** (`launchEnviron`) minus
  `restartEnvDropped`, which does not name the marker, and its argv is
  `restartArgv` over the frontend's own resolved flags, with the [attach]
  inner's carrier `--clown-unattended` taken from the OLD frontend's
  `unattendedValue()`. The relaunch does **not** re-read spinclass's pinned
  entry: it carries what the entry said at launch. So a posture change
  recorded only on spinclass's entry would reach `sc resume`, not a clown
  restart; one applied only in clown's relaunch would leave the entry stale
  for the next `sc resume`. Both halves are needed.
- The frontend never decides a restart. `clown restart --session K --when
  idle|now --reason R` is the hand-off spinclass runs; it only queues. At the
  idle transition the frontend asks `spinclass restart-preflight`, whose one
  JSON line `{verdict, reason, when}` is authoritative: "the verdict decides,
  not the request" (`restart_relaunch.go` on `cancelOnTurn`; the request's
  reason is not even used, since the control socket is any-same-uid).
- A relaunch is refused before the stop when its profile gate would refuse
  (`restart_gate.go`, clown#340), at request time and again before the stop.
- `unattendedLaunchError` refuses a launch the marker cannot put in dontAsk:
  a non-claude provider, `--naked`, forwarded `--permission-mode <m>` other
  than dontAsk, `--settings`, `--dangerously-skip-permissions`.

## Design

### The object: a posture on the restart, decided and recorded by spinclass

A restart carries a **posture** beside `when`: `unattended` (the relaunch
runs with `CLOWN_UNATTENDED=1` and `--permission-mode dontAsk`), `attended`
(with neither), or absent (keep, today's behaviour). Division of labour:

- **spinclass is the source.** The request (`sc restart-child-session
  --posture`, `restart-this-session --posture`) is recorded on
  `RestartRequest`; on a `restart` verdict the preflight rewrites the pinned
  entry (argv splice or unsplice, `Entry.Env` marker set or cleared) in the
  same locked `session.Update` that stamps `RestartingAt`, so a crash cannot
  leave them apart; `cancel-restart` drops the posture with the request.
  The marker in `Entry.Env` is the record; `sc list` can derive a column.
- **The verdict mirrors it.** `restart-preflight`'s line gains
  `"posture": "unattended"|"attended"` (absent = keep), also on `--peek` and
  `--rearm`.
- **clown applies the mirror to the relaunch**, because the relaunch runs
  from clown's own launch argv and env (above). `relaunchPlan` gains
  `posture`; `restartEnv` for `unattended` removes any existing marker and
  appends `CLOWN_UNATTENDED=1`, for `attended` removes it, for absent leaves
  the environment as today; `restartArgv` takes the plan's posture for the
  [attach] inner's carrier instead of the old frontend's value, for
  `unattended` ensures exactly one `--permission-mode dontAsk` (mirroring
  spinclass's splice, so argv and settings agree), and for `attended` drops
  a forwarded `--permission-mode dontAsk` / `--inherit-permission-mode
  dontAsk` (else claude's own flag keeps dontAsk and the relaunch is
  attended in name only). Which mode "attended" is belongs to spinclass's
  spawn rule (auto unless disable-auto-mode); on clown's side attended only
  means no marker and no dontAsk flag.

The control message (`clown restart --posture ...`) carries the posture as
the hand-off's argument, so the frontend can run the request-time gate
below, but it is never acted on by itself: the control socket is reachable
by any same-uid process, including the session's own Bash tool in an
unconfined session.

### Who may ask (spinclass's positions, FDR 0032 / 0035 / 0037)

- **Narrowing is free.** `restart-this-session --posture unattended` needs
  no right (D6, the operator's hand-off gates it) and is auto-allowed; a
  holder with the `restart` right may narrow a child. A session that
  narrows itself cannot widen itself back.
- **Widening is always-ask.** `--posture attended` needs the `restart`
  right AND the always-ask prompt (`perms.AlwaysAsk`, the shape of `force`
  in FDR 0037 D8, which no allow-list approves silently); at a terminal the
  caller is the operator, so nothing prompts. No new `posture` right in v1:
  a right would let a holder delegate the widening without a human. FDR
  0032 D13 gets a one-line note that `restart` does not carry widening.
- **Confined sessions are out of scope** (issue item 4): confined is
  dontAsk by construction (FDR 0035 `applyUnattendedMode`) and the posture
  is the confinement profile's; a posture request naming a confined session
  is refused with the reason, not silently kept.
- **Spawn refusals apply to the splice.** A pinned entry with no `--`
  separator or a non-claude provider cannot take `--permission-mode
  dontAsk`: the request is refused with nothing recorded.

### clown-side gates and visibility

1. Request-time gate, as for the profile (clown#340): a posture
   `unattended` is checked with `unattendedLaunchError` against the relaunch
   flags at request time and before the stop, so a session that cannot be
   put in dontAsk (`--naked`, codex, a forwarded `--settings`) is refused
   while it still runs, with the launch's own text. `attended` is not gated
   this way.
2. The hung-session status file gains `unattended` (bool, frontend-written,
   additive; `version` stays 1), so a holder sees the posture after the
   relaunch (FDR 0023 table). The sidecar still records nothing (FDR 0023's
   rule stands; spinclass's entry is the record).
3. The relaunched session's notice (`restartedNotice`) names the posture
   change in the reason, so the model knows its prompts now deny (or ask).
   What the session "would lose" (issue item 3) is not enumerable by clown:
   every call claude would have prompted for is denied instead, and clown
   does not see claude's allow lists. The hand-off prints one fixed line
   naming the clown-side rule it knows (TaskStop on the wake and troupe
   monitors is refused); spinclass owns the always-ask list and can print
   more.
4. A running merge or check job, the multiplexer attach and the deferred
   retry are unchanged: the posture rides the same restart, so every refusal
   and bound that applies to a restart applies to it.

### Limitation (spinclass's, stated plainly)

The same hole FDR 0037 names for a forged `when: now`: the request lives in
state an unconfined agent can write, and the plain preflight re-checks
rights only at request time. A hand-edited `posture: attended` would widen
on the next idle restart. The always-ask guards the tool, not the state
file; the mitigation is that an attended relaunch has a human at it. **Open
question for the operator:** should the preflight honour a widening only
when a trusted-layer sweatfile key allows posture widening at all?

### Why not a new relaunch verb

A second relaunch path would duplicate the stop, countdown, peek, exec and
status-file machinery and sit outside spinclass's restart record. The
restart already is "stop and relaunch the same principal"; the posture is
one more attribute of it.

### Compatibility

An older spinclass that sends no `posture`, or an older `clown restart`
without `--posture`, means keep. An older clown frontend ignores the
verdict's unknown key (encoding/json). A verdict with an unknown posture
value is an error, which does not restart (as an unknown verdict is).

## Records

- spinclass: an FDR 0037 amendment (Interface: `posture` on the two tools
  and the verdict; Decisions: widening is operator-gated) and the D13 note;
  spinclass/light-cedar offers the spinclass-side plan doc when asked.
- clown: FDR 0023's "restart keeps it" sentence becomes "keeps it unless the
  verdict carries a posture"; the status-file table gains the field.

## Test plan (unit, `cmd/clown`; no live run under the HOLD)

1. `parsePreflightVerdict`: `posture` absent, `unattended`, `attended`
   accepted; any other value is an error (new rows in `restart_idle_test.go`).
2. `restartEnv` (extend `TestRestartEnv`): with the launch env carrying the
   marker and without it, each posture yields exactly zero or one
   `CLOWN_UNATTENDED=1`, never two, and `attended` yields none; absent keeps
   the launch env's.
3. `restartArgv` (extend `TestRestartArgvResumesThePrincipal`): with an
   attach id, the carrier is present iff the plan's posture is `unattended`
   (or absent with the old value 1); `unattended` yields exactly one
   `--permission-mode dontAsk` whether or not the forwarded args had one;
   `attended` drops it in both spellings and `--inherit-permission-mode
   dontAsk`, and keeps every other forwarded arg and the operands after `--`.
4. Control handler (`restart_test.go`): a message with `posture` is queued
   like one without; the plan's posture comes from the verdict, over a table
   of request posture x verdict posture (absent/unattended/attended); an
   `attended` request with an absent verdict posture relaunches unchanged.
5. Request-time gate: `--posture unattended` on a relaunch whose flags are
   `--naked`, provider codex, or carry `--settings` is refused with
   `unattendedLaunchError`'s text and nothing queued; on a plain claude
   launch it is queued. `--posture attended` is never gated this way.
6. `runRestartVerb`: `--posture` parsed, an invalid value exits 2 with usage.
7. Status file: the writer records `unattended` from `unattendedValue()`;
   the JSON golden gains the field.
8. Child-process test, as FDR 0023's "child process started with the
   marker": the relaunch's exec receives the flipped environment and argv
   for each posture.
9. Live checks after the HOLD (B5 style, PTY and built clown, with a
   spinclass that sends the posture): launch unattended, request a restart
   with `attended`, confirm the relaunched claude's `--settings` has no
   `defaultMode`, its argv no dontAsk, a disallowed call prompts, and `sc
   resume` of the same session afterwards is attended too; and the reverse,
   confirming the denial with no dialog.
