# Why a relaunched session gets its brief again (investigation, 2026-10-26)

Finding only; nothing is changed. Labels: **V** observed, **R** read in code or
docs, **I** inferred. The report: after a `clown restart`, a throwaway worker
(circus/light-beech, clown d3645fac9ff1) said the same brief arrived a second
time in its conversation, then a notice that its MCP servers disconnected, then
a block re-announcing their instructions, with its earlier turns still in
context.

## What delivers the brief, and why it comes again

1. **The brief is claude's positional prompt.** R: spinclass's default
   `spawn-entry` is `["clown", "--clown-attach=spawn", "--", "{prompt}"]`
   (`internal/0/sweatfile/sweatfile.go` `SessionSpawnEntry`; sweatfile(5)
   `spawn-entry`: "`{prompt}` is replaced by the driver's brief verbatim,
   staying within its single argv element"). So clown receives the brief as
   an operand after `--`, and claude receives it as its `[prompt]` argument
   (V: claude 2.1.310's CLI declares `-r, --resume [value]` and a `[prompt]`
   positional, from `just explore-claude-binary-strings`). On a fresh launch
   that is the first message of the session.
2. **A restart's relaunch replays that operand.** R: the old frontend records
   its argv with `restartArgv` (`cmd/clown/restart_relaunch.go`), built from the
   launch's resolved flags (`reexecArgv`, `flags.forwarded`). It drops only the
   `--session-id` / `--resume` occurrences, puts `--resume <principal>` before
   the `--`, and **keeps every operand after `--`**. The brief is such an
   operand, so the relaunch runs, in effect,
   `clown ... --resume <principal> -- <provider flags> <the original brief>`.
3. **Claude then sends the prompt into the resumed conversation.** I: a
   positional `[prompt]` given together with `--resume` is submitted as a new
   user message after the conversation is restored; that is the observed
   behaviour (the brief appeared after the earlier turns, as a new message) and
   matches the CLI's grammar, but I did not run claude with that argv.
4. **The disconnect notice and the re-announced instructions are the
   restart itself, not the brief.** R/I: the old provider process (and its MCP
   connections) stopped, the new one started and connected its servers again,
   and each server's instructions block is announced to a new process. They
   come after the replayed brief only because the brief is submitted first.

## Is it deliberate?

Keeping operands is deliberate for correctness: `TestRestartArgvResumesThePrincipal`
(`restart_relaunch_test.go:42-49`) pins "operands after `--` stay, and the new
`--resume` goes before them (clown#318)", so a literal operand such as `-rf`
is not mistaken for a session flag and dropped. Nothing in the code, the
tests, `clown-restart(1)`, FDR 0020 or the restart plan says the initial prompt
should or should not be replayed, and nothing mentions it as a known effect. So:
the keep-operands rule is deliberate, the brief replay is an **unconsidered
consequence** of it (I).

## When it fires

- **Every restart relaunch of a session that was launched with an initial
  prompt** (R: all of them go through `restartArgv`). Spawned workers always are
  (the default `spawn-entry`); an operator-launched session with no prompt has
  no operand to replay, so it is unaffected.
- **Not** `clown resume <id>` or `sc resume` (I, neither path re-read for this
  note): `clown resume` builds its own argument list from the session id
  (`resumeLaunchFlags`, `cmd/clown/resume.go`) rather than replaying the
  original operands, and spinclass's resume entry carries the principal, not
  the prompt (sweatfile(5) `[session-entry].resume`).
- Not on an idle poke, a cancelled restart, or the first start.

## Why it matters

An unattended worker mid-task sees its original brief arrive as a fresh user
message after "restarted: <reason>" in its system prompt. It may restart the
task, redo finished work, or re-ask the questions the brief contains. The
restart notice in the system prompt (`restartedNotice`) says the conversation
is the same session but says nothing about a replayed brief (R).

## Options

| Option | What it does | Cost and risk |
|---|---|---|
| **A. Suppress on relaunch (clown)** | `restartArgv` drops the initial-prompt operand, so the relaunch resumes without a new user message. | Needs to know which operand is the prompt. After `--` the provider args mix claude flags with values (`--model opus`, `--permission-mode auto`, spinclass splices these before the brief) and the prompt, so "the last operand" is wrong when there is no prompt and the last element is a flag's value. A correct rule needs claude's grammar (which flags take values), which clown has only for the session flags (`resumeOccurrences`). Medium effort, risk of dropping a real operand or keeping the prompt. |
| **B. Record that the prompt was delivered (clown)** | At the first launch, clown notes in the session sidecar (`session-names.jsonl`) the exact operand tail it passed; `restartArgv` removes exactly that tail when the same session relaunches. | No grammar needed: it removes what was recorded. A new field in a durable file (compatible: unknown keys are ignored by older clowns). Small to medium; a relaunch of a session launched before the field existed still replays. |
| **C. Mark the resend (clown, one line)** | `restartedNotice` (the system-prompt fragment for a relaunch) adds: "if your original brief arrives again as a message, it is a replay caused by this restart; do not restart or redo your task, continue where you were". | A sentence and a test; no argv logic. It relies on the model heeding the notice, and the replayed message still costs a turn and tokens. Not a fix, a mitigation. |
| **D. Leave and document** | Say in `clown-restart(1)` and the restart plan that a spawned worker sees its brief again. | Free; leaves the hazard to every future restart of an unattended worker. |

## Recommendation

Do **C now** (cheap, safe, helps every case including sessions that predate
B), and **B** as the actual fix, because it removes exactly the replayed
operand without teaching clown claude's flag grammar. A is only worth it if B's
sidecar field is refused for some reason. Do not choose D alone: restarts
around day five of an unattended trip are exactly when a worker has real work
in flight.

## Not done / needs the coordinator

- **No reproduction.** I did not spawn anything and have no throwaway. A cheap
  unit-level check, without a throwaway, is a test that builds the argv
  `restartArgv` returns for `forwarded = ["--", "--permission-mode", "auto",
  "<brief>"]` and shows the brief in it; I have not added it because that
  would pin the behaviour this note calls a bug. If you want a live repro, a
  throwaway spawned with a short brief and then restarted once shows it; tell
  me whether one exists.
- Whether claude replays the positional with `--resume` is I until that live
  run (or running `claude --resume <id> "x"` in a scratch session).
- The `restart-child-session` text still saying "until clown#327 is deployed"
  is spinclass's (light-cedar), as you said.
