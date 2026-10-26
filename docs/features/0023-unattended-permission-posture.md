---
status: proposed
date: 2026-10-26
issue: clown#323
related: docs/plans/2026-10-26-unattended-clown-permissions.md, docs/plans/2026-10-26-unattended-dontask-spike.md (D0), docs/plans/2026-10-25-permission-hook-events.md, FDR 0020 (agent scope)
promotion-criteria: >
  experimental: B2 to B4 landed with tests, and the D0 spike and the B5
  live checks are recorded (dontAsk applied through --settings in an
  interactive session, inside and outside an agent unit, after a restart
  and a resume). testing: spinclass sets CLOWN_UNATTENDED=1 for its dontAsk
  policy and one confined session has run unattended for a day under it.
---

# Unattended permission posture (CLOWN_UNATTENDED)

Labels as in the permissions page: **V** verified live, **R** read in code
or tested against fakes, **I** inferred.

## Problem

clown runs claude in its default permission mode, where an unallowed tool
call opens a dialog and the session waits for a person (R; no timeout
found, I: it blocks until answered). The operator ruled on 2026-10-26 that
unattended plain launches use `dontAsk`: an unallowed call is denied with no
dialog and the session carries on (V(-p): recorded only under `claude -p`
with `--permission-mode dontAsk`; I interactively). spinclass already
splices `--permission-mode dontAsk` into its confined spawns; a plain clown
launch has no way to ask for it.

## Interface

- **Marker.** The environment variable `CLOWN_UNATTENDED`, read from the
  frontend clown's process environment at launch.
  - `1` (exactly the one character): apply the unattended posture.
  - unset or empty: no posture; claude's default mode, as today.
  - any other value (`true`, `yes`, `0`, ` 1`, ...): the launch is refused,
    naming the variable and the accepted value. A typo must not leave an
    unattended session blocking at its first dialog, and "0" is spelled by
    unsetting it.
- **Effect.** clown adds `"permissions": {"defaultMode": "dontAsk"}` to the
  JSON it already passes to `claude --settings` (`claudeSafetySettingsJSON`).
  `--settings` outranks the user, project and local settings files and a
  managed (policy) source sits above it (R: the vendored
  claude-code-settings(5), dated April 2026, not checked against the
  installed claude; D0 Q3). The bytes clown passed before #323 are
  unchanged without the marker (the launch_plan bats golden). Intended,
  not yet verified (I; D0 Q4): allow and deny lists, hooks and
  `--disallowed-tools` keep working, and a PreToolUse allow still allows.
- **Claude only.** The posture is a claude permission mode. With another
  provider (codex, juggler, opencode, crush) the launch is refused: those
  providers have no equivalent clown can set, and ignoring the marker would
  leave a session the operator believes unattended waiting at prompts.
- **Refused combinations** (the launch would not end up in `dontAsk`, or
  the operator asked for two things at once):
  - `--naked`: clown execs claude directly and passes no `--settings`;
  - a forwarded `--permission-mode <m>` (any spelling) with `m` other than
    `dontAsk` (`--permission-mode dontAsk`, which spinclass splices today,
    is accepted: it says the same thing);
  - a forwarded `--inherit-permission-mode <m>` with `m` other than
    `dontAsk` (a hidden claude flag, verified in the 2.1.310 binary's
    strings);
  - `--dangerously-skip-permissions` or
    `--allow-dangerously-skip-permissions`;
  - a forwarded `--settings` (it comes after clown's and wins, per
    `internal/provider/claude.go`) or `--setting-sources`: either could set
    another mode.
  - a clownfile `[profile].env` or a named profile's env table that names
    `CLOWN_UNATTENDED`: the marker is the launcher's, set in the
    environment, never configuration (the 2026-10-26 ruling).
- **Env only, never recorded.** The marker is not written to the
  session-names sidecar. The only argument that carries it is the internal
  `--clown-unattended`, which the [attach] wrap puts beside
  `--clown-attach-id` (below); clown refuses it without that attach id, so
  it is no way to ask for the posture. Each launch reads its own
  environment:
  - restart: the relaunch's environment is clown's launch environment
    (`launchEnviron`, captured from the process environment at start)
    minus `restartEnvDropped`, which does not list the marker, so a
    restarted unattended session keeps it (R, tests incl. a child process
    started with the marker);
  - `clown resume` runs `runWithFlags` in the same process and builds its
    settings from the environment it was started with; nothing of the
    posture is recorded (R, test). What claude then does is B5;
  - agent scope: the frontend builds claude's argv, `--settings` included,
    and passes the argv into the unit, so the unit needs no marker (R,
    test); the marker is not in the unit's pass list and the agent does not
    see it.
  - [attach]: the outer clown hands the multiplexer its own environment, but
    a multiplexer whose server already runs may start the inner with the
    server's environment, without the marker; the inner would run attended
    and wait at its first dialog. So the outer adds the internal
    `--clown-unattended` beside `--clown-attach-id` in the inner's argv,
    the inner applies the posture from it whatever its environment, and the
    inner's own restart (which keeps the attach id) carries it again (R,
    tests with the marker stripped from the inner). B5 item 8 checks it
    live.
  - `clown agent-scope-run` is a raw runner: it runs its argv as given and
    applies no `dontAsk`; it reads the marker only for the unit's wall
    clock (FDR 0020, clown#320). A confined unattended session
    must be launched through clown's normal launch with
    `CLOWN_AGENT_SCOPE`/`--agent-scope`; spinclass does that (R: its spawn
    code sets `CLOWN_AGENT_SCOPE` and never calls `agent-scope-run`, read
    on disk 2026-10-26).

## Consequences

- Under `dontAsk` claude fires no `PermissionRequest` (V(-p): recorded
  under `claude -p`; I interactively, D0 Q6). All
  three `clown.permission.<key>.{prompted,approved,declined}` counters are
  driven from `PermissionRequest`, so an unattended session counts nothing:
  a call denied by `dontAsk` is invisible to them, and they cannot drive an
  allow list from unattended runs.
- An operator watching an unattended session in its TUI could switch modes
  (shift+tab); clown does not stop that (I).
- A managed settings file that sets `defaultMode` would override clown's
  (R from the vendored man page only; unverified, D0 Q3).

## Limitations

- What `dontAsk` does to `AskUserQuestion`, and whether `--settings`
  `defaultMode` behaves exactly like `--permission-mode dontAsk`
  interactively, is not verified: the D0 spike
  (docs/plans/2026-10-26-unattended-dontask-spike.md).
- clown does not notify anyone of a denial (#322).

## Live checks (B5, not run; need a PTY and a built clown)

Run from a scratch worktree with `./result/bin/clown` from `just build`.
For each, confirm the mode with `pgrep -af 'claude.*"defaultMode":"dontAsk"'`
and by asking claude to run a disallowed call (`Bash(touch /tmp/x)`): it
must be denied with no dialog and the session must carry on.

1. Plain: `CLOWN_UNATTENDED=1 ./result/bin/clown --no-attach`.
2. Launch plan (no PTY needed):
   `CLOWN_UNATTENDED=1 ./result/bin/clown --print-launch-plan` shows the
   `--settings` document with the permissions key; without the marker it
   does not.
3. Refusals: `CLOWN_UNATTENDED=yes ./result/bin/clown --print-launch-plan`,
   `CLOWN_UNATTENDED=1 ./result/bin/clown --provider codex`,
   `CLOWN_UNATTENDED=1 ./result/bin/clown --naked`,
   `CLOWN_UNATTENDED=1 ./result/bin/clown -- --permission-mode default`
   each exit 1 naming `CLOWN_UNATTENDED`.
4. spinclass's splice beside the marker:
   `CLOWN_UNATTENDED=1 ./result/bin/clown --no-attach -- --permission-mode dontAsk`.
5. Agent scope: `CLOWN_UNATTENDED=1 ./result/bin/clown --no-attach --agent-scope <spec>`
   (the `explore-agent-scope-claude` recipe's spec), and check the unit's
   environment has no `CLOWN_UNATTENDED`
   (`systemctl --user show -p Environment <unit>`).
6. Restart: in session 1, trigger a restart (the idle runner's restart
   verdict, spinclass#419); the relaunched claude's args still carry
   `dontAsk`.
7. Resume: `CLOWN_UNATTENDED=1 ./result/bin/clown resume <id> --no-attach`
   carries `dontAsk`; `./result/bin/clown resume <id> --no-attach` without
   the marker does not.
8. [attach]: `CLOWN_UNATTENDED=1 ./result/bin/clown` with a clownfile
   `[attach]` multiplexer, once with the multiplexer server not yet running
   and once with it already running; the inner claude must carry `dontAsk`
   both times, carried by `--clown-unattended` in the inner's argv
   (`pgrep -af 'clown.*--clown-unattended'`).
9. Config refusal: a clownfile with `[profile]` `env = { CLOWN_UNATTENDED = "1" }`
   refuses `./result/bin/clown --print-launch-plan`, with or without the
   marker in the environment; so does `CLOWN_UNATTENDED=1 ./result/bin/clown
   --clown-unattended --print-launch-plan` (no attach id).

## spinclass coordination (B6)

- spinclass sets `CLOWN_UNATTENDED=1` in a spawn's process environment when
  its permission policy for that spawn is `dontAsk`, and not for its `auto`
  option. The value is exactly `1`.
- spinclass keeps splicing `--permission-mode dontAsk` into confined spawns
  for now; clown accepts that splice beside the marker. #323 does not
  replace it; removing the splice is spinclass's call once B5 has passed.
- A spinclass spawn of a non-claude provider must not set the marker:
  clown refuses it.

## Hung-session status file (clown#322)

This section is the contract between clown and spinclass for telling a hung
or dead session from a working one. Both sides cite it; a change to it is
announced to spinclass first. clown exports facts only: it never kills,
restarts, notifies or answers a prompt because of the file. Classifying,
alerting and recovery are spinclass's and the operator's (the 2026-10-26
ruling: a hung session is recovered only by the operator).

- **Path.** `$XDG_STATE_HOME/clown/status/<hex sha256(principal)>.json`
  (`~/.local/state` without an absolute `XDG_STATE_HOME`), where the
  principal is the session's `CLOWN_SESSION_ID` and the hex is lower-case.
  The file is 0600 in a 0700 dir; clown opens the dir as an `os.Root` after
  an lstat (a symlinked dir is refused, the dir must be owned by the user,
  a wider mode is narrowed to 0700), as `internal/permstat` does. The dir
  is not bound into an agent unit.
- **Who writes it.** Only the frontend whose idle trigger is armed (a
  claude launch inside a spinclass session, the conditions of
  `selfCloseDecision`; `close-self-run.log` says why a launch did not arm).
  No file means: not armed, not started yet, or exited cleanly.
- **When.** Once when the trigger arms (no hook yet), on every hook
  datagram the idle listener receives, when systemd-run runs the agent unit
  (`unit`), when the unit is active with its provider started (`main_pid`),
  or when the unconfined provider starts (`main_pid`). Nothing is written
  periodically: an old `written_at` is not a sign of death. Hook writes are
  coalesced to at most one per 100 ms (a flood on the unauthenticated socket
  must not slow the reader), so a hook's effect can show up to 100 ms late.
  The file is removed when the frontend exits cleanly, before its sockets
  close, so a successor's file is never deleted; a frontend killed by SIGKILL (or
  any death that skips its deferred cleanup) leaves it, and the pid pair
  then names a dead process or a reused pid.
- **Atomicity.** Each write is a temp file in the same dir (`<name>.tmp`)
  renamed over the file, so a reader sees a whole file. Read only
  `<hash>.json`. There is one writer per principal: a second frontend of
  the same key is refused at the idle socket and leaves the file alone.
- **Failure.** A failed write is logged once to `close-self-run.log` and
  otherwise ignored; the session never depends on the file. A file that
  stopped updating may be a write failure: check that log.

| Field | Type | Meaning |
|---|---|---|
| `version` | int | `1`. |
| `principal` | string | `CLOWN_SESSION_ID`. |
| `spinclass_session_key` | string | `SPINCLASS_SESSION_ID` in the frontend's env. |
| `claude_session_id` | string | `session_id` from the last turn-start hook's stdin; empty until one reported it. |
| `turn_running` | bool | Set by the UserPromptSubmit hook, cleared by the Stop hook. |
| `turn_started_at` | time | The last turn start; kept, with `turn_running` false, after the turn ends. Empty before the first. |
| `last_hook_at` | time | The last hook datagram of either kind; empty before the first. |
| `frontend_pid` | int | The frontend clown's pid. |
| `frontend_start_ticks` | uint | Field 22 (`starttime`, clock ticks since boot) of `/proc/<frontend_pid>/stat`. With the pid it tells a live frontend from a reused pid. 0 off Linux or if unreadable. |
| `unit` | string | The agent-scope unit, when confined; empty unconfined or before systemd-run ran. |
| `main_pid` | int | Unconfined: the provider's pid. Confined: the unit's `MainPID`, the in-unit init (the provider, or the exec shim running it as a child), as the agent-scope result file records it. 0 until known. |
| `transcript_path` | string | `transcript_path` from the last turn-start hook's stdin (absolute, or the payload is dropped); empty until one reported it. |
| `written_at` | time | This write. |

Times are RFC 3339 in UTC with nine fraction digits
(`2006-01-02T15:04:05.000000000Z`), so they also sort as strings. A
turn-start hook without a payload keeps the last reported
`claude_session_id` and `transcript_path`.

- **Absent is unknown.** An empty string, a 0, an empty time or a missing
  file is "not known", never "healthy". `turn_running` false with an empty
  `last_hook_at` is a session that has not had a prompt yet.
- **What it does not give.** No `tool_open` (whether a tool call is in
  flight) and no `dialog_open_since` (whether a permission or question
  dialog is waiting, and since when): clown sees neither. A turn that hangs
  in a tool, at a dialog, or in claude itself looks the same here: a
  `turn_running` true whose `turn_started_at` keeps ageing, and a
  `transcript_path` whose mtime spinclass may read. No exit code (the
  agent-scope result file has it for a confined launch).
- **Trust.** The hooks reach the frontend over the idle socket, which is
  bound into the agent unit and is not authenticated: anything running in
  the unit as the session's user can send a turn start or a Stop, and so
  set `turn_running`, the hook times, `claude_session_id` and
  `transcript_path`. Those fields are what the agent's side said.
  `principal`, `spinclass_session_key`, `frontend_pid`,
  `frontend_start_ticks`, `unit`, `main_pid` and `written_at` come from the
  frontend itself. `transcript_path` is UNTRUSTED input: clown checks only
  that it is absolute and holds no NUL, so it may contain `..`, name a
  symlink, a FIFO or device that blocks a reader, or something under `/proc`
  or another user's tree. spinclass must not open it with more privilege than
  the agent has, and should `stat` it without following symlinks, accept only
  a regular file, and never block on it.
- **The busy datagram's payload.** The UserPromptSubmit hook command the
  frontend writes into its hooks.json carries the `--payload` argument
  (`'<clown>' idle-notify --busy --payload '<socket>'`, paths single-quoted;
  an argument, not an environment prefix, so it parses in any shell,
  including fish).
  Only with it does `clown idle-notify --busy` read its stdin (at most 1 MiB
  and 200 ms, never a terminal), take `transcript_path` and `session_id` and
  nothing else (the prompt never leaves the hook), and send `'B'` followed by
  `{"transcript_path":...,"session_id":...}` (at most 4096 bytes). Without
  the flag it sends the single byte `'B'`, as before. The flag keeps both
  directions compatible: a frontend from before #322 reads only a one-byte
  `'B'` as a turn start and would take a longer datagram for an idle poke
  (running close-self-run), and it never writes the flag; an older
  idle-notify ignores the flag and sends one byte, which the new frontend
  still reads as a turn start with no payload. A payload that is not JSON,
  is too long, names a relative transcript path or an over-long session id
  is dropped, and the datagram still counts as a turn start. The Stop hook
  sends one non-`'B'` byte and reads no stdin.
- **Status.** R: unit tests (the listener with a fake sender, the pid and
  start ticks against `/proc/self/stat`, a reader loop during writes, the
  old one-byte datagram, payloads from stdin and from absent, garbage or
  never-closing stdin, removal by the clean-exit closer, a failing write
  logged once, the dir checks, the unit and main pid writes, the hash
  path). Not run live: a real claude's hook stdin, and a confined launch's
  `unit` and `main_pid` writes (I). The owner check has no test (it needs a
  dir owned by another user).
