# Permission hook events in Claude Code 2.1.308 (clown#302, step 1)

Status: findings draft, no product code. Labels: **V** observed in a run,
**R** read (binary strings, docs), **I** inferred.

Method: `just explore-permission-hook-events` (justfile, explore group). Each
case is a throwaway `claude -p` (haiku) in its own git repo under
`.tmp/permission-hooks/<case>/` with only project settings
(`--setting-sources project`). Every candidate event (PreToolUse,
PermissionRequest, PermissionDenied, PostToolUse, PostToolUseFailure,
Notification, UserPromptSubmit, Stop) runs a hook that appends its stdin JSON
to `hook.log`; the PermissionRequest hook can answer allow or deny. The raw
sequences are in `.tmp/permission-hooks/*/hook.log` (not committed).

## Event names in the binary (R)

`just explore-claude-binary-strings` on claude-code 2.1.308 lists, among the
hook events: `PreToolUse`, `PermissionRequest`, `PermissionDenied`,
`PostToolUse`, `PostToolUseFailure`, `PostToolBatch`, `Notification`,
`UserPromptSubmit`, `Stop`. Notification has a `notification_type` of
`permission_prompt` ("Claude needs your permission to use ..."), among
`idle_prompt`, `auth_success`, `elicitation...`. PermissionRequest runs on the
"ask path" (`decideLocation:"ask-path"`), and its hook output can decide
(`decision.behavior` allow/deny, deny with a `message`). PermissionDenied takes
a `retry` output ("to tell the model it may retry"): it is the event for a
denial made without asking, by the auto-mode classifier (R/I; not exercised).

## Observed sequences (V)

| case | setup | events, in order | tool ran |
|---|---|---|---|
| auto | Read, allowed by `--allowedTools` | UserPromptSubmit, PreToolUse(Read), PostToolUse(Read), Stop | yes |
| default | `touch X`, no allow, no hook decision (`-p`) | UserPromptSubmit, PreToolUse(Bash), PermissionRequest(Bash), Stop | no |
| approve | same, PermissionRequest hook answers allow | UserPromptSubmit, PreToolUse, PermissionRequest, PostToolUse, Stop | yes |
| reject | same, PermissionRequest hook answers deny | UserPromptSubmit, PreToolUse, PermissionRequest, Stop | no |
| dontask | same, `--permission-mode dontAsk` | UserPromptSubmit, PreToolUse, Stop | no |

Payloads (V):

- **PreToolUse**: `session_id`, `prompt_id`, `permission_mode`, `effort`,
  `tool_name`, `tool_input`, `tool_use_id`, `cwd`, `transcript_path`. No
  decision field: PreToolUse runs before the permission check and says nothing
  about whether a prompt follows.
- **PermissionRequest**: `session_id`, `prompt_id`, `permission_mode`,
  `effort`, `tool_name`, `tool_input`, `permission_suggestions` (e.g.
  `addDirectories`, `setMode acceptEdits`), `cwd`, `transcript_path`. **No
  `tool_use_id`**: it can be tied to its PreToolUse only by session, prompt_id,
  tool_name and tool_input (I: ambiguous if the same call repeats in one turn).
- **PostToolUse**: as PreToolUse, plus `tool_response`, `duration_ms`.
- **Stop**: `last_assistant_message`, `stop_hook_active`, `background_tasks`,
  `session_crons`, no tool fields.
- **UserPromptSubmit**: `prompt`, `prompt_id`, `permission_mode`.

Not seen in any `-p` case (V): Notification, PostToolUseFailure,
PermissionDenied. A refusal (the `-p` default denial, a hook deny) is marked by
no event of its own: after PermissionRequest there is simply no PostToolUse
or PostToolUseFailure for that call. dontAsk denies an unallowed tool without
a PermissionRequest at all.

## Not reproducible non-interactively (unverified)

- **A person approving or rejecting at the dialog.** In `-p` there is no
  dialog: an unallowed call is denied after PermissionRequest (case default).
  Whether the interactive dialog fires PermissionRequest first, then
  Notification(permission_prompt), and what follows an approval or a reject
  at the dialog, needs an interactive session (I: the same PermissionRequest
  then PostToolUse / nothing, from the shared "ask path").
- **Interrupted** (Esc at the dialog, or ^C): needs an interactive session;
  no event for it is known (I: the turn ends, Stop may or may not fire).
- **MCP tools**: not run (no MCP server in the throwaway repo). `tool_name`
  is `mcp__<server>__<tool>` per Claude Code's docs (R); the events should be
  the same as for built-in tools (I).
- **PermissionDenied**: needs auto mode (classifier) (I).

## Proposed event -> counter mapping (clown.permission.<server>.<tool>.*)

`<server>` is the MCP server for `mcp__<server>__<tool>`, else `builtin`;
`<tool>` the tool name.

| counter | from | observable |
|---|---|---|
| `prompted` | PermissionRequest | V (fires whenever the ask path is taken, also in `-p`) |
| `approved` | PostToolUse (or PostToolUseFailure) for a call that had a PermissionRequest | V via a hook allow; I for a person's approval |
| `rejected` | a PermissionRequest with no PostToolUse/PostToolUseFailure for that call by the next PreToolUse/Stop | V, but only by absence; cannot tell rejected from interrupted |
| `interrupted` | none known | not observable from hooks (unverified) |
| (auto-denied) | PermissionDenied | R only (auto mode) |

Consequences for the design (I):

- The counters need per-session state: remember each PermissionRequest
  (keyed by tool_name and a hash of tool_input, since it carries no
  tool_use_id) and resolve it on the next PostToolUse/PostToolUseFailure
  (approved) or at the end of the turn without one (rejected or
  interrupted). As built, the key is session + tool name + canonical input
  hash, not prompt_id, and a PreToolUse does not resolve (below).
- "rejected" and "interrupted" may have to be one counter (`declined`) unless
  an interactive run shows a distinguishing event.
- A PermissionRequest hook must not answer (no output), or it would make the
  decision itself.

## As built (step 2)

`internal/permstat`, driven by `clown-hook-toolcost` (the same binary and
statsd emitter as `clown.tool_result.*`), registered by the synthesized
clown-builtin-jobs plugin for PermissionRequest, PostToolUse and
PostToolUseFailure (matcher `.*`), UserPromptSubmit, and the Stop and
SessionEnd it already had:

    clown.permission.<key>.prompted:1|c   PermissionRequest
    clown.permission.<key>.approved:1|c   PostToolUse / PostToolUseFailure of a pending request
    clown.permission.<key>.declined:1|c   still pending at Stop, the next UserPromptSubmit or SessionEnd

- `<key>` is `toolcost.ToolKey` (`builtin.<Tool>`, `<server>.<tool>` for MCP,
  the plugin segment dropped), as for `clown.tool_result.*`; no host segment.
- **declined includes interrupts**: it is rejected-or-interrupted, since hooks
  show neither (above). An Esc or ^C at the dialog, or an interrupt while the
  request is pending, counts as declined, not as its own outcome. The names
  leave room for `rejected` and `interrupted` if an interactive run finds an
  event that tells them apart.
- A later PreToolUse does not decline a pending request: parallel tool calls
  interleave (I). So a decline is counted when the turn ends, not when the
  next call starts.
- State: one file per session under `$XDG_STATE_HOME/clown/hook-toolcost/
  permstat` (`~/.local/state/...` without XDG_STATE_HOME: the toolcost
  hook's own `userpath.StatePath` ladder, which ignores a relative value;
  `$TMPDIR/clown-permstat-<uid>` only when no home dir is known either), 0600
  in a 0700 dir, flock'd, O_NOFOLLOW, at most 32 pending requests, read
  bounded at 64 KiB, corruption read as empty, removed when the turn ends. A
  PostToolUse with nothing pending creates no file. It used to live under
  `$XDG_RUNTIME_DIR/clown/permstat`; files left there are not migrated or
  swept (the tmpfs clears them on reboot). Under `--tent` the variable is
  forwarded but the dir is not bound, so the state is container-local there
  (R, `internal/tent/tent.go`).
- The dir is agent-writable: inside `--agent-scope` the agent runs as the
  same uid, and the host-side hook uses the same dir. So nothing in it is
  trusted. The dir is lstat'ed (a symlink or non-directory is refused), then
  opened as an `os.Root`, which must be the very directory lstat'ed (a swap
  in between is refused before anything is done there); every later
  operation (open, lock, remove, the sweep's listing and reads) is relative
  to that root and cannot leave it, and the 0700 narrowing is an fchmod on
  it. The owner check only keeps out other users; it does not protect
  against the agent, which is the same uid. Keys read back from a state file
  reach statsd only if they have the toolcost key shape
  (`toolcost.ValidKey`), and at most 32 entries are read.
- Files left by sessions killed mid-turn are swept on each PermissionRequest,
  only those whose name has the state-file shape (32 hex digits, `.json`),
  never any other file however old: an empty or unreadable one past 24h, one
  still holding a pending request only past 7 days (mtime is the last write,
  not liveness: a person can sit at a dialog for a day). At most 16 are
  removed per sweep, and at most 4096 entries are looked at. The state dir
  survives reboots, so this sweep is what bounds it (tested in that dir:
  `TestSweepRunsInTheStateDirAndIgnoresTheOldLocation`).
- Locking is best effort: up to 5 attempts share one 200ms deadline overall,
  the lock tried without blocking, then the hook skips the bookkeeping
  (prompted still counts) rather than stall the agent. After locking, the
  file must still be the one the path names (a turn boundary may have
  removed it meanwhile); otherwise the next attempt reopens it, so no request
  is written to an unlinked file, and no end-of-turn hook reads one that
  another already took. The toolcost transcript budget (1s at SessionEnd)
  starts before the permission bookkeeping, so the two together stay within
  it.
- Pairing is by session, tool name and a hash of the tool input, decoded and
  re-encoded with sorted keys so key order and whitespace do not matter.
  PermissionRequest has no tool_use_id, and PreToolUse's cannot be borrowed:
  with parallel calls nothing ties a PreToolUse to the PermissionRequest that
  follows. It is first in first out; for two identical requests in one turn
  the counts are right even when it cannot say which ran. Inputs that
  canonicalise alike (duplicate keys, invalid UTF-8) pair with each other,
  which is harmless: the tool name is part of the key, and a mispairing
  within one tool leaves the counts right. Past 32 pending, the oldest is
  dropped, counted neither approved nor declined.
- The hook prints nothing on any of these events and always exits 0
  (tested with malformed input and statsd unreachable); prompted is sent even
  when the state file cannot be written.
- Which counters work where:

  | where | prompted | approved | declined |
  |---|---|---|---|
  | outside a unit | yes | yes | yes |
  | inside `--agent-scope` | yes | yes (I) | yes (I) |

  Inside a unit the hook gets `XDG_STATE_HOME` set to the frontend's value
  when that is absolute, else the operator's `~/.local/state`
  (`agentscope.OperatorXDG`, `internal/agentscope/unit.go`), so
  `permstat.Dir` is the same path inside and out (R), and `stageAgentHome`
  binds its parent `$XDG_STATE_HOME/clown/hook-toolcost` read-write (R,
  pinned by `TestStageAgentHomeBindsThePermstatDir`). That all three counters
  then work inside a running unit is inferred, not seen live (I): the
  hook-level test `TestReadOnlyRuntimeDirStillCountsAllThree` reproduces
  the unit's read-only `XDG_RUNTIME_DIR` with a writable `XDG_STATE_HOME`
  outside a unit. Earlier, with the state under `$XDG_RUNTIME_DIR` (a
  read-only tmpfs in a unit, ProtectHome=tmpfs), only `prompted` counted
  inside `--agent-scope`; with a state dir that cannot be created, that is
  still the behaviour anywhere (`TestUncreatableStateDirCountsOnlyPrompted`).
- Cost: the binary now also runs on every PostToolUse (I: one short process
  per tool call).

### Verifying the interactive case (unverified so far)

A person approving, rejecting or interrupting at the dialog has not been
recorded. To record it in one throwaway interactive session, run the recipe
once to set up the case dirs (it makes one small model call per case), then
start an interactive session in the `default` case dir. That dir records
every event and its PermissionRequest hook makes no decision, so the dialog
appears:

    just explore-permission-hook-events
    cd .tmp/permission-hooks/default && rm -f hook.log X && \
      env -u CLOWN_SESSION_ID -u SPINCLASS_SESSION_ID -u CLAUDE_SESSION_ID \
      claude --setting-sources project --model haiku \
      "Run exactly this one Bash command, once, verbatim: touch X"

At the dialog, approve. Then ask for `touch Y` and reject it, then ask for
`touch Z` and press Esc. Then `/exit`, and read the log:

    jq -c '{e: .recorded_event, tool: .input.tool_name, cmd: .input.tool_input.command, ntype: .input.notification_type}' hook.log

The questions are whether Notification(permission_prompt) fires, whether an
approval at the dialog gives PostToolUse as a hook allow does, and whether a
reject or an Esc gives any event at all (Stop or PostToolUseFailure). The
recipe does not record SessionEnd.
