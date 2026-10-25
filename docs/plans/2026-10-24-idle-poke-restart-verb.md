# `clown restart`: hand-off from spinclass and idle restart (spinclass#419)

Status: built on pure-teak (see the commits that add `cmd/clown/restart*.go`);
not yet run against a live session. The user-facing description is
clown-restart(1); spinclass's half is spinclass FDR 0037 (spinclass c942d87).
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
  the trigger off names the gate). Only `--when idle`.
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
  cancel; busy/invalid/other fall back). Otherwise an attached terminal gets
  an inline "restart pending (...); restarting in 30s" line on stderr and the
  wait, no cancel. Detached: no countdown.
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
