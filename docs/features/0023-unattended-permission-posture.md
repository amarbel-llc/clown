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
dialog and the session carries on (V under `claude -p`). spinclass already
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
  `--settings` outranks the user, project and local settings files; only a
  managed (policy) source sits above it (claude-code-settings(5)). Nothing
  else changes: allow and deny lists, hooks and `--disallowed-tools` keep
  working, and a PreToolUse allow still allows.
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
  - `--dangerously-skip-permissions` or
    `--allow-dangerously-skip-permissions`.
- **Env only, never recorded.** The marker is not written to the
  session-names sidecar and not carried as an argument. Each launch reads
  its own environment:
  - restart: the relaunch's environment is clown's launch environment
    (`launchEnviron`) minus `restartEnvDropped`, which does not list the
    marker, so a restarted unattended session stays unattended (R, test);
  - `clown resume` runs `runWithFlags` in the same process and reads the
    environment it was started with: resuming without the marker resumes
    attended (test);
  - agent scope: the frontend builds claude's argv, `--settings` included,
    and passes the argv into the unit, so the unit needs no marker (R,
    test); the marker is not in the unit's pass list and the agent does not
    see it.
  - [attach]: the outer clown hands the multiplexer its own environment;
    whether the multiplexer passes it to the inner clown is the
    multiplexer's, and not checked (a multiplexer whose server is already
    running may give the new session the server's environment instead).
    Checked live in B5.

## Consequences

- Under `dontAsk` claude never fires `PermissionRequest` (V, `-p`). All
  three `clown.permission.<key>.{prompted,approved,declined}` counters are
  driven from `PermissionRequest`, so an unattended session counts nothing:
  a call denied by `dontAsk` is invisible to them, and they cannot drive an
  allow list from unattended runs.
- An operator watching an unattended session in its TUI could switch modes
  (shift+tab); clown does not stop that (I).
- A managed settings file that sets `defaultMode` overrides clown's.

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
   both times. If the second loses it, the marker needs an argv carrier
   through the wrap (follow-up).

## spinclass coordination (B6)

- spinclass sets `CLOWN_UNATTENDED=1` in a spawn's process environment when
  its permission policy for that spawn is `dontAsk`, and not for its `auto`
  option. The value is exactly `1`.
- spinclass keeps splicing `--permission-mode dontAsk` into confined spawns
  for now; clown accepts that splice beside the marker. #323 does not
  replace it; removing the splice is spinclass's call once B5 has passed.
- A spinclass spawn of a non-claude provider must not set the marker:
  clown refuses it.
