# clown#342: relaunch a session into or out of dontAsk (design and test plan)

Status: design only, nothing implemented. Written 2026-11-05 under a memory
HOLD (no build, eval or test lane run); every claim is **R** (read in code at
0e3d121) or **I** (inferred) unless marked otherwise. Companion: FDR 0023
(the posture), spinclass FDR 0037 (restart), FDR 0032 (rights).

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
- A restart relaunches the same frontend by exec of the clown on PATH with
  `--resume <principal>` (`restart_relaunch.go`). Its environment is the
  launch environment minus `restartEnvDropped`, which does not name the
  marker, so the relaunch keeps whatever posture the launch had
  (`restartEnv`). Its argv is `restartArgv`: the resolved flags, with the
  [attach] inner's carrier `--clown-unattended` added from the OLD
  frontend's `unattendedValue()`.
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

### The object: a posture on the restart, decided by spinclass

A restart carries a **posture** beside `when`: `unattended` (the relaunch
runs with `CLOWN_UNATTENDED=1`), `attended` (without it), or absent (keep the
launch's, today's behaviour). It travels exactly as `when` does:

1. `clown restart --session K [--posture unattended|attended]` is the
   hand-off form spinclass runs. The control message gains `posture`. The
   frontend treats it as a request to be preflighted, never as authority.
2. `spinclass restart-preflight`'s verdict gains `"posture"`. **Only the
   verdict's value is acted on.** A control message asking `attended` while
   the verdict says nothing relaunches with the posture unchanged. This is
   what keeps tension 2 out of clown: the control socket is reachable by any
   same-uid process, including the session's own Bash tool in an unconfined
   session, so a widening (out of dontAsk) must not be decidable there.
   spinclass decides who may ask for which posture with its rights model
   (FDR 0032: the `restart` right, or a new right; whether
   `restart-this-session` may narrow itself). That is spinclass's design,
   handed off below, not decided here.
3. `relaunchPlan` gains `posture`. `restartEnv` applies it: `unattended`
   removes any existing marker and appends `CLOWN_UNATTENDED=1`; `attended`
   removes it; absent leaves the environment as today. `restartArgv` takes
   the plan's posture for the [attach] inner's carrier instead of the old
   frontend's value, and for `attended` drops a forwarded
   `--permission-mode dontAsk` / `--inherit-permission-mode dontAsk` (else
   claude's own flag keeps dontAsk and the relaunch is attended in name
   only). Confined sessions are out of scope (issue item 4): their posture
   is spinclass's splice plus the confinement profile, and the unit's pass
   list never carried the marker (FDR 0023).
4. Request-time gate, as for the profile (clown#340): a posture
   `unattended` is checked with `unattendedLaunchError` against the relaunch
   flags at request time and before the stop, so a session that cannot be
   put in dontAsk (`--naked`, codex, a forwarded `--settings`) is refused
   while it still runs, with the same text the launch would print. A posture
   `attended` has no such check.
5. Visibility (issue items 3 and 5). The marker stays unrecorded in the
   sidecar (FDR 0023's rule stands), but the hung-session status file gains
   `unattended` (bool, frontend-written, additive; `version` stays 1), so a
   holder and `sc list` can see a session's posture and its change after the
   relaunch. The relaunched session's system-prompt notice
   (`restartedNotice`) names the posture change in the reason, so the model
   knows its prompts now deny. What the session "would lose" is not
   enumerable by clown: every call claude would have prompted for is denied
   instead, and clown does not see claude's allow lists; the hand-off's
   stdout prints one fixed line naming the two clown-side rules it does know
   (TaskStop on the wake and troupe monitors is refused; spinclass's
   always-ask tools such as an unconfined spawn or a forced close are
   denied). spinclass can print more, since it owns the always-ask list.
6. A running merge or check job, the multiplexer attach and the deferred
   retry are unchanged: the posture rides the same restart, so every refusal
   and bound that applies to a restart applies to it.

### Why not a new relaunch verb

A second relaunch path (`clown relaunch --posture ...`) would duplicate the
stop, countdown, peek, exec and status-file machinery and sit outside
spinclass's restart record. The restart already is "stop and relaunch the
same principal"; the posture is one more attribute of it.

### Compatibility

An older spinclass that sends no `posture` in its verdict, or an older
`clown restart` without `--posture`, means "keep": today's behaviour. An
older clown frontend ignores the verdict's unknown key (encoding/json). A
verdict with an unknown posture value is an error, which does not restart
(as an unknown verdict is).

## Hand-offs

- spinclass: `restart-child-session --posture`, which right authorizes a
  widening to `attended`, whether `restart-this-session` may narrow to
  `unattended`, the verdict field, and the record on the session. Scoped
  question sent with this note.
- FDR 0023: the "restart keeps it" sentence under "Env only, never
  recorded" becomes "keeps it unless the verdict carries a posture"; the
  status-file table gains the field.

## Test plan (unit, `cmd/clown`; no live run under the HOLD)

1. `parsePreflightVerdict`: `posture` absent, `unattended`, `attended`
   accepted; any other value is an error (new rows in `restart_idle_test.go`).
2. `restartEnv` (extend `TestRestartEnv`): with the launch env carrying the
   marker and without it, each posture yields exactly zero or one
   `CLOWN_UNATTENDED=1`, never two, and `attended` yields none; absent keeps
   the launch env's.
3. `restartArgv` (extend `TestRestartArgvResumesThePrincipal`): with an
   attach id, the carrier is present iff the plan's posture is `unattended`
   (or absent with the old value 1); `attended` drops `--permission-mode
   dontAsk` in both spellings and `--inherit-permission-mode dontAsk`, and
   keeps every other forwarded arg and the operands after `--`.
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
   the FDR table test (if any) and the JSON golden gain the field.
8. Child-process test, as FDR 0023's "child process started with the
   marker": the relaunch's exec receives the flipped environment for each
   posture.
9. Live checks after the HOLD (B5 style, PTY and built clown): launch
   unattended, request a restart with `attended`, confirm the relaunched
   claude's `--settings` has no `defaultMode` and a disallowed call prompts;
   and the reverse, confirming the denial with no dialog.
