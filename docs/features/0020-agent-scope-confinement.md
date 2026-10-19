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
   frontend.

Per-tool status, to be measured by the launcher's test:

| Tool | Path | Expected |
|---|---|---|
| claude → model API | `HTTPS_PROXY` | honoured (Node fetch via claude's proxy support) |
| `git fetch` over https | `https_proxy` (libcurl) | honoured |
| `git fetch`/`push` over ssh | `GIT_SSH_COMMAND` with `-o ProxyCommand=clown egress-connect %h %p` | `clown egress-connect` dials the unix socket and issues `CONNECT`. `known_hosts` is bound read-only from the spec, or the check is not strict. |
| `nix build` substitution | done by nix-daemon (a system service, outside the unit) over `/nix/var/nix/daemon-socket/socket` | unaffected by the unit's netns |
| nix client-side fetches (flake inputs, `fetchTree`) | libcurl `https_proxy` | honoured |
| statsd (clown-hook-toolcost, UDP 127.0.0.1:8125) | not proxyable | dropped under confinement, or sent over a passed unixgram socket |

The sweatfile switch that enables confinement must not be able to produce a
confined unit without the proxy. clown refuses `--agent-scope` when the proxy
cannot start.

## Environment

The unit starts from an empty environment, because systemd-run does not
forward the client's environment. The manager's environment is added on top,
and clown unsets it. clown then sets exactly the following.

**Allowlist (passed through from the frontend):**
- `PATH`, `TERM`, `COLORTERM`, `LANG`, `LC_*`, `TZ`, `TZDIR`
- `LOCALE_ARCHIVE`, and the `NIX_*` path variables
- `ANTHROPIC_*` (provider routing that clown already sets)
- `CLOWN_NAME`, `CLOWN_GROUP_ID`, `CLOWN_GROUP_DESCRIPTION`, `CLOWN_XMPP_VHOST`

**From the spec:** `env{}`, verbatim (spinclass's `SPINCLASS_*` identity).

**Set by clown:**
- `HOME` and `XDG_{CONFIG,STATE,CACHE,DATA}_HOME` point under a per-session
  agent home (default `<worktree>/.tmp/agent-home`). Hooks and tools that write
  state then land inside the bound worktree instead of failing on the hidden
  real home.
- The proxy variables (see Egress).
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
- `TROUPE_XMPP_PASSWORD_FILE` and other credential paths
- `CLOWN_SESSION_ID` (clown#136: never ambient)

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
under a frontend-owned directory (`$XDG_RUNTIME_DIR/clown/agent/<unit>/`).
Each is bound into the unit as a single inode at the same path. The frontend
end checks `SO_PEERCRED` and `/proc/<pid>/cgroup` against the unit's cgroup.
Because of `PrivateNetwork`, loopback TCP is not offered to confined sessions
at all.

| Consumer (in the agent scope) | Needs | Mechanism |
|---|---|---|
| claude → frontend MCP servers (ringmaster mcp, troupe mcp, `spinclass serve`) | an MCP stream | A per-server unix socket. For each accepted connection, the frontend starts a fresh stdio server and splices the two. The in-unit side is `clown mcp-socket-relay <sock>`, which claude starts as a stdio MCP server. A plugin marks a server frontend in `clown.json` (`"scope": "frontend"`); clown-builtin-jobs' servers are frontend. |
| claude → agent-side MCP servers (moxy and other file-tool servers) | | Run **inside** the unit as claude's stdio children, so their file tools are confined too. A frontend moxy would be a ProtectHome bypass through `folio_write`. |
| plugin monitors (job wakes, chat) | wake lines on stdout | troupe's connection owner (`troupe receive`) and `ringmaster monitor` run in the frontend (FDR 0032 D7). The in-unit monitor is a relay that reads their lines from a passed unix socket. The XMPP password file then never enters the unit. |
| clown-hook-tee (Stop/SessionEnd) | troupe send | the troupe IPC socket from spec `sockets[]` |
| clown-hook-toolcost | state dir, statsd | state under the redirected `XDG_STATE_HOME`; statsd as in Egress |
| clown-hook-allow (PreToolUse) | nothing | pure |
| moxy hook logger | `$XDG_LOG_HOME/moxy/hooks` | redirected under the agent home, unless spinclass binds the real dir rw |
| claude itself | `~/.claude` config, credentials, transcripts | `CLAUDE_CONFIG_DIR` points at a per-session dir. `.credentials.json` is bound in (open question below). The project's transcript dir is bound rw, so `clown resume` and freud keep working. `~/.claude/settings.json` is never writable from inside: a hook written there would run in unconfined sessions. |
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
  "result_path": "<worktree>/.spinclass/agent-unit.json",
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

**Result.** Once the unit is `active`, clown writes the result atomically
(tmp + rename) to `result_path`. If the launch fails, clown rewrites the file
with `error` set.

```json
{
  "version": 1,
  "unit": "spinclass-agent-clown-cool-fir-1a2b.service",
  "cgroup": "/user.slice/user-1001.slice/user@1001.service/clown.slice/clown-agent.slice/spinclass-agent-clown-cool-fir-1a2b.service",
  "invocation_id": "…",
  "uid": 1001,
  "pty": "/dev/pts/N",
  "main_pid": 12345
}
```

Field notes:

- `unit` is authoritative. spinclass ignores its own hint once a result
  exists. clown escapes the hint and adds a unique suffix.
- `pty` is the host path of the slave that clown allocated.
- `main_pid` is in the frontend's pid namespace.

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
  them).
- `clown agent-exec`, the in-unit shim (`setsid` + `TIOCSCTTY(0)`), and the
  ptysuspend relay with `DetachSlave`/`NoEscape`.
- clown's plumbing binds for the base launcher: a per-session
  `CLAUDE_CONFIG_DIR` under `<worktree>/.tmp/agent-home/.claude` seeded from
  the operator's `.claude.json` and `settings.json`, the operator's
  credentials file and this project's transcript dir bound over it, the
  ringmaster/troupe state and runtime dirs, clown hook state, moxy's hook log
  dir, the troupe password file read-only, and the staging root read-only
  when it is outside the worktree. Until the monitors move to the frontend,
  troupe's connection owner still runs inside, which is why the password
  file and `TROUPE_*` env are passed.
- systemd-run and systemctl come from `PATH` (overridable with
  `CLOWN_SYSTEMD_RUN`/`CLOWN_SYSTEMCTL`): systemd-run serializes properties
  client-side, so it must match the running manager.
- `clown agent-scope-run --spec <path> -- argv` runs any command through the
  same launcher; `just verify-agent-scope` (bats tag `agent_scope`, host-only)
  uses it to check, against real units: home, bus, display, posh, ptys and
  other pids unreachable from inside; the result's unit and cgroup; the
  frontend and piggy-agent outside the cgroup; the unit gone after exit; the
  pty path; refusal of a spec that exposes the operator home.

End to end with real claude (`just explore explore-agent-scope-claude`, host
netns, 2026-10-19): `clown --agent-scope <spec> -- -p ...` from a scratch
worktree ran claude inside the unit; its Read tool call worked, the answer was
correct, exit 0; login worked through the bound credentials file; the
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

Not built yet: the egress proxy and `PrivateNetwork`, per-session frontend
MCP sockets and `"scope": "frontend"`, frontend monitors. Plugin monitors
(job wakes, chat) inside the unit are untested: `-p` mode does not start
them.

## Limitations and follow-ups

- The agent runs as the operator's uid, not a dynamic uid. The system-manager
  launcher is deferred.
- Moving troupe's connection owner to the frontend changes the
  `clown-builtin-jobs` monitor shape: a relay in the unit, the owner in the
  frontend.
- `claude` credential refresh under a single-inode bind is unverified. A
  rewrite by rename fails with `EBUSY`.
- Linux with a systemd user manager only.
