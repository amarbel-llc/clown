# Unattended clown: permissions

Status page, 2026-10-26, clown master da76a79 (+ this page). Goal: a clown
session that runs for days with nobody answering prompts. Docs only; no code is
proposed or changed here.

Labels: **V** verified live (a recorded real run; only the `claude -p`
recordings in `docs/plans/2026-10-25-permission-hook-events.md` qualify),
**R** read in code or tested against fakes (named), **I** inferred,
**not found**. Spinclass was read on disk.

## 1. What is proven, faked, unverified

Proven (V, `-p` only, claude-code 2.1.308): the hook event sequences for an
auto-allowed call, a default-mode unallowed call (`PermissionRequest`, then
denied, tool not run), a hook-approved and a hook-denied call, and `dontAsk`
(denied with no `PermissionRequest`). `Notification`, `PostToolUseFailure` and
`PermissionDenied` never fired in `-p`.

Tested against fakes (R): `clown-hook-allow` decisions
(`cmd/clown-hook-allow/main_test.go`); spinclass's permission tier check and
always-ask floor (`internal/charlie/perms/*_test.go`, `hooks_test.go`); the
`clown.permission.<key>.{prompted,approved,declined}` counters (permstat
tests; the hook path from recorded sequences to counters is R, no live
statsd check).

Unverified: a person approving, rejecting or pressing Esc at the dialog (the
recipe is written, unrun); whether an unanswered dialog ever times out (no
timeout found in clown code; I: the session blocks); `Notification` for
`permission_prompt` interactively; in-unit counting of all three counters;
Claude API auth and every credential's expiry behaviour (not found in-tree).

## 2. What clown sets up (R)

- A global deny list (`--disallowed-tools`: Bash(*), WebFetch, WebSearch,
  Write, EnterWorktree, NotebookEdit, PowerShell, LSP, Glob, Grep) plus
  per-profile extras.
- `--settings` env: `CLAUDE_AFK_TIMEOUT_MS=2147483647` (AskUserQuestion
  auto-continue off, clown#163: still to verify on the current build),
  `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`.
- **No `--permission-mode`, `--allowedTools` or `defaultMode`** anywhere in
  clown's Go code: claude runs in its default mode, where an unallowed call
  prompts.
- `clown-hook-allow` (PreToolUse): allows reads under `/nix/store`, every
  `mcp__plugin_clown-builtin-jobs_*` tool, rewrites the Explore agent to the
  read-only Discover subagent; the juggler paid-API tool is deliberately left
  to prompt; everything else defers.
- spinclass: `sc perms-check` (tier files, always-ask floor first) and a
  PreToolUse that turns every ask into a deny under `dontAsk`; spinclass
  splices `--permission-mode dontAsk` into its **confined** spawns. The
  always-ask floor covers force close/restart, grant/release handle, accept,
  and unconfined spawns; confined spawns are carved out.

## 3. With nobody at the keyboard

- `-p`: an unallowed call is denied after `PermissionRequest`; the session
  continues with the denial (V).
- `dontAsk`: denied with no dialog (V). Force operations are denied, never
  waited on, so the destructive operation does not run (I).
- Default interactive: the dialog waits; no timeout found; I: the session
  blocks until a human answers or the process is killed.
- No path notifies the operator that a session is stuck at a prompt
  (`Notification` hooks are not wired).

## 4. Missing for days-long unattended operation

1. No defined unattended permission mode for a plain clown launch (spinclass
   sets `dontAsk` only for its confined spawns).
2. No notification for a prompt-stuck session or a refused profile change.
3. No consumer of the permission counters and no live proof of interactive
   outcomes; nothing reads the counters to change an allow list.
4. Profile-change approval needs the operator; the approval surface is
   CLI-only until spinclass#407. An unattended session whose profile drifted
   stays down (R/I).
5. Credentials: forge push credentials are minted per session from the
   operator's card; with the card absent a session either refuses or, with
   `--allow-no-credential`, falls back to the inherited ssh-agent (ruled
   against for unattended use, #324); no expiry
   or refresh path found; commit signing is unavailable (unsigned commits are
   operator-accepted); XMPP/claude API credential behaviour is outside this
   repo.
6. Open security calls that touch long runs: #292 (URL credentials in the
   canonical profile form), #288 (stdio-bridge loopback peer), #279 (tent
   exposes `~/.config/nix`), #300 (egress allowlist).
7. The profile-pin guarantee is weak against an agent that controls clown's
   environment (FDR 0022).

## 5. Smallest ordered work list

1. Run the interactive recipe (permission-hook-events plan) and record
   approve/reject/Esc: #302; this fixes the real `declined` semantics.
2. Live-check #243 (MEMORY.md still loaded) and #163 (AFK auto-continue).
3. Run the #40 `clown-hook-allow` deny control with live credentials: #40.
4. Implement the unattended permission posture, ruled `dontAsk` (section 6),
   as a settings or `--permission-mode` injection. #323 (related: #276
   profile-level spawn permission schemes, #271 headless juggler-only agent
   loop).
5. Operator notification for a stuck prompt or a refused profile change.
   #322 (related: #214 monitor exits mid-session).
6. After step 1: spike an auto-allow decision from the counters: #302.
7. Posture for the credential fallback and the absent card: no fallback to
   the inherited ssh-agent (ruled, section 6). #324.
8. Security calls: #292, #288, #279, #300.
9. Profile approval from another surface: spinclass#407.

## 6. Decisions only the operator can make

- Ruled 2026-10-26: unattended plain launches use `dontAsk` (silent denial),
  not the default mode that blocks at the dialog (#323; not implemented
  yet).
- Ruled 2026-10-26: no fallback to the inherited ssh-agent when the card or
  the mint fails; the session goes without push credentials (#324).
- Ruled 2026-10-26: a prompt-stuck or hung session is recovered only by the
  operator; the coordinator gets a push notification for hung children
  (#322, design TBD).
- Whether unsigned commits are acceptable for unattended work.
- Whether the counters may ever drive auto-allow, and at what threshold.
- Profile-change policy when unattended: stay down until approved (today) or
  allow narrowing changes automatically (FDR 0022 decision 6, later).
- The security calls #292, #288, #279, #300.
- Whether force, restart and unconfined spawns stay operator-only unattended
  (today yes, by the always-ask floor).
- Related open rulings: #304 (keep refusing an all-stale exclusion list:
  ruled), #316 (keep undiscovered servers: ruled), #317 (left for the juggler
  extraction), #303 and #305 (fixed).
