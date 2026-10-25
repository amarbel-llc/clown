# `clown restart`: hand-off from spinclass and idle restart (spinclass#419)

Status: built on pure-teak (see the commits that add `cmd/clown/restart*.go`);
not yet run against a live session. The user-facing description is
clown-restart(1); spinclass's half is spinclass FDR 0037. This note was
first written against spinclass c942d87; the contract has moved since
(through spinclass ae042dd, read in conversation, not re-checked here):
`restart-preflight --peek` exists (read-only, reports `closed` for a close
that has started) and clown uses it before the exec (20 s bound) and again
in the relaunched frontend before its presence registration and `troupe
mint` (5 s bound, "clown: checking session state" on stderr; closed exits 0
with one stderr line, close-self-run.log being the durable record); a force restart is refused until `--when now` exists (see
the design note below); a queued `cancel-restart` is dropped when the
preflight answers `none`; and spinclass stamps `RestartingAt` when it
consumes a restart, so a close is refused for 2 minutes after that. The
verdict's reason reads "restart of KEY requested by REQUESTER: REASON",
which is what reaches the resumed session's prompt.
This note records what was built, the decisions taken, and what is unverified.

## Contract (spinclass side)

1. `restart-this-session` / `restart-child-session` record the request, then
   exec the live-read `[session-entry].restart` argv, e.g.
   `["clown","restart","--session","{principal}","--when","{when}","--reason","{reason}"]`:
   no shell, 15 s bound, cwd the worktree. A non-zero exit clears the request
   and shows clown's output to the caller.
2. At idle, clown runs `spinclass restart-preflight` with
   CLOWN_SESSION_ID=<principal>. It prints one JSON line
   `{"verdict":"restart"|"none"|"refused","reason":...,"when":...}` and exits
   0; it consumes the request, so the relaunched session does not loop.

## What clown does

- `clown restart --session K [--when idle] [--reason R]` dials
  `$XDG_RUNTIME_DIR/clown/control/K.sock` (unix stream, 0600 in a 0700 dir,
  same-uid peers only, a live socket of the same key is never taken over),
  sends `{op,when,reason}` and reads `{ok,error}`. Exit 1 with
  "no live clown frontend for K" when nothing answers, or the frontend's
  refusal ("restart needs an idle trigger; <provider> has none"; claude with
  the trigger off names the gate, and so does a claude launch whose
  conversation id is not its principal, which a relaunch could not resume).
  Only `--when idle`; anything else, and every usage error, exits 2
  (pinned by `TestRestartVerbArgvContract`).
- The inline countdown (no posh modal) has no viewer cancel, but a turn
  start during it still cancels the restart, as with the modal.
- The frontend binds the control socket for every launch that runs its
  provider as a child (not --naked, codex or --print-launch-plan), so an
  unarmed frontend can say why. Decision: the brief said "listens only when
  armed", but then an unarmed provider could only ever produce "no live
  frontend"; answering and refusing gives the message the contract asks for.
- Queue: one pending {when, reason}; a newer request replaces it.
- Idle (the existing clown#287 poke), fixed order: close-self-run; then, only
  if a restart is queued, take it and run the preflight (30 s bound). Only a
  well-formed `restart` proceeds; none, refused, non-zero, timeout, garbage or
  more than one line drop the request (logged in close-self-run.log). A close
  that landed gives `none`, which drops it.
- Restart: an attached session gets the 30 s countdown first (below). Then
  the frontend records the relaunch plan and SIGTERMs itself, which every
  runProvider path forwards to the provider; runWithFlags unwinds (plugin host
  and serve bridge stopped, agent unit stopped, idle and control sockets
  closed); main waits up to 15 s until no socket under
  `$XDG_RUNTIME_DIR/clown/serve/<principal>/` answers, then execs `clown` from
  PATH: the launch's resolved flags (reexecArgv, so --profile rides along),
  claude's --session-id/--resume replaced by `--resume <principal>`, the
  [attach] id kept for an inner clown, env = clown's start-up env with
  CLOWN_SESSION_ID=<principal> and CLOWN_RESTARTED_REASON, minus the
  launcher's one-shot CLOWN_PROFILE* approval. The profile gate runs as on any
  resume (a changed profile is refused). The relaunched frontend turns the
  reason into a "restarted: <reason>" system-prompt fragment (one line,
  control characters removed, 300 runes max).
- Countdown: POSH_SESSION set, posh on PATH and `posh modal --probe` answering
  `"modal":"ok"` -> `posh modal <session> --timeout 30 ...` (0 proceed, 10
  cancel; busy/invalid fall back; any other end of a shown modal proceeds). Otherwise an attached terminal gets
  an inline "restart pending (...); restarting in 30s" line on stderr and the
  wait, no cancel. Detached: no countdown.
- After the independent review (a0276f3..3eafd5b):
  - the relaunch's profile gate is dry-run before the countdown and the stop
    (restartProfileCheck); a refusal leaves the session running;
  - a UserPromptSubmit hook (`clown idle-notify --busy`) marks a turn running;
    a turn that starts during the countdown cancels the restart (checked every
    250 ms inline, after a proceeding posh modal, and again before the stop);
  - the relaunch's reason is the verdict's (spinclass's), sanitized (printable
    only, 300 runes) and fixed text when empty; the socket's reason is
    sanitized at queue time and otherwise unused;
  - preflight and posh stdout go to a file, so a grandchild cannot stall idle;
  - (superseded in round 2: the plan is now kept and the provider SIGKILLed,
    see below) exec failure names `clown resume <principal>`;
  - the control socket unlinks only its own inode, and an over-long path is a
    clear error;
  - providerRunning is set only once each runProvider path forwards SIGTERM
    (ptysuspend.OnStart now runs after its handler; agentscope Launch.OnStarted);
  - posh: a shown modal that ends without an answer proceeds, no second wait.
- mint-revoke: no frontend exit path runs `troupe mint-revoke` today (grep:
  only juggler's own sessions, `internal/jugglerrun`, and a debug recipe). The
  monitor's clean shutdown removes the presence record (ringmaster
  jobwake.RemovePresence); the relaunch re-registers it and re-runs the
  idempotent `troupe mint`. A test drives idle -> preflight -> stop -> exec
  with a recording troupe and asserts it is never run.

## Not verified / open

- No live session has been restarted; the self-SIGTERM stop, the unit stop and
  the re-exec keeping the terminal are read in code, not observed.
- posh's modal is the sleek-banyan prototype (d36f7296), not installed here;
  the probe/args follow its source, untested against a real daemon. POSH_GROUP
  is not passed.
- The --agent-scope spec path is reused by the relaunch; whether spinclass
  keeps that spec (and its result file) valid after the first launch is not
  checked.
- The notice is a system-prompt fragment, so the resumed agent sees it on its
  next turn; nothing prompts it to take one.
- "restart pending (waiting for idle)" at queue time is not shown (it would
  draw over the TUI mid-turn).

## Second review (f3a4f93..)

- A provider still running 30 s after the stop gets SIGKILL and the plan is
  kept (it was dropped before, stranding the session); providerRunning is
  re-checked after the countdown.
- The idle socket refuses a live listener of the same key and unlinks only
  its own inode.
- A restart cancelled after the preflight consumed it (profile refusal,
  countdown cancel, turn start) is reported on stderr and in the log; the
  requester must ask again.
- The verdict's reason is sanitized in the log; the log files are opened
  O_NOFOLLOW.
- The reason cap is 300 runes including the "...".

## Third review (65cc3ad..)

- The grace-period SIGKILL hits the unit under --agent-scope
  (`systemctl --user kill --signal=SIGKILL <unit>`, only while that Run
  owns it), and claude through the frontend's own process handle otherwise
  (never a reused pid); killProvider checks and kills under one lock.
- Keeping the plan made any exit after the stop relaunch, including a close
  in the window. Now, while a plan is stored, idle pokes run nothing and the
  control socket refuses a second restart; before the exec the frontend
  checks the worktree still exists (spinclass removes it on close) and exits
  0 without relaunching if not.
- Log opens are O_NOFOLLOW|O_NONBLOCK and a failed open says so on stderr;
  the idle socket path gets the length check; listeners remove the socket
  file on Chmod/Stat failure; the cancel line is printable text only.

## Limits accepted after review

- The idle poke is not authenticated (second review #1). The idle socket is
  bound into the unit and anything there running as the session's uid can
  send a poke or a turn-start byte; hooks and the agent's own Bash cannot be
  told apart by cgroup, and a token in the hook command would be readable by
  the agent. A forged poke cannot start a restart spinclass did not
  authorize (the preflight verdict gates it), but once one is queued the
  agent can time it, including mid-turn, or cancel it with a forged turn
  start.
- A turn interrupted before its Stop leaves the turn-running mark set; a
  queued restart then waits for the next Stop ("queued" means "at the next
  idle") (#5).
- A SIGTERM very early in an agent-scope launch could leave an orphan unit;
  unverified (#7).
- Failures after the stop are stop-then-hope: a provider that survives even
  SIGKILL, or a relaunch that fails, leaves the session for `clown resume`
  (#8). While such a plan stays stored, idle pokes run nothing, so the
  session cannot self-close until it exits.
- A user /exit typed between the stop's SIGTERM and the provider's exit
  (normally well under a second; up to the 30 s grace only for a provider
  that does not exit on SIGTERM) is not told apart from the stop's exit and
  relaunches the session (third review R1c); one typed during the countdown
  ends the session without a relaunch (the plan is stored only while the
  provider still runs). Telling the two exits apart by status or signal
  would differ across the direct, pty-suspend and agent-scope paths and
  depend on claude-code's SIGTERM handling (I, unchecked); the cost is one
  relaunch the user can /exit again.
- The forged idle poke above is reachable by design and low severity: the
  idle socket is bound into the agent unit on purpose (`BindPaths=` in
  `internal/agentscope/unit.go`) because the in-unit Stop and
  UserPromptSubmit hooks must reach it, and without --agent-scope any
  same-uid process can send one. ProtectHome=tmpfs protects the control
  socket, not the idle one. The harm is the agent timing or cancelling its
  own already-authorised restart, which it could do by exiting anyway, so
  no mitigation is planned.
- Close racing the restart, remaining gap after the peek (I): a close that
  starts after the peek and revokes the principal before the relaunched
  clown starts could be undone by it, since the relaunch runs the
  idempotent `troupe mint --session-key <principal>` and re-registers its
  presence. Narrowed since: the relaunched frontend peeks again before it
  registers or mints (preMintPeek), and spinclass ae042dd refuses a close
  for 2 minutes after a consumed restart; what is left is a close landing
  between that second peek and the mint, or one the peek cannot report.
  Closing it fully needs spinclass to order a close against a restart it
  has consumed.
- Liveness before the exec: `spinclass restart-preflight --peek`
  (spinclass 2a79497, read-only) reports `closed` for a session not found,
  tombstoned, abandoned or with a close started (ClosingAt), so a close in
  progress is now caught, not only one that reached the worktree removal.
  An unknown peek (non-zero exit, as from the deployed spinclass 22e43a0
  without `--peek`, a timeout, garbage, a missing state) relaunches, and the
  worktree check stays as the fallback. What remains: a close that starts
  after the peek, or one the peek cannot report, until spinclass removes
  the worktree (its last step).
- Under pty-suspend the SIGKILL reaches claude only; its children survive
  only if they also ignore the SIGTERM, and pluginhost group-kills the
  serve children (fourth review #7).
- A FIFO with a stalled reader at a log path can still park the idle
  goroutine on write (O_NONBLOCK covers only the open) (fourth review #8).
- The Chmod/Stat cleanup branches in the listeners have no test.
- Live-socket refusal (control and idle) is best effort: two frontends
  starting at the same instant can both see no live socket (third review
  #6).
- Under pty-suspend the SIGKILL now reaches claude only, not its process
  group (a reaped group leader's pgid could be reused); claude's own
  children are left to the session teardown.
- L2 (inline countdown line over the TUI) stands; the round-3 #8 hygiene
  notes are accepted as is.

- The inline countdown line is written to stderr, over claude's TUI (L2).
- L3 and L4 of the review are accepted as documented here, not changed.
- The no-revoke proof covers the idle -> preflight -> stop -> exec path only,
  with fakes; the frontend's unwinding between stop and exec (plugin host,
  monitors, unit) is shown clean of `mint-revoke` by grep, not by a run (L6).
- There is no bats end-to-end test of a restart; everything is Go unit tests
  with a fake spinclass, fake posh and stubbed exec.
- The busy signal is UserPromptSubmit: a turn started by something that does
  not fire it (an injected wake, if claude does not run the hook for it) is
  not seen; that is inferred, not checked against claude.
- Between the last busy check and the SIGTERM there is a window of a few
  instructions in which a turn can still start.
- A profile edited after the dry run and before the relaunch is still refused
  after the stop.

## Design note: `--when now` (not built)

Status: design only, nothing implemented. The semantics below are as agreed
with spinclass (bozo) in conversation; open choices are marked **Q**.

### Semantics

- `clown restart --session K --when now --reason R` still only asks: it
  queues on the control socket like `idle`, exit 0 meaning accepted or queued
  and non-zero meaning refused (spinclass reads only 0 against non-zero).
- The frontend runs `spinclass restart-preflight` itself, at once, instead
  of waiting for an idle poke. A `restart` verdict consumes the request as
  today; with `when:now` spinclass treats the running-job obstacle as
  pre-approved (its side, not clown's).
- The profile gate dry run still runs before anything is stopped; a refusal
  drops the restart as today.
- After the verdict the provider is stopped at once (the same SIGTERM path),
  killing the current turn: the turn-running mark is expected to be set and
  is not a reason to wait or cancel.
- Countdown, clown and posh's decision. The lean: keep the 30 s posh modal
  (or the inline notice) so a viewer can still cancel, but a turn start is
  not a cancel (busy is the normal state for `now`). **Q:** keep the
  countdown at all for `now`, or stop immediately? **Q:** a detached session
  restarts at once, as for `idle`?
- Everything after the stop is unchanged: the kept plan, the 30 s SIGKILL
  grace, the serve-socket wait, the `--peek` before the exec and the second
  peek before the relaunch's presence registration and mint.

### What the frontend needs

- A non-idle trigger: today the idle poke is what takes the queued request
  and runs the preflight (`idleRunner.onIdle`). `now` needs the control
  handler (or a goroutine it starts) to do the take, preflight and restart
  itself, serialized with the idle runner so the two never run a preflight
  for the same request (one mutex around take-preflight-restart, shared with
  `onIdle`).
- `restartControlHandler` accepts `when == "now"` only when the trigger is
  armed for it; `frontendRestarter.restart` gets the `when` so it can skip
  the busy re-check and the turn-start cancel for `now`.
- The control reply must come back within the verb's 10 s read deadline
  while the preflight can take 30 s: answer `{ok:true}` once the request is
  accepted, then run the preflight asynchronously. **Q:** or keep the verb
  waiting for the verdict and raise its deadline (spinclass's hand-off is
  bounded at 15 s, so probably not)?

### Races

- A turn running: expected; it is killed by the stop. The kill is the point
  of `now`.
- An idle poke arriving while a `now` preflight runs: the shared mutex makes
  it wait; once the plan is stored, `restartUnderway` makes the poke a no-op
  as today.
- A second `clown restart` (either `when`) while one is underway: refused as
  today ("already underway"). A `now` arriving while an `idle` request is
  queued but not taken: **Q:** replace it and act at once (the queue's
  newest-wins rule), or refuse?
- A close racing a `now` restart: as for `idle` (the two peeks); spinclass's
  RestartingAt stamp (ae042dd) makes close refuse for 2 minutes after a
  consumed restart.

### What stays refused

- `now` for a provider with no idle trigger (non-claude) or an unarmed one,
  as `idle` is today; a launch whose claude conversation id is not its
  principal; a second restart while one is underway; a profile the
  relaunch's gate would refuse.

### Tests that would be needed

- Verb: `--when now` exits 0 and queues `when:now` (today exit 2, pinned in
  `TestRestartVerbArgvContract`; that row flips).
- Handler: `now` accepted only when armed; reply before the preflight
  finishes.
- Restarter: `now` stops a busy session without the turn-start cancel;
  profile refusal still drops it; a viewer cancel still cancels.
- Serialization: a concurrent idle poke does not run a second preflight.
- spinclass contract: a fake spinclass returning `restart` with
  `when:"now"`.

### Size

M: one new trigger path and its locking, a `when` threaded through the
restarter, the verb and handler acceptance, the countdown policy, and the
tests above; no new external interface beyond accepting `now`.
