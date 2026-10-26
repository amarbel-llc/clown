# Live-check runbook for the operator (one page, needs a PTY)

Status: not run. Everything below was only exercised against fakes; none of
it can be run by an agent session (no PTY, no real claude). Run it from a
scratch worktree after the next clown deploy, with `./result/bin/clown` from
`just build`. Mark each line PASS or FAIL and paste any FAIL's output to the
coordinator. Contract and detail: FDR 0023 "Live checks" (B5) and the D0 plan
(docs/plans/2026-10-26-unattended-dontask-spike.md).

## A. The two things that changed 2026-10-26 (do these first)

| # | Do | Expect |
|---|----|--------|
| A1 | In a spinclass session, wait until the claude is idle (prompt shown, no turn running). From another shell: `clown restart --session <principal> --when idle --reason test`. | Within seconds `close-self-run.log` (under `$XDG_STATE_HOME/clown/`) shows a `restart-preflight` line with no prompt typed. Before #327 it showed none until the next turn. |
| A2 | Same, but send the request while a turn is running (a long prompt). | No preflight until that turn ends, then one. |
| A3 | Start a session, type one prompt. In fish (your shell) read the hook: `jq .hooks.UserPromptSubmit $(ls -d <staging>/clown-selfclose-plugin-*/hooks/hooks.json)`. | Command is `'<exe>' idle-notify --busy --payload '<sock>'`, no env prefix. |
| A4 | During a turn, `cat $XDG_STATE_HOME/clown/status/$(printf %s <principal> \| sha256sum \| cut -d' ' -f1).json` (principal = `CLOWN_SESSION_ID`). | `turn_running` true, `transcript_path` and `claude_session_id` set. After the turn: `turn_running` false, `turn_started_at` kept. Confined launch: `unit` and `main_pid` set. Quit clown cleanly: the file is gone. |
| A5 | If A3 shows the right command but A4 never shows `turn_running`, claude's hook shell failed to run it: check `clown-idle-notify.log` in the worktree `.tmp`. | A failure here means hooks are not run through a shell that accepts quoted paths; report the log line. |
| A6 | After A1 and a real restart (spinclass's verdict `restart`): query stats-me, e.g. `series` pattern `clown\.restart\..*`. | Counters `clown.restart.preflight.asked`, `.preflight.verdict.restart`, `.stopping`, `.relaunch.exec`, then `clown.restart.relaunched` (from the new frontend). A refused or none verdict shows `.verdict.refused` / `.verdict.none`; a cancelled restart shows `.cancelled`. |
| A7 | Retention (#321), after a few days of an unattended session: `ls -l $XDG_STATE_HOME/clown/close-self-run.log*`, `$XDG_STATE_HOME/clown/hook-toolcost/`, and `<worktree>/.cache/clown/agent-relay.log*` (the in-unit `$HOME/.cache/clown/`). | No log above about 1 MiB plus one `.1`; no toolcost state older than 30 days. If a log is much larger, report which and its size. Growth over days was never measured live. |

Not covered by any live check yet (add if you want them): a real claude hook
run under fish (A3/A4 cover it), the in-unit read-write madder store's
growth (madder's to bound, open), and that statsd actually reaches stats-me
from inside a confined unit (the relay is `statsd-relay`; A6 shows it).

## B. dontAsk (B5)

For each, confirm the mode with `pgrep -af 'claude.*"defaultMode":"dontAsk"'`
and ask claude to run a disallowed call (`Bash(touch /tmp/x)`): it must be
denied with no dialog and the session must carry on.

1. `CLOWN_UNATTENDED=1 ./result/bin/clown --no-attach`
2. `CLOWN_UNATTENDED=1 ./result/bin/clown --print-launch-plan` shows the
   `--settings` permissions key; without the marker it does not.
3. Each of these exits 1 naming `CLOWN_UNATTENDED`:
   `CLOWN_UNATTENDED=yes ... --print-launch-plan`,
   `CLOWN_UNATTENDED=1 ... --provider codex`, `... --naked`,
   `... -- --permission-mode default`.
4. `CLOWN_UNATTENDED=1 ./result/bin/clown --no-attach -- --permission-mode dontAsk` (spinclass's splice) stays dontAsk.
5. `CLOWN_UNATTENDED=1 ./result/bin/clown --no-attach --agent-scope <spec>`;
   the unit environment has no `CLOWN_UNATTENDED`
   (`systemctl --user show -p Environment <unit>`).
6. Trigger a restart in session 1: the relaunched claude still carries `dontAsk`.
7. `clown resume <id> --no-attach` with the marker carries `dontAsk`, without it does not.
8. `[attach]` multiplexer, server not yet running and already running: inner
   claude carries `dontAsk` both times (`pgrep -af 'clown.*--clown-unattended'`).
9. A clownfile with `[profile] env = { CLOWN_UNATTENDED = "1" }` refuses
   `--print-launch-plan`, with or without the marker.

## C. D0 spike questions worth answering in the same sitting

From the D0 plan: does `--settings` `defaultMode` equal `--permission-mode
dontAsk` interactively (Q1, Q2); what does dontAsk do to `AskUserQuestion`
(Q5, the one that could hang an unattended session); which hook events fire
for a denied call (Q6). Method is in docs/plans/2026-10-25-permission-hook-events.md
(throwaway HOME, recording hook on every event).
