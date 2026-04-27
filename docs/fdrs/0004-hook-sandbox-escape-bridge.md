---
status: drafting
date: 2026-04-27
---

# Hook Sandbox-Escape Bridge

## Abstract

This record specifies clown's implementation of the host-mediated hook
bridge defined in RFC 0007. It introduces:

1. A new `hooks` block in `clown.json`, parsed alongside `httpServers`
   and `stdioServers` in `internal/pluginhost`.
2. A dispatch loop in `clown-plugin-host` (in-process; not a separate
   binary) that listens on a per-session unix socket and spawns
   registered handler commands on the host with `cwd` set to the
   provider session's working directory.
3. A small `clown-hook-shim` binary that the sandboxed provider
   spawns as the `command` entry of every bridged hook. The shim
   proxies stdin, stdout, stderr, and exit code over the socket.
4. A change to clown's provider invocation that builds the hook
   block as a JSON object and passes it via `--settings` rather than
   touching `.claude/settings.local.json`.

The bridge is a transport adapter, not a sandbox. It assumes that
some other component (`clownbox`, a future provider sandbox, the
user's own setup) has scoped the provider's filesystem and that
hooks need to escape that scope to perform host-level work. RFC 0006
already establishes that clown does not impose isolation on plugins;
this FDR continues that posture.

## Motivation

Issue #34 describes the problem in the abstract; one consumer makes
it concrete. spinclass is a clown plugin that ships three hooks —
`PreToolUse`, `PostToolUse`, and `Stop` — each of which currently
invokes `spinclass hook` as a `command`-type entry. Today this works
because Claude Code is unsandboxed and the `spinclass` binary
inherits ambient privileges to read `~/.local/state/spinclass/...`,
walk to the main repo via `git -C`, append to a tool-use log
outside `.claude/`, and consult per-user permission tier files.
Under any sandbox that scopes the provider to a session worktree,
all of these break silently: the hook process exits non-zero, the
provider treats the tool call as denied, the session becomes
unusable.

A few less obvious motivations beyond just "hooks need host
access":

1. **Provider startup is no longer pristine.** spinclass today
   writes `.claude/settings.local.json` itself
   (`internal/sweatfile/apply.go:248-290`) to register its hooks,
   leaving on-disk state that survives sessions and conflicts with
   user-managed settings. Routing hook configuration through clown's
   `--settings` argv removes the on-disk artifact entirely.
2. **Lifecycle uniformity.** Today, hook handlers run inside the
   provider's process tree with the provider's environment.
   Routing them through the host helper gives them the same lifecycle
   guarantees that HTTP MCP servers already have under
   `clown-plugin-host`: structured logging, clean teardown, peer-cred
   trust check.
3. **Discovery uniformity.** `clown.json` already centralizes plugin
   declarations for HTTP and stdio MCPs. Adding `hooks` puts hook
   handlers in the same place, so a plugin author has one manifest
   to learn.

The current state of clown for context: there is no clown-imposed
sandbox today (RFC 0006). `clownbox` is an optional bwrap-based
wrapper users may run clown under (`cmd/clown/main.go:622-672`).
Future Claude Code releases or third-party wrappers may add their
own sandboxing. This FDR designs for the case where any of those
arrive; clown's behavior is unchanged for users who don't sandbox
the provider.

## Non-goals

- **Adding a sandbox.** This FDR adds no isolation. RFC 0006 still
  applies: clown takes no position on plugin isolation. The bridge is
  for the case where some other component supplies a sandbox.
- **Designing a generic IPC.** The wire format in RFC 0007 §5 is
  purpose-built for the hook stdin/stdout/exit-code shape. It is not
  a substitute for the JSON-RPC stream the stdio MCP bridge speaks.
- **Modifying the provider or the hook protocol.** A bridged hook is
  indistinguishable from an ordinary `command`-type hook from the
  provider's perspective.
- **Handling events the provider does not emit.** The dispatch loop
  is event-name-agnostic; whatever event names appear in `clown.json`
  are dispatched if the provider sends them. We do not enumerate or
  validate event names against `claude-code-hooks(5)`.

## Design

### 1. Components

Three pieces of code change or appear:

- **`internal/pluginhost`**: gains a `Hooks` field in the parsed
  `clown.json` config (`internal/pluginhost/config.go`), and
  `Discover()` (`internal/pluginhost/host.go:53-82`) collects
  per-plugin hook entries into a flat dispatch table alongside the
  HTTP server list.
- **`clown-plugin-host`**: gains an in-process dispatch loop that
  binds the unix socket, accepts connections, and runs registered
  handler commands. This is a goroutine inside the existing host,
  not a separate binary.
- **`cmd/clown-hook-shim`**: a new tiny Go binary, no dependencies
  beyond stdlib, that does one thing: read stdin, dial
  `$CLOWN_HOOK_SOCK`, send the request frame, read the response,
  forward stdout/stderr, exit with the returned code. Distributed
  alongside `clown` itself.

### 2. In-process vs out-of-process helper

We keep the dispatch loop in-process inside `clown-plugin-host`. An
out-of-process helper (a `clown-hook-helper` binary) would buy us
language-boundary isolation and a separate restart strategy, but
neither is needed: the helper has no work outside a clown session,
and there is no reason for it to outlive the host. Sharing the
pluginhost lifecycle gets us logging, signal handling, and graceful
shutdown for free.

### 3. Shim

The shim is intentionally minimal: under 200 lines of Go, no
external dependencies. It exists because:

- The provider needs a `command` entry to spawn. That command runs
  inside the sandbox, which means it must be allowed to dial the
  socket. A shim is the smallest thing that fits.
- Putting the socket-dialing logic in any larger binary (e.g. the
  plugin's own handler) would force every plugin to link the
  protocol. The shim makes the protocol clown's responsibility, not
  every plugin's.

Argv: `clown-hook-shim <plugin-id> <event-name> <ordinal>`. Reads
`$CLOWN_HOOK_SOCK` from the environment. Reads its own stdin to
EOF, builds the request frame per RFC 0007 §5.1, sends, reads the
response, writes the response's stdout to its own stdout and stderr
to its own stderr, exits with `exit_code`.

If `$CLOWN_HOOK_SOCK` is empty or the dial fails, the shim exits
127 with a stderr message naming the missing socket. This is the
case where a misconfiguration left the shim wired up but the host
never started; the provider treats it as a hook failure, which is
the right behavior.

### 4. Helper integration

`pluginhost.Host.Discover()` today iterates plugin dirs, parses
`clown.json`, and collects HTTP servers
(`internal/pluginhost/host.go:53-82`). We extend it to also collect
hook entries into a `[]DispatchEntry` keyed by
`(pluginName, eventName, ordinal)`.

After discovery and before the provider is spawned, the host:

1. Generates a 128-bit session id.
2. Creates the parent directory and binds the socket per RFC 0007 §3.
3. Starts the dispatch goroutine accepting connections.
4. Records the socket path so `runProvider` (`cmd/clown/main.go:374-425`)
   can set `CLOWN_HOOK_SOCK`.

After the provider exits, the host calls `Shutdown()`, which closes
the listener, joins the dispatch goroutine, and unlinks the socket
and parent directory. On startup, if a stale socket from a previous
SIGKILL'd session is present, the host detects it via the
`connect()` probe specified in RFC 0007 §3 and unlinks before
binding.

If discovery yields zero hook entries, the host skips socket
creation entirely and the provider runs unmodified. The bridge has
zero cost when no plugin uses it.

### 5. `--settings` construction

`runProvider` today executes the provider with
`executor.FormatArgs(args)`. We add a step that, when the dispatch
table is non-empty, synthesizes a JSON settings object of the form:

```json
{
  "hooks": {
    "PreToolUse": [
      {"type": "command", "command": "/path/to/clown-hook-shim spinclass PreToolUse 0"}
    ]
  }
}
```

and prepends `--settings <json>` to the provider's argv.

If the user's argv already contains `--settings`, we MUST merge the
two settings JSONs rather than override. The merge semantics: each
hook event is array-concatenated, with clown's bridged entries
appended after the user's. (Choosing user-first is the safer
default: a user explicitly setting up a hook arrangement gets it
honored, and the bridged hook fires after — neither shadows the
other since both run.)

If the user passes `--disable-clown-protocol` or
`CLOWN_DISABLE_CLOWN_PROTOCOL=1`, no `--settings` is injected and
the bridge is fully off. This matches the existing pluginhost
escape valve in `cmd/clown/main.go:319-368`.

### 6. Cwd choice

The host helper runs each handler with `cwd` set to clown's own
working directory at the time the provider was spawned, NOT the
shim's reported cwd. Reasoning:

- spinclass and similar plugins expect to see the worktree they
  were started from. That's the directory the user invoked clown
  from, which is also the directory the provider was started from
  before any sandbox scoped its filesystem view.
- Trusting the shim's reported cwd would give a sandboxed process a
  way to influence handler placement (set `CWD=/tmp` or similar).
  Pinning to the host's pre-sandbox cwd takes that off the table.

The handler's environment is the host's environment, plus the
manifest's declared `env`, plus any allowlisted keys from the
request's `env`. We do not pass through the shim's full environment;
that would re-leak whatever the sandbox stripped or altered.

### 7. Logging

The dispatch goroutine logs each invocation through clown's existing
log routing (`pluginhost.OpenLog`, `cmd/clown/main.go:329-347`):

- Plugin id, event name, ordinal.
- Handler exit code.
- Wall-clock duration.
- Bytes in (stdin size) and bytes out (stdout + stderr size).

We do NOT log handler stdout or stderr. Those bytes belong to the
hook output and may contain user data, deny reasons with paths, or
similar content. They flow back to the provider through the shim
and stop there.

A `--verbose` flag (already present at the host level) adds a debug
log line per accepted connection with the peer pid for triage.

## Trust

Inherits RFC 0006: clown does not sandbox plugins; plugins own their
isolation. The bridge does not change that.

The peer-credential check (RFC 0007 §6) defends against a *different
user on the same host* spoofing a hook call. Against the same user,
the bridge offers no defense, and that is acceptable: a same-user
attacker has many other paths to influence the session.

The dispatch table is the only meaningful capability the bridge
exposes. A sandboxed process can invoke registered handlers and
nothing else. The argv shape — `(plugin-id, event-name, ordinal)`
plus the request body — gives no path to argument injection or shell
escape.

## Alternatives considered

1. **Loopback TCP instead of a unix socket.** Rejected. macOS
   `sandbox-exec` permits loopback easily; bwrap requires
   `--share-net` which most sandbox profiles will not want;
   Landlock 6.7+ can block TCP bind/connect. A unix socket is
   grantable in all three sandboxes via filesystem-path rules
   alone, making it the only portable choice across the supported
   sandbox technologies.
2. **fd-passing instead of a socket path.** Rejected. The
   provider's hook protocol does not include a way to pass an
   inherited fd into the spawned hook command. Any change here
   requires modifying the provider, which violates the
   "transparent to provider" non-goal.
3. **Mutating `.claude/settings.local.json`.** Rejected. Leaves
   on-disk state across sessions, conflicts with user-managed
   settings, and isn't pristine. The provider already accepts
   settings via `--settings <json>` (see `claude-code(1)`
   OPTIONS § Settings Options), so the cleaner path exists.
4. **Per-event explicit opt-in flag.** Rejected. Treating
   "registering a handler" as the opt-in is the simpler model:
   plugins that need the bridge declare hooks in `clown.json`,
   plugins that don't, don't. A separate `hookBridge: true` toggle
   would be redundant for every observed use case.
5. **A second daemon process analogous to circus's
   `llama-server`.** Rejected. The dispatch loop has no work outside
   a clown session and no value in outliving one. Folding it into
   `clown-plugin-host` gives us the lifecycle for free.
6. **Routing hook handlers through the existing
   `clown-stdio-bridge`.** Rejected. The stdio bridge is JSON-RPC
   over streamable-HTTP for long-lived MCP servers. Hooks are
   short-lived, stdin-bytes-in / stdout-bytes-out / exit-code-out.
   Forcing the latter through the former would require wrapping the
   handler in a JSON-RPC stub and would push complexity onto every
   plugin.

## Migration / consumer impact

Existing plugins are unaffected. The `hooks` block is additive in
`clown.json`; older clown binaries ignore it.

spinclass is the immediate consumer:

- Today, spinclass writes `.claude/settings.local.json` to
  `internal/sweatfile/apply.go:248-290` registering three hooks
  pointing at the `spinclass` binary.
- After this FDR lands, spinclass declares those three hooks in its
  `clown.json` and removes the `settings.local.json` write. The
  hook handler binary itself (`spinclass hook`) does not change
  shape: same stdin payload, same stdout response, same exit codes.

Plugins that wish to keep their existing
`.claude/settings.local.json`-based hook registration can do so;
clown does not intermediate that path.

## Testing

- **Unit, `internal/pluginhost`.** Dispatch table construction from
  a synthetic `clown.json`. Frame parsing (request and response,
  including malformed cases). Peer-cred check on platforms where
  it's available; build-tag-skip on others.
- **Unit, `cmd/clown-hook-shim`.** End-to-end against a
  fake-host listener that asserts header contents and replies with
  scripted exit codes; verify the shim exits matching the response,
  verify stderr/stdout routing.
- **Integration, repository-level.** A test that runs a synthetic
  provider stub which emits a hook payload to `clown-hook-shim`
  while a real `clown-plugin-host` listens on the socket. Verify
  the handler is invoked with the expected cwd, the expected env,
  and the expected stdin bytes; verify the response round-trips.
- **Integration, spinclass.** Migrate spinclass to the new
  mechanism in a feature branch and run its existing bats tests
  (`zz-tests_bats/hooks.bats`) under both unsandboxed clown and a
  hand-rolled bwrap profile that scopes the worktree.

## Open questions

Same as RFC 0007 §"Open questions", plus the implementation-side
question:

- **Helper as a separate binary?** The current design embeds the
  dispatch loop in `clown-plugin-host`. If we ever need to run the
  bridge without `clown-plugin-host` — for example, in a
  pluginhost-disabled mode where the user still wants spinclass-like
  hooks — we would split it out into `clown-hook-helper`. No
  immediate need; flagging for the record.

## See also

- RFC 0007 — wire-level specification this FDR implements.
- RFC 0002 — clown plugin protocol; manifest discovery pipeline.
- RFC 0006 — sandboxing posture inherited here.
- FDR 0002 — stdio MCP bridge; closest precedent.
- FDR 0003 — plugin-contributed prompt fragments; precedent for
  plugin contribution mechanisms aggregated by clown at session
  start.
- Issue #34 — the originating request.
