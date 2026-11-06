# clown#344: a plugin disconnect record the session reads as an MCP resource (design note)

Status: design only, nothing implemented; implementation waits for the
operator's review (coordinator, 2026-11-06). Claims are **R** (read in code
at 70f904a) or **I** (inferred) unless marked otherwise. Companion: FDR 0002
(the stdio bridge), RFC 0002 (plugin host lifecycle), FDR 0023 (the
hung-session status file, whose state-dir conventions this reuses).

## The problem

On 2026-11-05 a coordinator's cutting-garden connection closed mid-batch
(cutting-garden#392). The session saw "connection closed" and nothing else;
a read of `~/.local/log/clown/clown-plugin-host-*.log` could not say which
log was its own, which call was in flight, which side closed, or whether the
child was alive. The operator's direction: on a plugin disconnect or crash,
clown writes one record the affected session reads directly as an MCP
resource, and the session is told where it is.

## Where each kind of disconnect is detected today (R)

The frontend clown hosts plugins in-process (`cmd/clown/main.go`,
`pluginhost.Host` with `BaseEnv` carrying `CLOWN_SESSION_ID`); each
`stdioServers` entry is a `clown-stdio-bridge` child of the host, which in
turn runs the plugin's stdio server as its own child
(`cmd/clown-stdio-bridge/main.go`). Three processes, two pipes, one HTTP hop.

| Kind | Where it is seen | What is logged now | What the session sees |
|---|---|---|---|
| Child exit (crash, clean exit, OOM kill) | bridge `run`: `cmd.Wait` on `childDone` | one stderr line "wrapped child exited unexpectedly: <err>" / "cleanly"; the bridge exits 1 after a 2 s `srv.Shutdown` | in-flight POSTs hang on `respCh` until the shutdown drops the connection: "connection closed"; later calls: connection refused |
| EOF on child stdout without exit | translator `runReader` returns nil; `Run` returns | "translator: <err>" only on a read error, nothing on plain EOF; pending entries are never failed | the same hang, until the client cancels |
| EOF on child stdin (child closed its end; EPIPE on write) | translator `runWriter`: `logExitOrphans` | "write to child stdin failed: <err>; writer exiting; pending_orphaned=N" | queued requests are never written; after 256 the queue is full and new calls get `ErrQueueFull` |
| Client cancel (claude's own timeout or abort) | mcphttp spine: `post end id=.. outcome=ctx_canceled` | the end line, on the bridge's stderr, forwarded to the host log under the server's prefix | claude's own timeout error; the bridge marks the id abandoned (#50) |
| Bridge-side per-call timeout | none exists; the request context is the HTTP request's | | |
| Kill (SIGTERM/SIGINT to the bridge) | bridge `run`: `sigCh` | "received <sig>; shutting down"; the child gets SIGTERM, SIGKILL after 5 s | the same as child exit, with the bridge gone |
| Parent gone (serve mode only) | `watchParent` | "parent clown is gone; shutting down the serve socket" | n/a: the frontend is dead |
| HTTP serve error | `serveErr` | "HTTP serve error" | connection errors |
| Bridge process exit, seen from the host | `ManagedServer.reap` closes `done`; **nothing watches it during the session**; `Stop` at shutdown logs "plugin server died unexpectedly" with exit code and signal | only at session end | nothing |

Child stderr is the bridge's stderr (`cmd.Stderr = os.Stderr`), which the
host's `forwardStderr` writes to its log as `plugin stderr line=...`; the
bridge never sees those lines itself. The host log is
`$XDG_LOG_HOME/clown/clown-plugin-host-<utc>-<pid>.log`, named by the host
pid, not the session; nothing maps a session to it (the issue's point 1).
The spine's `post start` (id, method, body size) and `post end` (id,
outcome, elapsed, transport) lines already give the issue's point 2 for one
bridge, but they land on the bridge's stderr, forwarded into the host log
under a server prefix that is the same for every session's bridge of that
plugin, so a dozen live bridges are indistinguishable there, and a start
without an end is found only by reading the whole log.

No log scrubber exists today: the only redaction in clown is the launch
plan's key-based `redactEnvEntry` (`cmd/clown/launchplan.go`), applied to
environment entries, never to log lines. "Scrubbed the same way logs are"
therefore has to be defined here.

## Design

### Who writes, who serves, who tells

Three roles, placed where the facts are:

1. **The bridge writes the record** for everything it can see: child exit,
   stdout EOF, stdin write failure, a signal it did not expect, a serve
   error. It holds the pending map (in-flight ids), the child's pid and wait
   status, and, once it tees the child's stderr (below), the last stderr
   lines. It learns its identity from the host: `CLOWN_SESSION_ID` is
   already in its environment (`BaseEnv`); the desugaring in
   `pluginhost/compile.go` adds `--plugin <name> --server <name>` to the
   synthesized argv so the bridge knows what it is wrapping.
2. **The host writes the record** for the one thing the bridge cannot: the
   bridge itself dying. A goroutine per started server waits on
   `ManagedServer.Done()` and, if it closes before `Shutdown`, writes a
   record of kind `bridge_exit` with the bridge's exit code or signal and
   the host log path. (The bridge's own `expected shutdown` notice, below,
   lets the host skip a bridge that the host stopped.)
3. **The frontend serves the records** as MCP resources, from one place
   that outlives any plugin: a built-in in-process HTTP MCP server on the
   `mcphttp` spine, registered in the synthesized builtin plugin as
   `clown-builtin-diagnostics/disconnects` (the pattern of
   `clown-builtin-jobs`, `cmd/clown/jobmonitor.go`; the frontend moxin of
   `frontendmoxin.go` shows a frontend-served server reaching a confined
   unit over the per-session socket, which this reuses). It answers
   `initialize`, `resources/list`, `resources/templates/list` and
   `resources/read` from the session's record directory, and nothing else
   (no tools). Under confinement it is forwarded into the unit like
   ringmaster and troupe (`frontendServerAddrs`).
4. **The session is told twice.** (a) When the bridge is still alive, it
   fails every in-flight request itself instead of letting the connection
   drop: a JSON-RPC error whose message is the record's one-line summary
   and ends with the resource URI, delivered before the bridge shuts its
   HTTP server. (b) The frontend sends the session one job-wake message
   (`restartwake.go` already sends wakes to the principal) with the same
   summary and URI; this covers a bridge death with no call in flight and
   the case where the agent never saw the error. The next tool result after
   a reconnect is **not** rewritten: the bridge exits after a child death
   (FDR 0002 "Crash behavior") and claude's reconnect lands on a refused
   port, so there is no later result to annotate; a wake is the honest
   channel. Keeping the bridge up and restarting the child is FDR 0002's
   deferred `restart` field, not this change.

### The bridge's expected-shutdown notice

The host's `Stop` sends SIGTERM to every bridge at session end; today a
bridge cannot tell that from an external kill. Before `Stop` signals, the
host POSTs `/clown/shutdown` (a control route beside `/clown/exclude-tools`,
frontend-only in serve mode, loopback-only otherwise). A bridge that has
seen it writes no record for the SIGTERM that follows and tells the host
nothing more; a SIGTERM without the notice is a `signal` record. The host
likewise writes no `bridge_exit` record for a bridge it just stopped. This
keeps a clean session end record-free.

### Tee of the child's stderr

The bridge gives the child a pipe for stderr instead of its own fd, copies
every line to its own stderr unchanged (so the host log is unchanged) and
keeps a ring of the last `stderrTailLines` (40) lines, each cut to
`stderrLineBytes` (512) bytes, with the time it arrived. A child that writes
a line over 1 MiB is read with the same bounded scanner the translator uses;
the cut keeps the head.

### The record

One JSON object per event, written atomically (temp file in the same
directory, then rename, as the status file does), 0600 in a 0700 directory.

| Field | Meaning |
|---|---|
| `version` | 1 |
| `session` | the principal (`CLOWN_SESSION_ID`) |
| `plugin`, `server` | the clown.json names the bridge wraps |
| `n` | this record's number within the session and plugin, from 1 |
| `at` | when the event was seen (RFC 3339 UTC, nine digits, as FDR 0023) |
| `kind` | `child_exit`, `child_stdout_eof`, `child_stdin_closed`, `signal`, `parent_gone`, `http_serve_error`, `bridge_exit` |
| `closed_by` | `child`, `bridge`, `host`, `external`, `unknown`: which side the kind implies (the child for the first three; `external` for a signal without the notice; `host` is never recorded, since the notice suppresses it) |
| `bridge_pid`, `child_pid` | pids |
| `child_exit` | `{code, signal}` when the child exited (from `cmd.Wait`); absent when it was still alive (stdout EOF with a live child, a signal) |
| `bridge_exit` | `{code, signal}` on a `bridge_exit` record (from the host's `waitState`) |
| `uptime_ms` | since the bridge started |
| `in_flight` | `[{id, method, started_at, elapsed_ms}]` for every pending entry with a live `respCh`; `abandoned` likewise for entries the client already cancelled |
| `queue_depth`, `dropped_outbound` | the translator's counters at the moment |
| `stderr_tail` | `[{at, line}]`, the ring above, scrubbed (below) |
| `host_log` | the host log path, for the operator; empty when the writer does not know it |
| `uri` | this record's resource URI |

`method` requires the translator to remember it: `pendingEntry` gains
`method`, set by the spine's caller (it already parses the method for the
`post end` line and the stats label). No arguments or results are ever
recorded (the issue's constraint for point 2).

### Scrubbing

Applied to every `stderr_tail` line and to the error summary; defined here
because nothing scrubs log lines today:

- a `KEY=VALUE` fragment whose key matches the launch plan's
  `secretEnvKeyRe` (`TOKEN|KEY|SECRET|PASSWORD`, case-insensitive) has its
  value replaced by `<redacted>` (the same rule as `redactEnvEntry`);
- a token that looks like a credential is replaced whole: `Bearer <x>`,
  `sk-`/`ghp_`/`gho_`/`xox[abp]-`/`AKIA` prefixes, and any run of 32 or more
  base64 or hex characters;
- control characters other than tab become spaces (the status file's
  `oneBoundedLine` rule), so a line cannot carry an escape sequence into a
  terminal that prints the record.

The same function is offered to the host's `forwardStderr` as a follow-up,
so the host log scrubs the same way; that is not part of this change, and
whether the host log should be scrubbed at all is a question for the
operator (it is the operator's own file, and scrubbing it loses evidence).

### Resource URIs and who may read

- `clown://session/<principal>/plugins/<plugin>/<server>/disconnects` lists
  the session's records for that server, newest first, as a JSON array of
  `{n, at, kind, uri}`.
- `clown://session/<principal>/plugins/<plugin>/<server>/disconnects/<n>`
  is one record, `application/json`.
- `clown://session/<principal>/plugins/disconnects` lists across every
  plugin of the session (the entry point when the agent does not know
  which plugin failed).

The diagnostics server is per frontend, so per session: it reads only its
own principal's directory and answers `resource not found` for any other
principal in a URI, so a session cannot read another's records even on the
same host. Under confinement the serve socket admits only the unit and the
parent (peercheck), as for every frontend-served server. The read needs
claude's `ReadMcpResourceTool`: the worker template allows it since
13f773c; reviewer, porter and the canaries disallow it, so for them the
error summary and the wake carry the facts and the URI is for the operator
(`clown disconnects <session>`, a verb that prints the same records,
is the operator's surface and a cheap addition).

### Storage and retention

`$XDG_STATE_HOME/clown/disconnects/<hex sha256(principal)>/<plugin>/<server>/<n>.json`,
beside the status file's directory and with its hardening (`openStateDir`:
a plain directory owned by the user, 0700, opened as an `os.Root`). The
directory is not bound into an agent unit; the unit reads records only
through the server.

- Records outlive the reconnect, the bridge and the frontend: a resume of
  the session finds them.
- Per server, at most `maxRecords` (20) are kept; the oldest is removed
  when the next is written.
- At every launch the frontend sweeps the whole `disconnects` tree and
  removes a principal's directory whose newest record is older than
  `retention` (30 days). "Pruned with the session" in the stricter sense
  needs the closer: spinclass removes the worktree on close and could run
  `clown disconnects prune --session <principal>` as a hand-off; that is
  spinclass's call and the age sweep is the floor without it.
- The counter `n` is the highest existing number plus one, read under the
  directory root, so concurrent writers (the bridge and the host for the
  same event) cannot collide: the host writes only `bridge_exit`, the
  bridge never does, and each takes the next number after a directory
  listing with a retry on `EEXIST` (`O_EXCL`).

### Compatibility

A bridge started by an older host (no `--plugin`) writes no record and
behaves as today; an older bridge under the new host ignores nothing it
does not know (the control route is a POST it answers 404 to, which the
host treats as "no notice support" and then does not suppress the record,
so one spurious `signal` record per session end is the worst case during a
mixed deploy). The host log format is unchanged. No profile, protocol or
digest changes.

## What this does not do

- No restart of a dead child (FDR 0002's deferred field) and no change to
  claude's reconnect behaviour, which clown does not control.
- No server-side request log inside the plugin (cutting-garden's own
  issue).
- No session-to-log index file: the record carries `host_log`, which is
  the mapping the issue's point 1 asked for, for the cases that matter.
  The spine's `post start` and `post end` lines already cover point 2 for
  a call that never ends without a disconnect; stamping them with the
  session id (the bridge has `CLOWN_SESSION_ID`) is the small follow-up
  that would make them searchable per session.

## Open questions for the operator

1. Should the host log's `plugin stderr` lines be scrubbed with the same
   rule, or left verbatim as the operator's evidence?
2. Is a job-wake message the right second channel, or is the error on the
   failed call enough?
3. `maxRecords` 20 and `retention` 30 days.
4. Whether spinclass's close should prune the directory (a hand-off to
   spinclass), or the age sweep suffices.

## Test plan

Unit, `cmd/clown-stdio-bridge` (a fake child script in `t.TempDir`):

1. Child exits mid-request: every in-flight request gets a JSON-RPC error
   whose message ends with the record URI; the record has `kind`
   `child_exit`, the exit code, the in-flight ids with methods and
   elapsed, `closed_by` `child`.
2. Child closes stdout and stays alive: `child_stdout_eof`, no
   `child_exit`, the child's pid.
3. Child closes stdin: `child_stdin_closed` with `pending_orphaned` as the
   in-flight list; later calls get `queue_full` as today.
4. SIGTERM without the notice: a `signal` record, `closed_by` `external`;
   after `POST /clown/shutdown`, SIGTERM writes none.
5. Stderr tail: 60 lines in, the last 40 kept in order; a 2 KiB line cut to
   512 bytes; a line with `TOKEN=abc`, a `Bearer` token and a 40-char hex
   run comes out redacted; a line with an escape sequence comes out
   printable; the host-side copy on the bridge's stderr is verbatim.
6. The record is one file, 0600, written by rename (no partial file is
   ever visible to a concurrent reader loop, the status file's test shape).
7. `n` increments across records and survives a bridge restart within the
   same session directory.
8. No `--plugin`: no record, no error text change.

Unit, `internal/alfa/pluginhost`:

9. A bridge that exits during the session produces a `bridge_exit` record
   with the exit code and the host log path; `Stop` after the notice
   produces none.
10. The desugared argv carries `--plugin` and `--server` (compile test).

Unit, the diagnostics server (`cmd/clown`):

11. `resources/list` and `resources/templates/list` shapes; `resources/read`
    of the list and of one record; an unknown `n`, an unknown plugin and
    another principal's URI are `resource not found`; the server has no
    tools.
12. The wake message names the URI and the summary, once per record.
13. Retention: the 21st record removes the 1st; the launch sweep removes a
    principal directory older than 30 days and keeps a newer one.

Live check after the operator's go (a built clown, a scratch plugin whose
server exits on a given tool call): the failed call's error names the URI,
`ReadMcpResourceTool` on `clown-builtin-diagnostics` returns the record, and
`clown disconnects <session>` prints it.
