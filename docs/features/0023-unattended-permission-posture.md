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
  - `clown agent-scope-run` is a raw runner: it runs its argv as given,
    reads no marker and applies no `dontAsk`. A confined unattended session
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
