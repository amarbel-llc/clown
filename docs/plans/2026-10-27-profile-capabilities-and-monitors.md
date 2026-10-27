# Profile `capabilities` and the wake monitor (clown#330 design note), 2026-10-27

Labels: **R** read in code or docs, **V** observed, **I** inferred, **U** not
checked. Design only; nothing here is implemented. It extends
`2026-10-26-subagents-in-confined-sessions.md`, which sketched the list.

## The two kinds of monitor

- **R** Clown's own wake delivery is a *plugin-declared* monitor: the
  synthesized `clown-builtin-jobs` plugin's `plugin.json` carries a `monitors`
  array (`ringmaster-monitor`, and `troupe receive` / `troupe agent` when an XMPP
  credential exists; `cmd/clown/jobmonitor.go` `synthJobMonitorPluginDir`). The
  README says it plainly: "Clown does not spawn or supervise monitors — Claude
  Code does."
- **R/I** The `Monitor` *tool* is a different thing: a tool the model calls to
  start a command whose output lines come back as notifications (clown#330's
  open-world-shell concern). It is a tool name, so it can be denied by name like
  Agent. The coordinator reports spinclass's hook now has a deny floor for it
  (spinclass 8f1f019, deployed); I have not read that code (U).
- So "deny Monitor" and "keep the wake monitor" do not conflict: the wake
  monitor is not started through the tool.

## What clown owns and what spinclass owns

| Concern | Owner | Why |
|---|---|---|
| Which tools the *harness offers* the model (a name in `--disallowed-tools`) | clown (profile env `CLOWN_DISALLOWED_TOOLS`, `claudesettings.go`) | clown builds claude's argv |
| Whether a tool *call* is allowed at run time (floor, allow list, dontAsk) | spinclass (its PreToolUse hook and sweatfile) | the permission policy of the fleet |
| The vocabulary and expansion of a `capabilities` list in a profile | clown (`internal/profile`, `pkg/profilelookup`) | the profile registry is clown's; spinclass links the library |
| Pinning and refusing a changed profile | clown's gate and spinclass's use of the library digest | digest is the library's |

Defence in depth: offering no tool (clown) and denying the call (spinclass) are
independent; a capability that is off should be off at both.

## How templates would state them

A closed set, `capabilities = [...]`, where **absent or empty means none**:

| Capability | When on, the template does not disallow | Names today (R) |
|---|---|---|
| `subagents` | the Agent tool | `Agent` (the Task name in older builds) |
| `monitor` | the model-started Monitor tool | `Monitor` (I: the name is from the issue; not in any template today) |
| `schedule` (later) | cron and wakeup tools | `CronCreate`, `CronDelete`, `CronList`, `ScheduleWakeup` (already in every template's list) |
| `workflow` (later) | Workflow | `Workflow` |

`worker` and `canary-spinclass` would state `capabilities = []` outright
(today: `Agent` in their disallow list, no Monitor entry). Attended templates
list what they grant. The expansion is `disallowed = all known capability tools
minus those the profile grants`, merged with the template's explicit
`CLOWN_DISALLOWED_TOOLS`.

## Protocol and digest cost

- **R** `pkg/profilelookup/lookup.go`: "Do not reorder, add omitempty, or add
  fields without bumping ProtocolVersion", and `canonical.Protocol` is *inside*
  the canonical JSON that is hashed. One constant, `profile.ProtocolVersion`
  (2), serves both as the registry stamp and as that field.
- A new canonical field means bumping it, which changes **every** profile's
  digest: every pinned session is refused on `clown resume` and by a restart's
  dry-run gate until re-pinned, and every launcher approval recorded against an
  old digest stops matching. Not acceptable during an unattended run.
- **Shape that avoids it (I):** `capabilities` is *sugar*. Load-time expansion
  rewrites `Env["CLOWN_DISALLOWED_TOOLS"]` before `Canonicalize`, so the digest
  is the digest of the expansion; `canonical` and its protocol number do not
  change, and no existing profile's digest moves. Two profiles that expand
  alike share a digest, which is right.
- It still needs the *registry stamp* to say a newer reader is required, so an
  old clown or library refuses a file with the key instead of ignoring it
  (`pkg/profilelookup` already fails closed on unknown keys, R). That is the
  split: a `RegistryProtocol` (3 when any profile uses `capabilities`, written by
  `Save` as today's `protocolFor` does for `base`) and a `CanonicalProtocol`
  (stays 2, the value inside the hashed JSON). Both are public library
  behaviour that spinclass links, so the split must be announced to
  spinclass first (the package doc's rule).
- `pkg/profilelookup.Resolve` must apply the same expansion, or spinclass would
  digest the unexpanded profile (a test must pin that both paths agree).

## Smallest first slice

1. **Now, no protocol change:** add `Monitor` to `CLOWN_DISALLOWED_TOOLS` in the
   templates that have no business with it (`worker`, `canary-spinclass`). It
   changes only those two profiles' digests, like the Agent change did (so it
   is best batched with any other change to them, and sessions pinned to the old
   digests re-pinned once). Redundant with spinclass's floor, deliberately.
2. **Next:** the registry/canonical protocol split plus a parser for
   `capabilities` that expands into the disallow list, `worker` and
   `canary-spinclass` migrated to `capabilities = []` *without changing their
   expanded env* (so their digests do not move), and a test that every builtin
   expands to what it carried before.
3. Only then new capabilities.

## Can `TaskStop` stop the wake monitor?

- **R** `TaskStop` is a built-in tool: "Stops a running background task by its
  ID". Claude Code reports the plugin monitor as a background task with an id; the
  `[clown-job]` notifications in a session carry it as `task-id` (V: this session
  receives them). So the model can name that id and stop it.
- **I** Stopping it ends all wake delivery to that session (job completions,
  chat, the restart self-wake of #329) until the session is relaunched; nothing
  in clown restarts a stopped monitor, since Claude Code owns it. I have **not**
  called `TaskStop` on a monitor (U): it would cut this session's wakes.
- **R** `TaskStop` is not in any builtin template's disallow list, and a worker
  uses it legitimately to stop its own background subagents/tasks today (the
  `Agent` change reduces that need in the two moxy-less templates).

### Option A: unstoppable by removing the tool

Add `TaskStop` to `CLOWN_DISALLOWED_TOOLS` in the unattended templates.
- One line per template; digest of those profiles changes (batch with the
  `Monitor` line).
- Cost: the worker cannot stop *any* background task, only wait for it or let
  it finish. Likely acceptable for `worker`/`canary-spinclass` (no Bash, no
  subagents), not for attended templates.
- Does not stop anything else that can end the monitor: killing the process
  needs Bash, which these templates lack.

### Option B: keep the tool, refuse the wake monitor's id

A PreToolUse hook (clown already has `clown-hook-allow`) denies `TaskStop`
when its `task_id` is a clown monitor's.
- No digest change, and ordinary background tasks stay stoppable.
- Needs the hook to know the monitor's task id. Claude Code assigns it at spawn;
  clown does not see it (U whether it is in the hook payload for a start event
  or discoverable in the transcript). Without that it can only deny
  `TaskStop` outright when `CLOWN_UNATTENDED=1`, which is Option A without the
  digest cost and without the template listing.
- A hook is bypassed if the hook process is not run (`--naked` is refused
  unattended, R `unattendedGate`), so it is the weaker of the two.

### Recommendation (I; the operator has not ruled)

For the two unattended templates, Option A with `Monitor` in one digest-moving
edit, because it removes the tool instead of policing a call, and those sessions
have no use for `TaskStop`. Keep Option B in reserve if an unattended session
turns out to need `TaskStop` for something else. Either way a stopped wake
monitor is only recoverable by a relaunch, so the status file's `restart_refusal`
and spinclass's liveness stay the backstop.

## Not verified

- The `Monitor` tool's exact name in claude 2.1.310 and whether a worker
  session exposes it (the issue's first probe found no Monitor tool in a plain
  dontAsk session, so the escape may not exist there at all).
- Whether `TaskStop` accepts a plugin monitor's task id (I from the tool text and
  the notification format; not called).
- spinclass's Monitor deny floor (the coordinator's report, code unread).
