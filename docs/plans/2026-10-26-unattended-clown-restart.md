# Unattended clown: restart

Status page, 2026-10-26, clown master da76a79 (+ this page). Goal: restart
that works for days with nobody at the keyboard. Docs only; no code is
proposed or changed here.

Labels: **V** a recorded live run (none exists for restart), **R** read in
code or tested only against fakes (named), **I** inferred, **not found**.
Sources: `man/man1/clown-restart.1`,
`docs/plans/2026-10-24-idle-poke-restart-verb.md` (limits and the `--when now`
sections), `cmd/clown/restart*.go`, `selfclose.go`, spinclass FDR 0037 and its
first-live-restart runbook (read on disk).

## 1. What is proven, faked, unverified

Headline: **nothing on the restart path is V.** Every doc that discusses a
live run says it has not happened (clown plan "not yet run against a live
session"; spinclass runbook "Status: not run"; posh runbook "Nothing here was
run against a real posh"). clown has no bats end-to-end test of a restart.
Where those docs say "verified" they mean code or test evidence.

Tested against fakes (R): the idle poke path and preflight (fake spinclass);
`--peek` and the pre-exec/pre-mint peeks; the posh modal and countdown
(`fakePosh`); SIGTERM, the 30 s grace and the SIGKILL (stubbed kill); the
agent-scope unit kill (fake `systemctl`); the relaunch exec with `--resume`
(stubbed exec); the serve-socket wait (real unix sockets in tests); presence
re-registration (closure fakes only); the profile gate; `--when now` (fake
spinclass); spinclass's `force`, always-ask, and close-versus-restart
ordering (`RestartingAt`, 2-minute window) in spinclass's own Go tests and a
stub-clown bats.

Unverified (I): flic runs spinclass 22e43a0 and clown ca610b0 per the
runbook, neither of which has `clown restart` or `--peek`; spinclass's
`[session-entry].restart` global setting is "not known to be set"; confined
self-restart is untested (spinclass runbook: "untested (I)").

## 2. Missing for restart to work for days unattended

1. **Hung agent.** No watchdog or hang detection in either repo. The idle
   poke needs a Stop hook, so a hung turn never triggers a preflight (I).
   `--when now` (clown 011db3c) and spinclass `force` cover it only if
   something calls them; force is operator-gated (always-ask), agents cannot
   use it, and an unforced request on a hung session is cancelled by the busy
   rule every time (I). Nothing triggers a restart unattended. Detection
   input now exists on clown's side: the armed frontend writes the
   hung-session status file (turn running and since when, last hook,
   frontend pid and start ticks, unit, main pid, transcript path; FDR 0023
   "Hung-session status file", #322; R, unit tests, not run live).
   Classifying and notifying are spinclass's; clown never acts on it.
2. **Provider crash.** The frontend relaunches only when a restart plan is
   stored; otherwise the session exits and prints a resume hint. Someone
   runs `clown resume` (R).
3. **Reboot / logout.** Nothing persists: the agent unit is transient and
   there is no frontend unit or linger handling in the restart code (I).
4. **Loop protection.** None beyond consuming each request. An unforced
   restart-this-session is auto-allowed for the agent in spinclass, so an
   agent could restart itself every turn with no cap or backoff (I).
5. **Countdown with nobody watching.** posh shows nothing (exit 11/12), the
   inline countdown runs 30 s then restarts unless a turn starts; a detached
   session restarts at once. Unattended it is a delay, never a block (R). posh
   exit codes are unchecked against a real posh.
6. **Confined sessions.** The control socket is not reachable from the unit
   by design; the trigger is spinclass's hand-off outside the unit (R). A
   confined self-restart test: not found. Whether a relaunch keeps
   `--agent-scope` is for the confinement page (restartArgv uses reexecArgv,
   which carries the spec on the attach path (R)).
7. **Profile drift refusal.** Logged to `close-self-run.log` and the
   frontend's stderr only; spinclass is not told (its notification covers
   only its own refusals), so nobody is notified and the requester must ask
   again (R).
8. **Unarmed cases are refused** (so no unattended restart is possible):
   codex, `--naked`, `--print-launch-plan`, `-p`, `--continue`, a non-UUID
   key, claude with the idle trigger off.
9. **`close-self-run.log`** has no rotation (R).
10. **Accepted limits** (documented in the plan and man page): the
    unauthenticated idle poke, the busy mark left by an interrupted turn,
    `/exit` between SIGTERM and exit, close racing restart, the 2-minute
    `RestartingAt` close block, a frontend death mid-countdown.

## 3. Smallest ordered work list

1. Confirm flic's versions: clown at 011db3c or later (613bf9a for the
   pre-relaunch peek), spinclass at e581574/ae042dd or later. No issue.
2. Operator sets `[session-entry].restart` in the global sweatfile (spinclass
   runbook prerequisite). spinclass#419.
3. Detached throwaway live run per the spinclass runbook; then the race check
   (runbook step 5). spinclass#419.
4. Decide the hung-session and provider-crash policy (section 4). Hung
   sessions ruled 2026-10-26 (recovery stays operator-gated; a push
   notification for hung children, #322); provider crash still open. #322's
   clown half (the status file) is on branch pure-teak; spinclass reads it
   and notifies.
5. Confined-session restart: spinclass#382 with clown#280. No issue for the
   test.
6. `close-self-run.log` retention. #321: done (1 MiB, one `.1` generation).
   Counters for a long run (cmd/clown/restart_metrics.go, statsd, best
   effort): `clown.restart.preflight.asked`, `.preflight.verdict.<restart|
   none|refused>`, `.preflight.error`, `.cancelled`, `.stopping`,
   `.relaunch.exec`, `.relaunch.aborted`, `.relaunched` (a frontend started
   as a restart's relaunch), and `clown.status.write_failed`. R: unit tests;
   nothing observed in stats-me yet. Not counted: a request queued (clown
   does not know the requester's intent) and a forced restart (spinclass's).
7. Frontend return after reboot (user unit plus linger). No issue.
8. First live posh modal (posh runbook). No issue.
9. Adjacent: clown#241 (plugin host does not restart or surface a dead
   child), clown#287 (idle poke, cited as built), spinclass#422 (forced CLI
   lacks the always-ask gate), the `closing_at` authority-field gap in
   spinclass's loose-ends.

## 4. Decisions only the operator can make

- Ruled 2026-10-26, amended later that day: recovery of a hung session is the
  operator's (force remains always-ask), except spinclass's
  `restart-hung-child` may force-restart a busy-too-long child under the
  bounds in FDR 0023; in addition, the coordinator gets a push notification
  for hung children (#322). Design agreed with spinclass: clown writes the
  status file (facts only, FDR 0023), spinclass classifies and notifies.
- Whether a hung session with an unforced request should ever be restarted
  (today the busy cancel blocks it).
- Whether the countdown runs with no viewer (today: a 30 s delay).
- Who is told when a profile drift refusal happens (today: nobody).
- Whether a restart-storm cap belongs in spinclass or clown (none exists).
- Whether frontends should return after a reboot.
- `close-self-run.log` retention (#321).
- Whether to set `[session-entry].restart` on flic, and whether the first
  live test runs on flic's current versions or after an upgrade.
- Whether the forged-poke limit is acceptable for unattended use.
