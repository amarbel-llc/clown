---
status: proposed
date: 2026-10-19
parent: spinclass FDR 0032 (Principals, handles, and provenance) — D7, D10, D17
companion: spinclass FDR 0035 (session confinement, spinclass#338)
issue: clown#244
promotion-criteria: spinclass's confinement check (FDR 0034 "Where the card root is real") passes against a worker launched with `--agent-scope`, on flic, with the worker's hooks, troupe chat, ringmaster wakes, model API calls, git fetch and nix build working from inside
---

# Agent scope confinement — launching `claude` in a transient systemd unit

## Problem Statement

spinclass FDR 0034 makes profile rights card-rooted, with enforcement behind a
switch that stays inert while its confinement check cannot pass. Today a
spawned worker's `claude` runs in the operator's login scope
(`/user.slice/user-1001.slice/session-N.scope`). It inherits the session
D-Bus. It is kept away from display sockets only by luck. It can write any file
the operator can, and it can write the operator's own pty slaves
(`/dev/pts/N`: 0620, same uid). piggy-agent(1) SCOPES requires that nothing an
agent runtime controls can draw on the operator's prompt surface, and assigns
arranging that to the session manager.

FDR 0032 D7 gives clown the *launching* half: `claude` and its whole tree go
in an **agent scope**, and clown and troupe's connection owner go in a
**frontend scope** outside it. D17 names the boundary: ProtectHome, the
worktree bind-mounted in, a dynamic uid, the tool sockets passed in.

This record is clown's half. spinclass FDR 0035 owns *what the unit must see*
and the confinement check. This record owns *how the unit is started* and what
clown guarantees about it.

## The two scopes

| | Frontend scope | Agent scope |
|---|---|---|
| What runs there | `clown` itself; the pty relay; the `systemd-run` client; the egress proxy; pluginhost servers declared frontend (ringmaster mcp, troupe mcp, `spinclass serve`); troupe's connection owner and `ringmaster monitor` | `claude` and everything it starts: tool calls, Bash, subagents, Claude Code hooks (PreToolUse/Stop/SessionEnd: clown-hook-allow, clown-hook-tee, clown-hook-toolcost, moxy's hook logger), in-unit MCP servers, in-unit relays |
| cgroup | wherever spinclass placed clown (its spawn path), never under `clown.slice/clown-agent.slice` | `/user.slice/user-<uid>.slice/user@<uid>.service/clown.slice/clown-agent.slice/<unit>.service` |
| Started by | spinclass (`sc spawn` / `spawn-session`) | clown, as a transient **service** (not a scope: a scope wraps an existing process and carries no exec sandboxing) |

piggy-agent runs as its own user service (`app.slice/piggy-agent.service` on
flic), outside both scopes. It authenticates callers by cgroup and refuses the
agent scope.

## Unit properties

Measured on flic (NixOS, systemd 258.3, user manager) with
`just explore explore-agent-unit-probe`. Every property is applied to a
`systemd-run --user` transient service. clown fails closed when any property
marked **required** cannot be applied: it refuses to launch, and there is no
unconfined fallback.

| Property | Value | Why | Status on flic |
|---|---|---|---|
| `PrivateUsers=` | `self` | Under the user manager, mount namespacing only works inside a user namespace, and that namespace is set up before the others. Maps the operator's uid to itself. | works |
| `PrivatePIDs=` | `yes` (**required**) | Same uid plus a shared pid ns lets `/proc/<operator pid>/root/...` walk out of any mount namespace (spinclass F10). | works: 4 pids visible, against 429 without |
| `PrivateNetwork=` | `yes` (**required**) | With the host netns, every loopback TCP service is reachable, and clown-stdio-bridge trusts loopback (ADR 0006). That includes *unconfined* sessions' moxy, whose `folio_write` reaches anywhere under `$HOME`. Also closes abstract unix sockets (Xwayland `@/tmp/.X11-unix/X<n>`) and cross-session MCP (spinclass F6, F12). Egress goes through the proxy below. | to be measured with the proxy |
| `ProtectHome=` | `tmpfs` (**required**) | Hides `/home`, `/root` and `/run/user` wholesale. Listed paths are re-exposed with `BindPaths=`. | works: real `$HOME` hidden and unwritable |
| `TemporaryFileSystem=` | `/dev/pts` (**required**) | Hides the operator's pty slaves. `PrivateDevices=yes` does **not** do this in a user unit (host ptys 0..22 stayed visible); this property does. | works: `/dev/pts` empty |
| `PrivateTmp=` | `yes` | Hides `/tmp/.X11-unix` and the operator's `/tmp`. | works |
| `ProtectSystem=` | `strict` | `/usr`, `/etc` and `/boot` become read-only; the worktree is re-allowed with `ReadWritePaths=`. | works |
| `NoNewPrivileges=` | `yes` | No setuid escalation from inside. | works |
| `BindPaths=` | the worktree; spec `binds_rw[]`; each spec `sockets[].path` as a single inode | What the unit must see (spinclass's list). | works for the worktree and the ssh-agent socket |
| `BindReadOnlyPaths=` | spec `binds_ro[]` (e.g. the madder store) | | |
| `InaccessiblePaths=` | spec `deny_paths[]` | Belt and braces: ProtectHome already hides `/run/user`. | |
| `UnsetEnvironment=` | every key of `systemctl --user show-environment` not on the allowlist below | The user manager injects its own environment into every unit, including `DBUS_SESSION_BUS_ADDRESS` and `XDG_RUNTIME_DIR` (observed). | works |
| `--slice=` | `clown-agent.slice` | Stable cgroup prefix for the piggy, posh and spinclass checks. | works |
| `--expand-environment=no` | | systemd otherwise expands `$` and `$$` in argv (observed: `$$` became `$`). | works |
| `--collect`, `--wait`, `--pipe` | | The unit is garbage-collected even on failure; clown waits for the exit code; stdio fds are passed in. | works |

**Not applied: `DynamicUser=`.** It is system-manager only (systemd.exec(5)
USER/GROUP IDENTITY), so the agent runs as the operator's uid. Separation
comes from the mount, pid, network and devpts namespaces, not from the uid.

A per-session uid needs the **system** manager, which means two things:

- a root-installed launcher (a system service, or `systemd-run --system`
  behind a polkit rule scoped to `clown-agent-*`) with `DynamicUser=yes`;
- an idmapped bind mount of the worktree (`mount_setattr(MOUNT_ATTR_IDMAP)`,
  which needs `CAP_SYS_ADMIN` in the init user namespace), so the dynamic uid
  can write operator-owned files.

This is deferred. FDR 0032 D17 names the dynamic uid as the end state; this
piece accepts the operator uid (coordinator decision, 2026-10-19).

## Egress

The unit has its own network namespace, which holds only `lo`. Outbound
traffic goes through a frontend-scope proxy:

1. clown runs an HTTP `CONNECT` proxy in the frontend, listening on a
   per-session unix socket. The socket is bound into the unit like every other
   passed socket.
2. In the unit, a relay listens on `127.0.0.1:<N>` in the unit's own netns and
   splices each connection to that unix socket.
3. The unit's environment sets `HTTPS_PROXY`, `HTTP_PROXY`, `https_proxy` and
   `http_proxy` to `http://127.0.0.1:<N>`. `NO_PROXY` is `localhost,127.0.0.1`.
4. The proxy enforces a `host:port` destination allowlist. The allowlist comes
   from the sweatfile, via the spec. Its default is: the model API, the forges
   (`code.linenisgreat.com`, `forge.starbrandshoes.com`), `github.com`,
   `cache.nixos.org`, and the krone nix cache. Refusals are logged in the
   frontend. **Open (clown#300):** the krone nix cache is not in
   `DefaultEgressAllow` (`internal/agentscope/egress.go`) today. That list
   also has the claude.ai and console/platform login hosts and more GitHub
   hosts than are named here. Whether to add the cache or correct this
   sentence is the operator's decision.
5. The proxy never reaches the host's loopback, whatever the allowlist says.
   A `CONNECT` to `localhost` (or a name under `.localhost`), `127.0.0.0/8`,
   `::1`, an unspecified address, or a name that resolves to any of these is
   refused with a 403 that names the destination and the reason. The proxy
   dials the addresses it checked, not the name again. A spec `egress_allow`
   entry naming the loopback refuses the launch, naming the entry; a loopback
   `ANTHROPIC_BASE_URL` is not added to the allowlist (logged). Unconfined
   launches are unchanged. Loopback services a unit needs are given to it as
   forwards (below), one chosen address each. Before this, a loopback
   `ANTHROPIC_BASE_URL` was appended to the allowlist, and any process in the
   unit could CONNECT to it (audit 2026-10-22, top 3). Not covered: the
   host's own non-loopback addresses (LAN, tailnet). An allowlisted name
   that resolves to one reaches services bound to `0.0.0.0`; the allowlist
   is the only guard there.

Per-tool status, to be measured by the launcher's test:

| Tool | Path | Expected |
|---|---|---|
| claude → model API | `HTTPS_PROXY` | honoured (Node fetch via claude's proxy support) |
| `git fetch` over https | `https_proxy` (libcurl) | honoured |
| `git fetch`/`push` over ssh | `GIT_SSH_COMMAND` with `-o ProxyCommand=clown egress-connect %h %p` | `clown egress-connect` dials the unix socket and issues `CONNECT`. `known_hosts` is bound read-only from the spec, or the check is not strict. |
| `nix build` substitution | done by nix-daemon (a system service, outside the unit) over `/nix/var/nix/daemon-socket/socket` | unaffected by the unit's netns |
| nix client-side fetches (flake inputs, `fetchTree`) | libcurl `https_proxy` | honoured |
| statsd (clown-hook-toolcost, in-unit clown-stdio-bridge; UDP 127.0.0.1:8125) | not proxyable | relayed over a passed unixgram socket (see "statsd relay" below) |

The sweatfile switch that enables confinement must not be able to produce a
confined unit without the proxy. clown refuses `--agent-scope` when the proxy
cannot start.

### statsd relay (clown#269)

statsd is fire-and-forget UDP, which neither the `CONNECT` proxy nor the TCP
forwards carry. Without a path, every confined session's hook-toolcost metrics
were silently lost.

1. The frontend binds a per-session **unixgram** socket,
   `$XDG_RUNTIME_DIR/clown/agent/<unit>/statsd.sock` (0600), with
   `SO_PASSCRED` set, and binds it into the unit as a single inode.
2. The in-unit `clown agent-relay` listens on UDP `127.0.0.1:8125` in the
   unit's netns (`--relay udp:127.0.0.1:8125=<sock>`) and sends each datagram,
   unchanged, to that socket.
3. The frontend reads each datagram with its `SCM_CREDENTIALS`. The kernel
   attaches the sender's pid (translated into the frontend's pid namespace),
   so the sender cannot forge it. A datagram whose sender is not in the unit's
   cgroup is dropped (the same `.../clown-agent.slice/<unit>` suffix rule as
   the egress proxy and the forwards). The rest go, unchanged, to the
   frontend's `STATSD_HOST:STATSD_PORT` (default `127.0.0.1:8125`).
4. Inside the unit `STATSD_HOST=127.0.0.1` and `STATSD_PORT=8125` are set, so
   the hooks need no change and the metric names are the same as in
   unconfined sessions.

Why this shape: a datagram socket keeps statsd's semantics (no connection
state, a lost frontend drops metrics instead of blocking a hook), and
`SO_PASSCRED` gives a per-datagram identity check that a connected stream
socket would only give per connection. The relay is the only UDP path out of
the unit, and it reaches exactly one destination.

The relay is skipped when the frontend has `CLOWN_DISABLE_STATSD=1`; the unit
then gets `CLOWN_DISABLE_STATSD=1` too, so senders inside do not try. A relay
that fails to start is reported on stderr and the unit runs without one:
metrics are not worth refusing a launch over. Capability: `statsd-relay`.

Coverage: Go unit tests in `internal/agentscope` (forwarding unchanged, drop
from a foreign cgroup, the cgroup check runs on the kernel-attached pid, both
ends end to end, the unit's binds/relay argv/env). `just verify-agent-scope`
cannot run from inside a confined unit (no user manager there), so the
host-level check that a real hook's metrics arrive in stats-me is left to a
frontend session.

## Environment

The unit starts from an empty environment, because systemd-run does not
forward the client's environment. The manager's environment is added on top,
and clown unsets it. clown then sets exactly the following.

**Allowlist (passed through from the frontend):**
- `PATH`, `TERM`, `COLORTERM`, `TERMINFO`, `TERMINFO_DIRS`, `LANG`, `TZ`,
  `TZDIR`, `LOCALE_ARCHIVE`, `USER`, `LOGNAME`, `SHELL`
- `SSL_CERT_FILE`, `SSL_CERT_DIR`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`
- `CLOWN_NAME`, `CLOWN_GROUP_ID`, `CLOWN_GROUP_DESCRIPTION`,
  `CLOWN_XMPP_VHOST`
- `MCP_CONNECTION_NONBLOCKING` (clown#296): the frontend sets it to `false`
  (unless the operator set it) so claude connects its MCP servers before the
  first model request. Not secret. Its companion
  `CLAUDE_CODE_MCP_STARTUP_WAIT_MS` already passes under the `CLAUDE_CODE_`
  prefix.

`CLOWN_BIN` is no longer passed (2026-10-22, round R). It used to be copied
from the frontend's env, and the frontend honors an inherited `CLOWN_BIN`. A
stray value from the spawner's environment then put the unit's producers
(`${CLOWN_BIN:-clown}`) and in-unit servers on an older clown build than the
one running the unit's shim and egress: round R saw two clown store paths of the
same `0.4.4` version string in one unit. Now clown sets it (below).

**Pass prefixes** (`passPrefixes` in `internal/agentscope/unit.go`). Every key
with one of these prefixes passes unless it is scrubbed. A prefix is a blanket
grant, so each one needs a reason here, and a new one needs a review of what
secrets could carry it:

| Prefix | Why it passes | Secret-bearing members, and how they are handled |
|---|---|---|
| `LC_` | locale; tools misrender without it | none |
| `NIX_` | the devshell's toolchain wiring (`NIX_CC`, `NIX_CFLAGS_COMPILE`, `NIX_LDFLAGS`, ...), `NIX_PATH`, `NIX_SSL_CERT_FILE`, `NIX_REMOTE`, `NIX_PROFILES` | `NIX_CONFIG`, `NIX_USER_CONF_FILES` and `NIX_CONF_DIR` are **scrubbed** (clown#272): on the dev hosts `NIX_CONFIG` carries `access-tokens`, and the other two redirect nix to a config file that can. |
| `ANTHROPIC_` | provider routing clown itself sets (`ANTHROPIC_BASE_URL`, model choice) | an `ANTHROPIC_API_KEY` passes. It is the agent's own model credential, which the agent needs. |
| `CLAUDE_CODE_` | claude's own settings (`CLAUDE_CODE_TMPDIR` is redirected separately) | a `CLAUDE_CODE_OAUTH_TOKEN` passes. Like `ANTHROPIC_API_KEY`, it is a model credential the agent then holds. |
| `TROUPE_` | the session's own XMPP identity (`TROUPE_XMPP_USER`) | `TROUPE_XMPP_PASSWORD_FILE` passes only as a path, bound read-only, and only when it names this session's own principal (clown#267). |

**nix configuration (clown#272).** The nix config keys are dropped, not
rewritten. A rewrite would have to recognise every secret-bearing nix setting
(`access-tokens`, `netrc-file`, an `!include` of a secrets file, ...) across
nix versions; a missed one leaks. Dropping is enough because the unit's builds
and substitution go through the host's nix-daemon (socket bound in), which
reads its own configuration. For the same reason the spec may not set these
keys in `env{}`, and may not bind the operator's `~/.config/nix` (or an
ancestor of it). The agent home under the worktree has no `nix.conf` unless
the agent writes one itself. `/etc/nix/nix.conf` stays visible read-only (it
is outside `/home` and readable by any local user anyway); a token placed
there is not protected by this boundary.

What a worker loses without the token: **client-side fetches of private flake
inputs** (`nix flake lock`/`update`, `fetchTree`/`builtins.fetchGit` of a
private forge or GitHub repo during evaluation) are unauthenticated, so they
fail or hit the anonymous rate limit. Already-locked inputs present in the
store, and everything the daemon substitutes or builds, are unaffected. A
worker that needs a private input fetched asks the operator, or uses the
per-session forge token spinclass mints into the worktree (spinclass FDR 0028)
where the fetcher supports it.

**From the spec:** `env{}`, verbatim (spinclass's `SPINCLASS_*` identity).

**Set by clown:**
- `HOME` and `XDG_{CONFIG,STATE,CACHE,DATA}_HOME` point under a per-session
  agent home (default `<worktree>/.tmp/agent-home`). Hooks and tools that write
  state then land inside the bound worktree instead of failing on the hidden
  real home.
- The proxy variables (see Egress).
- `CLOWN_BIN` is the shim's own binary, the clown the frontend runs the unit
  with. It overrides anything the frontend env, the provider binding or the
  spec's `env{}` carries, and in-unit servers get the same path.
- `STATSD_HOST`/`STATSD_PORT` (the in-unit statsd relay), or
  `CLOWN_DISABLE_STATSD=1` when the frontend has it.
- `SSH_AUTH_SOCK`, only when the spec passes the agent socket.
- `CLAUDE_CONFIG_DIR` (see the socket table).

**Passed but hollow:** `XDG_RUNTIME_DIR` and `XDG_{CONFIG,STATE,CACHE,DATA,LOG}_HOME`
keep the frontend's values, so job-platform tools inside resolve the same
journal and socket paths as the frontend. Under `ProtectHome=tmpfs` those
directories contain only what was bound in (the bus socket, posh and Wayland
are absent).

**Scrubbed** (never set, and unset if the manager had them):
- `DBUS_SESSION_BUS_ADDRESS`
- `DISPLAY`, `WAYLAND_DISPLAY`, `XAUTHORITY`
- `SSH_AUTH_SOCK`, unless passed
- credential paths other than `TROUPE_XMPP_PASSWORD_FILE`, which passes as
  a path to the session's own password file, bound read-only (see the
  `TROUPE_` row above)
- `CLOWN_SESSION_ID` (clown#136: never in the unit's own environment; claude
  receives it through its `--settings` env block instead, so hooks see the
  principal — clown#289)
- `NIX_CONFIG`, `NIX_USER_CONF_FILES`, `NIX_CONF_DIR` (clown#272, above)
- `CLOWN_UNIT` is scrubbed from every other source and then set last to the
  unit's own name (`--unit=`, `.service` included), so it is present iff the
  process runs inside an agent-scope unit. It is the marker spinclass's
  PreToolUse hook reads to tell it is confined; a hint for choosing
  defer-vs-ask, not a boundary, since an agent can set variables in its own
  processes

Every other key the manager injects is unset as well. On flic that set is:
`GIO_EXTRA_MODULES GTK_A11Y GTK_PATH HOME INFOPATH LANG LIBEXEC_PATH
LOCALE_ARCHIVE LOGNAME NIX_PATH NO_AT_BRIDGE PATH QTWEBKIT_PLUGIN_PATH SHELL
TERMINFO_DIRS TZDIR USER XDG_CONFIG_DIRS XDG_DATA_DIRS XDG_RUNTIME_DIR
LOCALE_ARCHIVE_2_27 DBUS_SESSION_BUS_ADDRESS`. Allowlisted keys are re-set
from the frontend's values.

## The terminal

The agent never receives the operator's tty. In agent-scope mode, clown
always runs the `internal/ptysuspend` relay, which today is opt-in via
`[attach] pty-suspend`:

1. clown allocates a fresh pty in the frontend and keeps the master.
2. The slave is handed to the `systemd-run --pipe` client as
   stdin/stdout/stderr, **without** becoming that client's controlling tty.
   Otherwise the kernel refuses the in-unit `TIOCSCTTY` (observed: `EPERM`
   when the slave was already some session's ctty).
3. Inside the unit, clown's exec shim (`clown agent-exec`) calls `setsid()`
   and `TIOCSCTTY(0)` on fd 0, then execs `claude`. Observed: the controlling
   tty is acquired (`tty_nr` non-zero), a master-side `TIOCSWINSZ` delivers
   `SIGWINCH` inside, and relayed input arrives.
4. clown relays operator input (terminal → master) and agent output
   (master → terminal), and propagates resizes.

Consequences:

- The unit's devpts is empty, so `ttyname()` fails inside with `ENODEV`. The
  agent's terminal is an fd, not a path. spinclass's pty probe (F8b) should
  compare `st_rdev` of `/proc/<unit pid>/fd/0` from outside against the
  operator's tty.
- **By design, the agent writes the terminal pane it runs in**, because that
  pane is its UI. The operator's prompt surface for piggy acts must therefore
  never be the pane that renders the agent's pty. Where piggy's askpass draws
  is posh's and piggy's lane.

## How the agent still reaches its sockets

Every socket below except piggy-agent's is created per session by clown,
under a frontend-owned directory: `$XDG_RUNTIME_DIR/clown/agent/<unit>/`,
except the monitor sockets (`$XDG_RUNTIME_DIR/clown/monitors/<principal>/`,
named before the unit is), the idle socket (`clown/idle/`) and the serve
socket (`clown/serve/<principal>/`, F12).
Each is bound into the unit as a single inode at the same path. The frontend
end checks `SO_PEERCRED` (or, for the statsd datagram socket,
`SCM_CREDENTIALS`) and `/proc/<pid>/cgroup` against the unit's cgroup. The
exception is the idle socket, which has no peer check
(`cmd/clown/selfclose.go`): any datagram only triggers this session's own
`spinclass close-self-run`.
Because of `PrivateNetwork`, loopback TCP is not offered to confined sessions
at all.

| Consumer (in the agent scope) | Needs | Mechanism |
|---|---|---|
| claude → frontend MCP servers (ringmaster mcp, troupe mcp, `spinclass serve`) | an MCP stream | Each runs in the frontend behind its clown-stdio-bridge: one child per bridge, shared by every connection (clown#286). ringmaster and troupe keep loopback TCP and are reached through a per-port PortForward socket. spinclass serve's bridge listens on the session's serve socket instead (F12, below). The in-unit agent-relay listens on the port claude's URL names. A plugin marks a server frontend in `clown.json` (`"scope": "frontend"`); clown-builtin-jobs' servers are frontend. |
| claude → agent-side MCP servers (moxy and other file-tool servers) | | Run **inside** the unit as claude's stdio children, so their file tools are confined too. A frontend moxy would be a ProtectHome bypass through `folio_write`. |
| plugin monitors (job wakes, chat) | wake lines on stdout | troupe's connection owner (`troupe receive`) and `ringmaster monitor` run in the frontend (FDR 0032 D7). The in-unit monitor (`clown agent-monitor`) reads their lines from a per-principal socket under `$XDG_RUNTIME_DIR/clown/monitors/<principal>/`, bound in as a single inode. The XMPP password file still enters the unit, read-only, for clown-hook-tee (next row). |
| clown-hook-tee (Stop/SessionEnd) | troupe send | `troupe muc send` inside the unit, with the session's own password file (bound read-only) over the forwarded XMPP port; no troupe socket or directory |
| clown-hook-toolcost | state dir, statsd | state under the redirected `XDG_STATE_HOME`; statsd through the statsd relay (Egress) |
| self-close idle poke (Stop) | one datagram to the frontend | The per-session unixgram `$XDG_RUNTIME_DIR/clown/idle/<principal>.sock`, bound in as a single inode (`Launch.IdleSocket`, clown#287). The in-unit `clown idle-notify <sock>` Stop hook sends one byte and always exits 0. The frontend listener runs `spinclass close-self-run` for its own principal, serialized (pokes during a run coalesce into one more), outside the unit; it reads the datagram only to tell a turn-start byte (UserPromptSubmit, which marks the session busy and runs nothing) from a poke. The datagram is not authenticated: anything in the unit running as the session's uid (the agent's Bash as well as the hooks) can send either byte. So a unit can only trigger its own session's runner, a no-op unless the session queued a close and spinclass allows it; and when a restart is queued, a poke runs `spinclass restart-preflight` (spinclass#419, below), whose verdict still gates it. A forged poke cannot start a restart spinclass did not authorize, but it can time an authorized one: it clears the turn-running mark, so the restart can proceed mid-turn (an accepted limit). A second frontend for the same key does not take over a live idle socket. Armed only for a claude launch inside a spinclass session with `spinclass` on PATH. |
| session restart (spinclass#419) | nothing inside the unit | `clown restart` runs from spinclass, outside the unit, and queues on the frontend's control socket `$XDG_RUNTIME_DIR/clown/control/<principal>.sock` (0600, dir 0700). It is not bound in, and the unit cannot reach it: `XDG_RUNTIME_DIR` is passed, but under `ProtectHome=tmpfs` the unit's `/run/user` is an empty tmpfs holding only the sockets bound in (the idle socket among them). `internal/agentscope/idle_test.go` pins the unit's systemd-run arguments for one launch (not checked live): ProtectHome=tmpfs is set and no BindPaths=/BindReadOnlyPaths= source is the control socket or a directory above it; that the control socket is then unreachable inside a running unit rests on systemd's tmpfs semantics, read from its documentation. The self-close plugin's UserPromptSubmit hook (`clown idle-notify --busy <sock>`) sends a turn-start byte on the idle socket, so a restart counting down is cancelled rather than stopping a running turn (forgeable, see the row above), except under a verdict whose `when` is `now`, where the turn is stopped on purpose. `clown restart --when now` reaches the same control socket, so it is equally out of the unit's reach; it only makes the frontend run the preflight at once instead of at the next idle poke (serialized with the idle runner), and the verdict, the profile gate and the countdown still decide. On a `restart` preflight verdict the frontend first dry-runs the relaunch's profile gate (a refusal leaves the session running), then stops the unit like any exit (if it has not exited 30 s later, `systemctl --user kill --signal=SIGKILL <unit>`, bounded at 10 s, only while that launch still owns the unit; until the relaunch, idle pokes run nothing and a second restart is refused), waits until the serve socket stops answering (`ListenServeSocket` refuses a live one), and, unless `spinclass restart-preflight --peek` (read-only, run immediately before the exec) reports the session closed (a close that has only started counts) or, as a fallback, its worktree is gone, re-execs `clown` from PATH (an unknown peek, e.g. a spinclass without `--peek`, relaunches on the worktree check alone) with `--resume <principal>`; the new frontend starts a new unit. See clown-restart(1). |
| clown-hook-allow (PreToolUse) | nothing | pure |
| moxy hook logger | `$XDG_LOG_HOME/moxy/hooks` | redirected under the agent home, unless spinclass binds the real dir rw |
| claude itself | `~/.claude` config, credentials, transcripts | `CLAUDE_CONFIG_DIR` points at a per-session dir. The operator's `.credentials.json` is bound in **read-only** (see "The operator's claude credential" below). The project's transcript dir is bound rw, so `clown resume` and freud keep working. `~/.claude/settings.json` is never writable from inside: a hook written there would run in unconfined sessions. |
| ssh / git signing | piggy-agent socket | spec `sockets[]`. Every act prompts the operator (piggy-agent(1) SCOPES: the boundary is the prompt surface and the home directory, not the socket). |
| nix | nix-daemon socket | `/nix/var/nix/daemon-socket/socket` (visible: outside `/home`, read-only under ProtectSystem, which does not stop `connect`) |

## Launcher interface (agreed with spinclass, FDR 0035)

**Opt-in.** Use `clown --agent-scope <spec.json>` or
`CLOWN_AGENT_SCOPE=<spec.json>`. When neither is set, behaviour is unchanged:
interactive clown and today's spawned workers work as they do now. Only the
claude provider on Linux with a systemd user manager supports it; other
providers and darwin refuse.

**Spec** (owned and versioned by spinclass):

```json
{
  "version": 1,
  "worktree": "/abs/worktree",
  "operator_home": "/home/<op>",
  "unit_name_hint": "spinclass-agent-<repo>-<branch>",
  "result_path": "$XDG_STATE_HOME/spinclass/units/<hash>.json",
  "binds_rw": ["..."],
  "binds_ro": ["..."],
  "sockets": [{"path": "/abs/sock", "purpose": "piggy-agent | troupe-ipc | ..."}],
  "deny_paths": ["$XDG_RUNTIME_DIR/bus", "..."],
  "egress_allow": ["api.anthropic.com:443", "..."],
  "env": {"SPINCLASS_SESSION": "..."}
}
```

clown refuses to start when:

- `version` is unknown;
- `worktree` is not absolute;
- a bind path equals `operator_home` or contains it.

**Result.** clown writes the result atomically (tmp + rename) to
`result_path` at these points (since 2026-10-22; audit "result file
unreliable on failure"):
- **Provider started.** The unit is `active`, its main pid has settled, and
  that pid has started the provider. clown reads that from
  `/proc/<pid>/comm`, which the shim, staying pid 1, sets to `clown-init`
  once the provider runs. A shim that is not pid 1 execs instead, and then
  its `cmdline` is no longer `<clown> agent-exec ...`. Until the exec, pid 1 is the shim, which can
  still refuse (its relays, its in-unit servers, a profile's unconfirmed
  exclusions), so "active" alone is not success. clown waits up to 2 minutes
  for this (`startupTimeout`). Raise it if a cold-store launch is reported
  as not started while its servers were still starting.
- **Exit.** When the unit exits after its provider started, the file is
  rewritten with `exit_code`. If the provider exited too fast for clown to
  see it start, the file has `exit_code` but no `cgroup` or `main_pid`.
  That holds only with evidence that something ran:
  - systemd's `Result` for the failed unit was `exit-code` or `signal`, or
  - clown saw the shim running, or
  - the exit code is 0.
  Otherwise, or when systemd's `Result` names a start failure (`exec`,
  `resources`, ...), the file gets `error`.
- **Failure.** On any refusal after the spec loads, the file is rewritten
  with `error` set. This covers staging, the frontend endpoints, a unit that
  failed, a provider that did not start in time, and a shim that refused
  before the exec. The shim's own exit codes are 2 (usage), 126 (refused)
  and 127 (provider not found); a unit exiting with one of them before its
  provider was seen is reported as a shim refusal. A provider that itself
  exits 2, 126 or 127 that fast is misreported the same way. A spec that
  fails to load or validate writes nothing: its `result_path` is not
  trusted.

```json
{
  "version": 1,
  "capabilities": ["agent-unit", "private-network", "egress-proxy", "frontend-mcp-sockets", "in-unit-moxy", "statsd-relay", "frontend-monitors"],
  "unit": "spinclass-agent-clown-cool-fir-1a2b.service",
  "principal": "<CLOWN_SESSION_ID: not in the claude subtree's environment (clown#136), so reported here>",
  "cgroup": "/user.slice/user-1001.slice/user@1001.service/clown.slice/clown-agent.slice/spinclass-agent-clown-cool-fir-1a2b.service",
  "invocation_id": "…",
  "uid": 1001,
  "pty": "/dev/pts/N",
  "main_pid": 12345,
  "exit_code": 0
}
```

Field notes:

- `unit` is authoritative. spinclass ignores its own hint once a result
  exists. clown escapes the hint and adds a unique suffix.
- `pty` is the host path of the slave that clown allocated.
- `main_pid` is in the frontend's pid namespace. It is the unit's lasting
  main process: the init of the unit's pid namespace, in the unit's cgroup.
  Since 2026-10-22 that is the shim itself, renamed `clown-init` once it has
  started the provider as its child (it no longer execs the provider), so
  `main_pid` is not claude's pid. spinclass uses it only as the root of the
  mount namespace and walks every pid in the cgroup for environ reads
  (spinclass/firm-mulberry, 2026-10-22). clown waits for that before
  writing the result (clown#273): right after the unit turns active, systemd
  briefly names the intermediate process that unshared the pid namespace,
  which then exits (observed ~60 ms on flic with
  `just explore explore-agent-scope-mainpid`). A unit whose main pid never
  settles within 30 s gets a result carrying the error instead.

## Keystroke observation (the clown#244 fact to verify)

**Finding: there is an observation point above `claude` in the frontend
scope.** It is the clown pty relay. It exists only when the relay is on, and
it sees bytes, not prompts.

Evidence:

- `internal/ptysuspend/ptysuspend_linux.go:79-98` allocates the inner pty and
  starts the child on its slave. `relayInput` (`:177-201`) reads every
  operator byte in clown before writing it to the inner master. clown is the
  parent, outside the child's tree.
- On the default path, claude inherits clown's stdio and no clown code sits in
  the input path. `pty-suspend` is off by default
  (`internal/clownfile/clownfile.go:143`). The pty master is held by
  posh-server (the default multiplexer) or by the terminal emulator.
  posh-server also observes every keystroke above claude.
- In agent-scope mode the relay is mandatory (the agent never gets the
  operator's tty), so clown in the frontend scope always observes the input
  stream.
- The probe showed this path end to end: bytes written on the frontend master
  arrived at the in-unit reader (`read=typed-by-operator`).

Limits: the relay sees raw keystrokes, but `claude`'s TUI does line editing,
history recall and paste handling. In general, the submitted prompt cannot be
recovered from the byte stream.

The practical D10 design is therefore the one FDR 0032 anticipates. The
`UserPromptSubmit` hook (agent scope) hands the payload to a frontend-scope
signer, which authenticates the caller by cgroup. The frontend relay's byte
log can serve as corroborating evidence. clown registers no `UserPromptSubmit`
hook today, and the signer path is not built in this piece.

## Is `--tent` the realization?

No. tent (FDR 0007, clown#83) runs claude in a rootless podman container. It
is the wrong realization here because:

- Its cgroup is podman's (libpod/conmon scopes), not a stable systemd unit
  under a known slice. piggy, posh and spinclass cannot match it by cgroup
  prefix, and there is no `systemctl --user show` handle.
- It needs a container image and the userns `keep-id` mapping, and it is a
  shelved experiment (FDR 0017).
- The transient user service gets every property this record needs (mount,
  pid, network and devpts namespaces, env scrubbing, cgroup identity) from the
  host's `/nix/store`, with no image.

What carries over is FDR 0017's model. The agent unit is a second **locus**,
and it translates the same five resources:

| Resource | How the agent unit translates it |
|---|---|
| argv | systemd-run prefix plus the exec shim |
| environment | explicit allowlist; manager env unset |
| files | `BindPaths=` from the spec, instead of tent's static allowlist |
| network services | passed unix sockets plus the egress proxy, instead of loopback URL rewriting |
| tty | the ptysuspend relay, with an in-unit `TIOCSCTTY` |

## Implementation status

Built (`internal/agentscope`, `cmd/clown/agentscope.go`):

- `clown --agent-scope <spec>` / `CLOWN_AGENT_SCOPE` for the claude provider,
  refused with `--tent`, `--naked` and other providers; carried across the
  `[attach]` re-exec.
- The unit with every property above except `PrivateNetwork` (host netns,
  so **not yet a confined unit**: the egress proxy and the frontend MCP
  sockets come next, and the confinement switch must not be offered before
  them). *Superseded: `PrivateNetwork=yes` has been unconditional since
  2026-10-19 (below).*
- `clown agent-exec`, the in-unit shim (`setsid` + `TIOCSCTTY(0)`), and the
  ptysuspend relay with `DetachSlave`/`NoEscape`.
- clown's plumbing binds for the base launcher: a per-session
  `CLAUDE_CONFIG_DIR` under `<worktree>/.tmp/agent-home/.claude` seeded from
  the operator's `.claude.json` and `settings.json`, the operator's
  credentials file and this project's transcript dir bound over it, clown
  hook state, moxy's hook log dir, the session's own troupe password file
  read-only (for clown-hook-tee's `troupe muc send`, with the forwarded XMPP
  port), the frontend monitor sockets, and the staging root read-only when it
  is outside the worktree.
- No ringmaster or troupe directory is bound (2026-10-22). The runtime
  `troupe/` dir (every session's `accounts/<uuid>.pass` and socket), the
  state `troupe/` dir (every account's cursors), and the ringmaster runtime
  and state dirs (every channel's journals, nudge sockets and presence) used
  to be bound read-write whole. A worker could read, replace or delete any
  session's XMPP password, and read or append to any session's job journal.
  The troupe dirs went first (ce3cc4a; troupe/rich-elder's survey showed the
  in-unit verbs need only the named password file). The ringmaster dirs went
  with the frontend monitors below, because the in-unit `ringmaster monitor`
  bound its socket in the shared runtime `jobs/` dir and its reaper appended
  to every channel's journal (ringmaster/bright-holly's survey); nothing
  narrower let it run inside.
- **Frontend monitors (built 2026-10-22).** Under `--agent-scope` clown
  rewrites the job-platform plugin's monitors (`moveMonitorsToFrontend`):
  claude runs `clown agent-monitor <socket>` inside the unit, and the
  frontend runs the original commands (`ringmaster monitor --session <key>`,
  and `troupe receive` under xmpp-native, or the legacy `troupe agent`) with
  its own env and arguments, as `agentscope.MonitorRelay`s.
  - Each relay serves its monitor's stdout lines on
    `$XDG_RUNTIME_DIR/clown/monitors/<principal>/<name>.sock`, bound into the
    unit as a single inode. Callers must be in the unit's cgroup.
  - The stream is one-way: what a reader sends is read only to notice it has
    gone and is discarded. Every line the relay serves, including its own
    `[clown] monitor <name> unavailable` notice, is frontend-written. This
    covers the socket, not claude's pipe: inside the unit a process of the
    same uid can write to `clown agent-monitor`'s stdout, as it could to an
    in-unit monitor's before, or connect first and take the queued lines.
  - A monitor starts with its first reader, not with the relay: ringmaster's
    monitor acks what it prints, so lines printed before claude's
    `agent-monitor` connects would be acked and then exist only in the relay.
  - Lines produced while no reader is connected are queued (1024 lines,
    oldest dropped) and replayed to the next reader. The bound, the drop
    count and, on close, the undelivered count are in the launch log.
  - The reader forwards complete lines only, so a relay write cut short and
    replayed whole is never glued to a fragment.
  - A monitor that exits is restarted with capped backoff (1s to 30s). After
    5 consecutive runs shorter than 10s, the relay emits the unavailable notice
    once and keeps retrying. `ringmaster monitor` replays unacked records when
    it starts, so a wake produced while it was down arrives after the restart
    (`zz-tests_bats/agent_monitor.bats`, against the real ringmaster). Lines
    acked by ringmaster and still queued here are lost when the relay closes:
    when the frontend dies, when the unit fails to start after a reader
    connected, or in the short gap between claude exiting and the frontend
    stopping the relay. Unconfined, a wake that arrives while claude is down
    stays unacked and replays on resume; here it can be lost in those gaps.
  - Monitors are killed with the relay and, through `Pdeathsig`, with the
    frontend process even when it dies without unwinding (SIGKILL, a hangup).
  - **Authority:** a frontend monitor is the same monitor every unconfined
    session runs today, with the same reach. ringmaster's reaper is
    host-wide. The change is that a worker no longer gets that reach.
    Turning the reaper off for these monitors (`RINGMASTER_DISABLE_LIVENESS`)
    is ringmaster's call.
  - Other plugins' monitors still run inside the unit.
- systemd-run and systemctl come from `PATH` (overridable with
  `CLOWN_SYSTEMD_RUN`/`CLOWN_SYSTEMCTL`): systemd-run serializes properties
  client-side, so it must match the running manager.
- `clown agent-scope-run --spec <path> -- argv` runs any command through the
  same launcher. It is a raw runner, not a session launcher: it neither checks
  nor writes the confinement record (clown#318), so a claude conversation it
  starts is unrecorded and a later `clown resume` of it runs unconfined.
  Confined sessions it starts are outside #318. A session launch records the
  spec's sha256 and refuses a later launch whose spec bytes differ, refuses a
  spec file inside the spec's own worktree or `binds_rw`, and `Validate`
  refuses a worktree or `binds_rw` entry covering or inside
  `$XDG_STATE_HOME/clown`, where the records live. `just verify-agent-scope` (bats tag `agent_scope`, host-only)
  uses it to check, against real units: home, bus, display, posh, ptys and
  other pids unreachable from inside; the result's unit and cgroup; the
  frontend and piggy-agent outside the cgroup; the unit gone after exit; the
  pty path; refusal of a spec that exposes the operator home; the operator's
  claude credential readable but not writable; the egress proxy refusing the
  host loopback, and a loopback `egress_allow` entry refusing the launch.

End to end with real claude (`just explore explore-agent-scope-claude`, host
netns, 2026-10-19): `clown --agent-scope <spec> -- -p ...` from a scratch
worktree ran claude inside the unit; its Read tool call worked, the answer was
correct, exit 0; login worked through the bound credentials file (then
read-write; read-only since 2026-10-22, not yet re-run); the
transcript landed in the operator's `~/.claude/projects/<slug>` through the
bind; the Stop hooks (clown-hook-tee, clown-hook-toolcost) ran inside with
`hookErrors: []` and wrote their state into the operator's
`$XDG_STATE_HOME/clown/hook-*` through the binds. Two fixes came out of it:
temp-dir variables outside the worktree are redirected to `<worktree>/.tmp`
(claude failed with `EROFS` on an inherited `CLAUDE_CODE_TMPDIR`), and
`XDG_STATE_HOME`/`XDG_LOG_HOME` are always set to the operator paths the
binds use (hook state otherwise landed in the agent home).

`result_path` inside the worktree or a `binds_rw` entry is refused: the unit
could forge a result it can write.

Since then (2026-10-19, all on flic):

- `PrivateNetwork=yes` is unconditional, and the egress proxy is built as
  described under Egress.
- **Forwards replace loopback URLs.** Claude's MCP URLs stay
  `127.0.0.1:<port>`. The shim's `clown agent-relay` listens on the same
  port inside the unit's netns and splices each connection to a per-session
  frontend `PortForward` socket. That socket accepts only callers in the
  unit's cgroup and dials exactly that one host port.
- **What is forwarded.** Only frontend-class servers (clown.json
  `"scope": "frontend"`; clown-builtin-jobs' ringmaster and troupe), plus
  troupe's XMPP server (`TROUPE_XMPP_HOST:PORT`, default 127.0.0.1:5222)
  when the session has its own xmpp-native credential. Nothing else on the
  host's loopback is reachable.
- **Agent-side servers are not started under `--agent-scope`** (moxy, the
  juggler plugin, anything without `scope`), so confined mode lacks
  folio/grit/rg. Running them INSIDE the unit is the next slice, and confined
  mode is not offered to real workers until it lands (coordinator decision,
  2026-10-19). `--mcp-collapse` is refused with `--agent-scope`.
- **`clown agent-scope-capabilities`** prints
  `{"version":1,"capabilities":[...]}`, and every result echoes the same
  list. The list on that day was `agent-unit`, `private-network`,
  `egress-proxy` and `frontend-mcp-sockets`. Capabilities are added only
  with their code. The current list is `Capabilities` in
  `internal/agentscope/spec.go`: those four plus `in-unit-moxy`,
  `statsd-relay`, `frontend-monitors` and `frontend-serve-socket`.
- **Evidence (that day).** `just verify-agent-scope` passed 11/11 (18/18 at
  2026-10-24), including a
  forwarded loopback listener being reachable while unforwarded ones and a
  host abstract socket stay unreachable. Real claude -p inside the unit
  called ringmaster `job_read` and troupe `chat_list` over the forwards
  (`MCP-OK 7`). hook-tee's post arrived in the group channel over the 5222
  forward; without the forward it was silently lost.
- **XMPP credential.** It is passed into the unit only when its file names
  `TROUPE_XMPP_USER`'s principal (clown#267, fixed in the same series).

**In-unit servers (same day).** Agent-side clown.json servers (moxy, madder,
dodder, just-us, the juggler plugin, anything without `scope`) are no longer
dropped. The frontend reserves each one at `127.0.0.1:2xxxx` inside the
unit's netns (`Host.Reserve`) and compiles that address into claude's
manifest. The in-unit `clown agent-relay --serve` starts the real server
inside the unit with pluginhost's own handshake and healthcheck, then relays
the reserved port to the address the server announced. The servers' stderr
goes to `$HOME/.cache/clown/agent-relay.log` in the agent home.

A profile's saved cheap-context selection covers in-unit servers too
(clown#290). The frontend has no catalog for a server it never started, so an
in-unit server is kept or dropped by name against `context_servers`. A dropped
one is never started by the relay. A kept one carries the profile's saved
`context_excluded` names verbatim in the `--serve` file. Once the server is
healthy, the relay fetches its tool catalog, resolves the saved names against
it (exactly, or by claude's form of the rendered name: moxy renders
`grit.branch`, claude shows `grit_branch`; clown#291), and POSTs the rendered
names to `/clown/exclude-tools`. It then fetches the catalog again and checks
that none of the pushed names is still listed (the readback). Under a profile
nothing about this is best-effort, here or on the frontend: a catalog that
cannot be fetched, saved names that match nothing, a failed push, or a
readback that still lists an excluded tool refuses the launch. The relay
exits before its ready byte, so the confined launch fails, and the frontend
returns before starting the provider. Launches without a saved selection keep
the old best-effort push. The readbacks are recorded as JSON: the frontend's
servers in `$XDG_STATE_HOME/clown/readback/<principal>.json`, the in-unit
servers in `$HOME/.cache/clown/exclusion-readback.json` in the agent home.

Unmatched saved names (rule of 2026-10-22, audit top 5). A saved exclusion
that matches no live tool under any rendering is harmless: the tool is not
there. It is recorded as `unmatched` and the launch continues. Under a
profile the launch is refused, and the record lists the entry under
`ambiguous` with the candidate tools and the server, when:
- an unmatched saved name's loose form (case folded, claude's `mcp__<plugin>__`
  prefix stripped, `.`/`-`/`_` as one separator) equals a live tool's; the
  profile meant that tool, which would otherwise stay visible;
- a saved name matches more than one live tool (the claude-safe fold can
  collide).

In nikulin round P, 89 of canary-grit's 152 moxy names were unmatched. These
are taken to be exactly the tools of the moxins the in-unit moxyfile
disables: smith 34, chrest 21, freud 12, piers 7, car 5, stats-me 5, gmail 2,
calendar 1, gws 1, prison 1. That count is an inference from the code; it
stays one until a confined round's readback record is quoted. The denylist
itself still admits a tool a server adds later (principle 1). Allowlist
mode belongs to the dynamic-profiles design (clown#266).

What the readback proves, and what it does not:
- It checks LISTING only; the record says so (`"checks": "listing"`). moxy
  refuses an excluded tool at call time too. `clown-stdio-bridge` used to only
  filter `tools/list`, so for a bridged server (spinclass, just-us) a tool
  absent from the listing was still callable by name. The bridge now answers
  a `tools/call` naming an excluded tool itself, with moxy's
  "excluded for this session" tool error, and never forwards it. Until then,
  spinclass tools a profile excluded (spawn-session, merge-this-session,
  close-child-session, the handle grants, resurrect, in canary-grit) were
  hidden but callable by name. Since 2026-10-22 (audit top 4) the bridge
  compacts each body to one line before forwarding, so a tools/call cannot
  ride as an inner line of a harmless request to a line-reading child. It
  also matches keys case-insensitively, as a Go child's decoder does, and
  refuses as ambiguous (JSON-RPC -32600) a request that repeats a key in any
  case, e.g. `{"name":"kept","Name":"excluded"}`. This is still policy, not
  a boundary: in a unit, the bridge belongs to the worker, and its
  `/clown/exclude-tools` is unauthenticated loopback (ADR 0006's premise does
  not hold there; not reopened yet). The exception is spinclass serve's
  frontend bridge on the serve socket. Its call refusal runs in the frontend,
  and its control routes refuse the unit (F12, clown#301), so there the
  exclusions hold against the worker.
- In-unit servers, and their `/clown/exclude-tools` route, run inside the
  worker's unit on its loopback. A worker able to reach that route could
  change its own exclusions. The in-unit record is written by that same unit.
  So in-unit exclusions are policy, not a boundary: only a whole-server drop,
  or a frontend enforcer, is.
- **Recipe scoping is unenforced.** No component in clown's path inspects
  `run_recipe`'s arguments (just-us was not read). A confined worker with
  just-us can run any recipe in its justfile. The design places recipe
  enforcement in a clown-run frontend moxy proxy (moxy#487), which does not
  exist yet.

Before this, the
replay saw only frontend-started servers and silently dropped every in-unit
server, so a confined worker under any builtin profile had no moxy and no
just-us.

moxy inside the unit reads a global moxyfile that clown binds read-only over
`$HOME/.config/moxy/moxyfile` (`agentscope.InUnitMoxyfile`). It sets an
`allow-tools` allowlist, which moxy intersects down the hierarchy, so a
worktree moxyfile can only narrow it, plus `disable-moxins`.

The madder store is bound **read-write** (coordinator decision,
2026-10-19). moxy writes large tool outputs there, and a confined worker's
outputs must reach the frontend's blob store. moxy failed to start in the
unit when no store was visible at all.

Evidence on flic, real claude -p plus the operator's moxy plugin, with the
madder store bound as spinclass binds it:
- moxy started inside the unit (its pid in the unit's pid namespace) and
  logged `allow-tools active`.
- folio write inside the worktree succeeded (the file appeared on the host).
- folio write to `$HOME` was refused, and the file does not exist on the
  host. The refusal came from moxy's permission layer; the mount boundary
  that would also have stopped it is proven separately by
  `verify-agent-scope`.
- folio read worked, no smith tool existed, and the claude.ai Krone
  connector's `issue-filer_file` was listed. That connector reached its
  origin through the egress proxy; it was not called.

**statsd relay (2026-10-20, clown#269).** Built as described under "statsd
relay"; capability `statsd-relay`. Covered by Go unit tests only so far.

**Frontend monitors (2026-10-22).** Built as described in the launcher
bullets above. Covered by Go unit tests (relay delivery, queue replay, input
ignored, restart and unavailable notice, queue bound, caller check, kill and
replay), a nix-lane bats test against the real `ringmaster monitor`
(including killing it while a wake is produced), and `verify-agent-scope`
(no ringmaster or troupe state inside a unit). Not yet verified in a live
confined session.

## What a confined worker loses

By tool, for a brief to state plainly:

| Tool | Status inside the agent unit | Why |
|---|---|---|
| moxy: folio, grit, rg, jq, man, chix, env, batch/async, status, restart | **available** | They act on what the unit sees. git push/fetch go through the egress proxy; nix builds go through the daemon. |
| moxy: smith | absent | It writes to forges. A confined worker files issues through the issue-filer connector only. |
| moxy: chrest | absent | It drives the operator's browser, which is the prompt surface. |
| moxy: calendar, car, gmail, gws, piers, prison | absent | Credentials live under the hidden home. |
| moxy: freud | absent | It reads operator transcripts. |
| moxy: stats-me | absent | It needs a host-loopback service the unit cannot reach. |
| circus read-only cloud moxins (hetzner, cloudflare, nfsn) | absent for now | Not on the allowlist. They could be added as read-only after review. |
| ringmaster, troupe MCP (jobs, chat) | available | Forwarded from the frontend. |
| `ringmaster`/`troupe` CLI from Bash, and in-unit job producers (e.g. moxy's async jobs) | **not supported in confinement** | Decision (a), coordinator, 2026-10-22: confined workers use the frontend MCP job tools (ringmaster, troupe). In-unit moxy async jobs and `ringmaster`/`troupe` CLI use from Bash are not supported: no ringmaster or troupe directory is bound, so a journal write from inside the unit is expected to fail (inferred from the binds; not observed). An in-unit moxy also gets no `CLOWN_SESSION_ID` (pre-existing), so before this it journaled to a channel no monitor watched. Revisit (bind the session's own channel dir, pass the key to in-unit servers) only if a worker needs moxy async in confinement and the operator asks. |
| spinclass serve | available (`"scope":"frontend"`), over the session's serve socket (F12) | Its bridge admits only the unit and the frontend; it has no TCP listener in confined mode. |
| claude.ai connectors (e.g. Krone's issue-filer) | available | Through the egress proxy (`mcp-proxy.anthropic.com`, `mcp.starbrandshoes.com` on the default allowlist). |
| Bash network tools | only allowlisted `host:port` over the proxy | `HTTPS_PROXY`/`HTTP_PROXY` are set. Tools that ignore proxy env get no network. |
| local juggler model (loopback `ANTHROPIC_BASE_URL`) | unavailable | The unit's loopback is its own. |
| statsd metrics (clown-hook-toolcost, in-unit clown-stdio-bridge) | relayed | UDP `127.0.0.1:8125` inside goes over the statsd relay to the frontend's statsd. Dropped only when the frontend sets `CLOWN_DISABLE_STATSD=1` or the relay failed to start. |
| moxy and plugin dynamic system-prompt fragments | not fetched | The frontend cannot reach in-unit servers. Their static fragments still apply. | Plugin monitors inside the unit are untested,
because `-p` mode does not start them. A loopback `ANTHROPIC_BASE_URL` (a
local juggler) is not reachable from the unit.

## A per-session serve socket (F12)

**Status: BUILT (2026-10-24), option (y) with one shared child.** Unit tests
pass; the host bats run (`just verify-agent-scope`) has **not** been run for
it, and no confined spinclass session has used it yet.

What was built:
- `clown-stdio-bridge --listen-unix <sock> --peer-unit <unit>` serves its
  usual streamable-HTTP on the unix socket instead of loopback TCP. Its
  listener (`agentscope.ListenServeSocket`) creates the socket 0600 in a
  0700 directory, `$XDG_RUNTIME_DIR/clown/serve/<principal>/<server>.sock`.
  On every accepted connection it checks the peer (SO_PEERCRED, then
  `/proc/<pid>/cgroup` against `.../clown-agent.slice/<unit>`). It refuses
  and logs anyone else: the log names the caller's pid and cgroup. When the
  pid itself cannot be read, the caller is refused (fail closed) and the log
  says its cgroup is unknown.
- **The frontend is admitted by pid.** The bridge's parent clown health-checks
  the server, fetches its prompt fragment and pushes cheap-context
  exclusions over the same socket. It is accepted by pid rather than cgroup.
  The check is against `getppid()` at each accept, never pid 1, so once the
  parent dies its old pid no longer matches. The serve bridge also polls its
  parent and shuts down once it is gone, so no reused pid can reach the
  child. No other host process is admitted.
- **A live socket is not taken over.** The socket is keyed on the session's
  principal, which a resume or a pinned `CLOWN_SESSION_ID` can repeat. A
  second bridge for the same principal first probes the path. If a listener
  answers, it refuses ("serve socket … is live") instead of unlinking the
  first session's socket; only a stale socket file is replaced. Two launches
  of one principal racing in the same instant are not guarded against.
- **The control routes are the frontend's only (clown#301).** On the serve
  socket the bridge tags each accepted connection with its peer class
  (`agentscope.ServeConn.Frontend`).
  - `/clown/system-prompt` and `/clown/exclude-tools` answer 403 to a caller
    in the unit, or to any request without a class (fail closed). So the
    unit cannot read or clear the profile's tool exclusions for this server,
    or fetch the fragment.
  - `/mcp` and `/healthz` stay open to the unit.
  - The frontend still pushes and reads back exclusions and fetches the
    fragment.
  - The loopback-TCP bridges (ringmaster, troupe, and every unconfined
    session) are unchanged: their control routes are loopback-only, which is
    not a boundary.
- The handshake gains a unix form (`1|1|unix|<abs path>|streamable-http`).
  pluginhost dials the socket for its own requests (`Handshake.Client`,
  `BaseURL`). The compiled manifest names the in-unit relay's port
  (`ManagedServer.RouteManifestThrough`, `127.0.0.1:19000`,
  `agentscope.ServePortBase`).
- Only spinclass's bridged frontend server is routed this way
  (`routeServeSockets` in cmd/clown). ringmaster, troupe and every other
  bridge user keep loopback TCP and their forwards, unchanged.
- clown names the unit before pluginhost starts the frontend servers
  (`flags.agentScopeUnit`), so the bridge admits the unit from claude's
  first connection.
- The socket is bound into the unit as a single inode and relayed from the
  port (`Launch.Serve`). The result gains `serve_socket`, and the
  capability list gains `frontend-serve-socket`. SpecVersion is unchanged.

**Child lifetime: one shared child (changed plan).** An intermediate plan was
one child per accepted connection. It is **withdrawn**: claude speaks
streamable-HTTP to the bridge, and one MCP session spans several HTTP
connections (POSTs and the SSE GET), so a child per connection would split a
session. The bridge keeps its one child for all connections, as on TCP.

**Known limit (clown#286): within-session cross-talk.** Every connection
shares the one child. The bridge does not remap JSON-RPC ids. A request
whose id is already in flight is refused ("duplicate JSON-RPC id"), and every
server-to-client message is broadcast to every SSE subscriber. With F12 those
connections can only come from the session's own unit and its frontend, so
another session cannot reach the child; a second client inside the same unit
still shares it. The alternative, a child per MCP session inside the bridge
(keyed on initialize / `Mcp-Session-Id`), is recorded on clown#286 and not
built.

**Not verified:**
- the host bats: MCP over the socket from inside a unit, a host process
  refused on the socket, and no TCP listener for the confined serve;
- spinclass serve itself over the socket (prompt fragment and tools);
- a cheap-context exclusion push over the socket in a real launch;
- spinclass reading `serve_socket` and requiring the capability.

The proposal it implements, for the record:

**Problem.** spinclass serve speaks MCP over stdio only (spinclass
`cmd/spinclass/commands_mcp.go`, `transport.NewStdio`, per
spinclass/firm-mulberry). The TCP listener on the host loopback is
clown-stdio-bridge wrapping it. Any same-uid host process can connect to
that port and act as the session, and the bridge cannot tell the session's
own worker from anyone else (spinclass F12). The proxy-side reach is closed:
the egress proxy refuses the loopback.

**Design (option (y)).** The check runs in the frontend, in the bridge
(principle 7); nothing in the unit is trusted.
1. Under `--agent-scope`, the frontend runs the serve's clown-stdio-bridge
   with `--listen-unix $XDG_RUNTIME_DIR/clown/serve/<principal>/spinclass.sock`
   (0600; directory 0700) and `--peer-unit <unit>`, instead of a TCP
   listener.
2. For every accepted connection, the bridge runs agentscope's cgroup peer
   check, the same one PortForward and the egress proxy use, and only then
   splices the connection into the child's stdio. A caller outside
   `.../clown-agent.slice/<unit>` is refused, with its cgroup logged.
3. clown binds that one socket inode into the unit, like the monitor
   sockets. The in-unit agent-relay listens on the `127.0.0.1:<port>` that
   claude's URL already names and splices to the socket. Nothing changes for
   claude, and a confined session's serve has no TCP listener.
4. clown computes the unit name before pluginhost starts the frontend
   servers (a small reorder). The check needs only the unit's cgroup, not
   spinclass's record, so claude's first MCP connection at startup is
   accepted: no race.
5. The result gains `serve_socket`, which spinclass's F12 probe already
   checks. clown advertises the capability `frontend-serve-socket`, and
   spinclass adds it to its RequiredCapabilities.

**Changes when built.**
- clown-stdio-bridge: `--listen-unix` and `--peer-unit`.
- pluginhost: start a frontend-scoped stdio server that way under
  `--agent-scope`; add a unix handshake form (RFC 0002).
- agentscope: the reorder, the bind, the relay entry, `serve_socket`, and
  the capability.
- Unconfined sessions are unchanged: TCP loopback, same uid, not a
  boundary.
- **ADR 0006 ("clown-stdio-bridge trusts loopback clients") is superseded
  for this mode.** A confined session's serve bridge trusts only its unit's
  cgroup.

**Retracted: option (b).** An earlier draft had spinclass serve listen on
the socket itself and read the unit name lazily from spinclass's unit
record. That draft assumed the serve had its own TCP listener. It has none,
so option (b) would need a new socket transport in spinclass or go-mcp, and
it would race claude's first connection against the record write. Do not
build (b).

**Tests when built.**
- Unit test: the bridge refuses a foreign cgroup.
- Host bats: MCP from inside the unit works over the socket; a host process
  on the socket is refused; nothing listens on TCP for the confined serve.

## The operator's claude credential

**A confined worker can READ the operator's Claude credential.** The
operator's `~/.claude/.credentials.json` (the claude.ai OAuth access and
refresh tokens) is bound into every unit. Treat it as exposed to anything
run in a unit: the worker's Bash, its tools, and anything they fetch. The
model API and claude.ai hosts are on the default egress allowlist, so by
default a worker that reads it can also use it.

Since 2026-10-22 the bind is read-only (`stageAgentHome`; before, it was
read-write, so a worker could overwrite or truncate the operator's
credentials; audit 2026-10-22, top 2). A spec `binds_rw` (or worktree)
that would expose the file writable is refused (`Spec.Validate`). The host
test checks that a write fails with "Read-only file system".

- Untested consequence, to be watched in the next live round: claude is
  expected to write refreshed tokens back to this file (inferred from the
  file holding a refresh token; not observed here). Inside a unit any such
  write now fails. How claude reacts is not observed: it may keep the refreshed token
  in memory, or fail auth once the access token expires. If the provider
  rotates refresh tokens on use, a refresh from inside a unit could also
  leave the operator's stored refresh token stale. That is a theory, not
  verified. A round that runs past an access-token expiry would show it.

**Does claude in the unit need the file?** It needs *a* credential. Two
alternatives are known from Claude Code's documented environment variables,
not tested in a unit here:
- a long-lived `CLAUDE_CODE_OAUTH_TOKEN` minted with `claude setup-token`;
- an `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN`.

Either one is still a secret held in the unit. Whether either is narrower
or separately revocable than the operator's login is not verified here.

**Can the frontend inject it, so that the unit never holds it (principle
7)?** Not through the egress proxy as built. The proxy is a CONNECT tunnel:
TLS ends inside the unit, so the proxy cannot add a header without
intercepting TLS, which would need a CA the unit trusts. A feasible route
is a frontend HTTP reverse proxy for the model API:
- `ANTHROPIC_BASE_URL` points at a plain-HTTP listener reached through a
  forward socket in the unit;
- the frontend replaces a placeholder bearer with the real token and
  forwards over TLS to `api.anthropic.com`;
- the frontend also owns the refresh.

Open points to size before building it:
- claude.ai connectors (`mcp-proxy.anthropic.com`) authenticate with the
  same OAuth token and would need the same injection, or be lost;
- whether claude accepts a placeholder credential and a non-default base
  URL for a subscription login is not verified;
- streaming and the `anthropic-beta` OAuth headers have to pass through
  unchanged.

Rough size: one new frontend component (an HTTP-aware sibling of
`PortForward`), a token source and refresh in the frontend, unit tests,
and a live round. Not built; a separate design.

## Limitations and follow-ups

- The agent runs as the operator's uid, not a dynamic uid. The system-manager
  launcher is deferred.
- Until 2026-10-22 the unit's PID 1 was claude itself (agent-exec exec'd it),
  which did not reap orphans: zombies accumulated during a session (nikulin
  round P: 2 to 6 in one session). agent-exec now stays PID 1 as a minimal
  init (`runAsInit`):
  - it runs the provider as its child and reaps every process reparented
    to it;
  - it passes SIGTERM, SIGHUP, SIGUSR1 and SIGUSR2 on to the provider;
  - with a terminal, it does not pass on SIGINT and SIGQUIT, which already
    reach the provider through the shim's process group; without one it
    passes them on;
  - it exits with the provider's status, or 128 plus the signal that killed
    it.

  Host test: an orphan in a unit whose provider never reaps does not stay a
  zombie. Not yet seen in a live round.
- `claude` credential refresh under a single-inode bind is unverified. A
  rewrite by rename fails with `EBUSY`, and since the bind became read-only
  a write in place fails with `EROFS` (see "The operator's claude
  credential").
- Linux with a systemd user manager only.
- The statsd relay (clown#269) is covered by Go unit tests, not yet by
  `just verify-agent-scope`: the worker that built it ran inside a confined
  unit, where there is no user manager to start a nested one. The host-level
  check (a real hook-toolcost run in a unit, its metrics in stats-me) is left
  to a frontend session. The relay forwards datagrams unchanged: it does not
  validate that they are statsd lines, only who sent them. It is
  fire-and-forget like statsd itself, so a full frontend socket buffer drops
  metrics rather than blocking a hook. Any future metrics emitted from inside
  the unit (e.g. the per-session unused-tool counts in
  `docs/plans/2026-10-20-unused-tools-per-session.md`) should take this same
  path rather than a new one.
