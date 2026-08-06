---
status: exploring
date: 2026-05-27
superseded-by: FDR-0014 (cloud tent — the cross-arch problem this record addresses is removed by the cloud substrate)
promotion-criteria: cross-arch papercuts (Mach-O bash, host nix-store leak-through, --tent-pass-devshell PATH races) eliminated by construction; a host-side hook tunnel exists and survives a representative plugin set (docs-r-us, eng:*, course-correct, spinclass) without per-plugin tent-awareness; agent-driven git work via grit MCP matches the ergonomics of agent-driven git work via in-tent `git` today; clown depends on moxy as a documented peer
---

# Tent narrow userspace + host-side hook tunnel

## Problem Statement

FDR-0007 introduced tent as "the environment the harness sees" — the
trust boundary that lets clown enforce its unbypassable-settings
invariant by controlling filesystem, network, and command surface
around the harness. Today's tent implementation tries to be a
*general-purpose linux userspace* on top of that: it bind-mounts the
host's `/nix/store`, forwards a filtered host `$PATH`
(`--tent-pass-devshell`, auto-on inside `IN_NIX_SHELL`), bind-mounts
`~/.claude/`, and generally tries to give the in-tent claude
everything the un-tented claude would have.

That "general-purpose linux userspace" framing collapses on darwin
because the host nix-store contains Mach-O binaries that cannot
execute on linux. When the in-tent claude (or a hook, or a plugin
subprocess) resolves `bash`, `jq`, `git`, or any other unqualified
command through `$PATH`, it finds the darwin binary first and
fails with `Exec format error`. Issue #44 (docs-r-us SessionStart
hook hitting Mach-O bash) is the most visible instance, but the
mechanism is general — every shebang-less subprocess invocation in
every plugin's hooks has the same failure mode. `nix build` inside
the tent has the same problem one layer up: the linux build would
consume darwin store inputs, producing mixed-arch closures that
cannot run.

The root cause isn't the bind-mount or the PATH rewriting — it's
that tent's responsibility was scoped too broadly. The actual goal
of tent is to **make broad Bash permissions (and YOLO surprises like
`rm -rf`) safe by bounding the agent's filesystem and network blast
radius**. That goal does not require the tent to run general-purpose
linux work. It requires the tent to run *the agent loop and its
builtin tools* in a contained environment, and to delegate
everything else back to the host where the user's real
environment (right-arch binaries, ssh-agent, signing keys, Keychain,
language toolchains) lives.

The current scope conflation imposes three costs:

1. **Cross-arch papercuts** (issue #44 and its siblings): every
   shebang-less subprocess in a hook or plugin breaks on darwin.
2. **Bind-mount complexity creep**: eng#107/108/112 each added a new
   mount to the host's podman-machine config because something
   in-tent reached for a host path. The mount list is reactive to
   plugin behavior, not load-bearing for the trust boundary.
3. **`--tent-pass-devshell` race conditions**: the flag is on by
   default in any `IN_NIX_SHELL` and silently rewrites PATH. On
   darwin this is the immediate cause of #44, but even on linux it
   gives the tent a non-deterministic, host-dependent PATH that
   makes the isolation boundary hard to reason about.

This FDR narrows tent's contents and adds a host-side **hook tunnel**
symmetric to the MCP-over-HTTP tunnel that already exists. The
in-tent harness runs against a self-contained linux userspace
(busybox-flavored coreutils + bash + curl). Anything that needs to
touch the host — plugin hooks, git operations, language toolchains,
the user's ssh-agent — calls out through the tunnel. The result:
the cross-arch problem evaporates by construction (the tent has its
own bash; PATH passthrough disappears; the host nix-store bind-mount
disappears), and the trust boundary becomes "the agent + harness
builtins run contained; everything else runs on the host."

## Interface

### What's inside the tent

- **claude-code binary** (linux, right-arch for the tent).
- **Busybox-flavored userspace**: `bash`, `sh`, `cat`, `grep`, `sed`,
  `awk`, `find`, `head`, `tail`, `cp`, `mv`, `mkdir`, `rm`, `ls`,
  `which`, `env`, `xargs`, `tr`, `sort`, `uniq`, `wc`, `diff`, plus
  whatever else the agent reaches for during routine Bash-tool use
  inside a project tree. The final set is calibrated against real
  agent-session transcripts.
- **Networking toolchain**: `curl`, `wget`, `openssl`,
  `ca-certificates`, a DNS resolver. The tent has unrestricted-ish
  network access; the agent's WebFetch tool and Anthropic API calls
  go directly from the tent over its own network namespace, not
  through the host.
- **A `clown-hook-shim` binary** baked into the image. Every hook
  command registered with the in-tent claude is a `clown-hook-shim`
  invocation that proxies stdin/stdout/stderr/exit-code over a unix
  socket bind-mounted from the host. (Shape mirrors FDR-0004's
  bridge.)

### What's no longer inside the tent

- **No host `/nix/store` bind-mount.** The tent's nix-store entries
  are baked into the image at build time. Cross-arch impossible by
  construction.
- **No `--tent-pass-devshell` PATH rewriting.** The image bakes its
  own PATH. The flag is removed (or kept as a no-op with a
  deprecation note for one release).
- **No `git` binary.** Git operations route through grit, which is an
  MCP tool surface served by moxy on the host. Inside the tent, the
  agent calls `grit_status`, `grit_diff`, `grit_commit` etc. via
  claude-code's MCP layer; those calls reach the host where the real
  git, ssh-agent, and signing keys live.
- **No language toolchains** (`nix`, `npm`, `cargo`, `go`, `python`,
  language servers). These either route through MCP tools served by
  moxy (the preferred path; they end up looking like grit), through
  the hook tunnel (for one-shot invocations), or are out of scope
  for the agent loop and require explicit user invocation outside
  tent.
- **No `~/.ssh`, no Keychain reach, no `~/.aws`, no host devshell
  env**. The tent's filesystem is bounded to the project workdir
  plus the tent image. Host secrets are reachable only via host-side
  tunneled operations.

### Host-side hook tunnel

The hook tunnel is the new transport. It is symmetric to (and a
generalization of) the FDR-0004 bridge:

- Clown stages a per-session **hooks block** alongside the existing
  `mcpServers` block when launching claude into tent.
- Every registered hook (SessionStart, PreToolUse, PostToolUse,
  Stop, etc.) has a `command` of `clown-hook-shim` plus opaque
  routing info (handler id, hook event name, plugin id).
- claude-code-in-tent invokes the shim. The shim connects to the
  per-session unix socket (bind-mounted from the host) and forwards
  the hook payload + env.
- A host-side dispatcher (folded into clown itself, since clown
  already manages the tent lifecycle) executes the original hook
  command on the host, with the user's real environment, and
  returns stdout/stderr/exit-code.
- Hook authors write hooks unaware of tent. They run on the host
  with the user's PATH, ssh-agent, and Keychain.

### Trust model (unchanged in spirit, sharpened in shape)

- The host is trusted.
- The agent's *behavior* is not.
- The tent is the lever that lets the user push **permission prompts
  down** ("allow Bash" once instead of fifty times) and accept
  **YOLO-surprise containment** (`rm -rf` inside tent destroys an
  ephemeral writable layer; the host workdir is unaffected if the
  mount is read-write-overlay, or the destruction is scoped to the
  bind-mounted workdir if it's a direct mount).
- Network exfiltration is *not* eliminated — the tent has curl. The
  trade is deliberate: agents legitimately need WebFetch and HTTP,
  and exfil-via-API was always possible through the LLM's own
  response. The tent's filesystem boundary still keeps host secrets
  (`~/.ssh`, Keychain, the user's `~/.aws`) out of reach.

### Three-way balance preserved

- **Performance**: no cross-arch friction, no host nix-store
  traversal cost, no PATH-rewriting race. Hook tunnel adds a single
  unix-socket round-trip per hook invocation; for hooks that fire
  once per tool call this is dominated by the tool call itself.
- **Permission prompts**: broad Bash inside tent is safe (contained
  userspace, bounded filesystem, no host nix-store). The user can
  grant `Bash(*)` and trust the boundary.
- **YOLO-surprise**: bounded blast radius. Destructive Bash inside
  tent destroys tent-local state; the host filesystem is reachable
  only via the project workdir bind-mount, which is the user's
  active codebase (the place they expected the agent to be editing
  anyway).

## Examples

### What an in-tent claude session looks like

```sh
clown --tent
# inside the tent: claude-code launches, the image's PATH points at
# /usr/bin (linux busybox + bash + curl), the user's workdir is the
# only writable host path, hooks fire via clown-hook-shim
```

### How a SessionStart hook flows

```
in-tent claude
  → spawns: /usr/bin/clown-hook-shim --hook-event SessionStart \
            --handler docs-r-us-session-start
  → shim connects to /tent/host-tunnel.sock
host clown-hook-dispatcher
  → receives: {event: "SessionStart", handler: "docs-r-us-session-start", env: {...}}
  → executes on host: bash ${CLAUDE_PLUGIN_ROOT}/hooks/docs-r-us-session-start.sh
    (host PATH, host bash, host brew, host gh)
  → returns: {stdout: "...", stderr: "", exit_code: 0}
shim receives the response, writes stdout/stderr, exits with the host's code
in-tent claude receives the hook output as if the script had run in-tent
```

### How agent-driven git work flows

```
agent decides: "let me check what's modified"
claude-code-in-tent → MCP call: grit_status
  ↓ HTTP over loopback to clown-plugin-host on the host
host moxy: runs `git status` in the workdir on the host
  → ssh-agent / pivy-agent reachable (signing keys for commits)
  → returns structured status to the agent
agent decides: "stage and commit"
claude-code-in-tent → MCP call: grit_commit { paths: [...], message: "..." }
  ↓ HTTP over loopback
host moxy: `git add`, `git commit -S` (signing works because we're
on the host with the user's gpg/ssh keys)
```

No in-tent git binary. No ssh-agent forwarding. No keychain plumbing.

### `--tent-pass-devshell` is gone

```sh
# Old behavior (today):
clown --tent          # implicitly --tent-pass-devshell when IN_NIX_SHELL
clown --tent --tent-pass-devshell  # explicit on
clown --tent --no-tent-pass-devshell  # explicit off

# New behavior:
clown --tent          # tent has its own PATH; flag is a no-op + deprecation warning
```

Users who genuinely need a host nix-store binary inside the tent
(rare and probably wrong) will surface that need explicitly via a
new mechanism rather than relying on opaque PATH passthrough.

## Limitations

- **Subagent-spawned long-running builds** (e.g. `cargo build`,
  `npm install`, `nix build`) cannot run inside this tent. They
  must be invoked on the host — via an MCP tool surface
  (preferred), via the hook tunnel for one-shots, or by the user
  explicitly outside the tent. This is the largest user-visible
  trade vs. today's tent.
- **The tent loses `direnv` / devshell PATH transparency.** A user
  in a project with a devshell who runs `clown --tent` no longer
  has their devshell's tools available inside the tent. If they need
  those tools to be reachable, they go through the MCP tool surface
  or through the hook tunnel. The justification: the devshell's tools
  are darwin-on-darwin or linux-on-linux as appropriate; routing
  them through host-side tunnels removes the cross-arch failure mode
  and matches how MCP servers already work.
- **The tent has network**. WebFetch, Anthropic API, and any
  `curl` the agent runs reach the open internet. The trust trade is
  deliberate (exfil-via-LLM-response was always possible) but it
  means the tent is *not* an air-gapped sandbox. Users wanting
  air-gap semantics can layer `--network=none` on top, accepting
  that the agent loses WebFetch and may lose API access depending
  on routing.
- **moxy is a peer dependency**. Without moxy on the host serving
  grit (and likely other tools the agent now needs to delegate),
  the in-tent agent can't do git work. clown ships a documented
  peer-dep declaration and a clear error when moxy isn't reachable.
- **The hook tunnel adds a per-hook latency floor** (unix socket
  round-trip + process spawn on the host). For hooks that fire on
  every tool call (PreToolUse), this needs to stay under ~5 ms
  to feel transparent. Implementation has to keep the host-side
  dispatcher resident.
- **Image size grows** to accommodate the baked busybox + bash +
  curl + openssl + ca-certificates set. Today's tracer-bullet image
  is ~80 MB; the new self-contained shape is likely 100-130 MB.
  Acceptable.

## More Information

- [FDR-0007: tent — Harness Containment as the Policy Boundary][fdr-0007] —
  the parent FDR. This FDR narrows tent's contents and clarifies the
  scope of "the environment the harness sees" without changing the
  unbypassable-settings invariant or the trust direction.
- [FDR-0004: Hook Sandbox-Escape Bridge][fdr-0004] — the host-mediated
  hook bridge that this FDR generalizes. The hook tunnel here is the
  same transport pattern; FDR-0004's machinery (clown-hook-shim,
  per-session unix socket, dispatcher in clown) is reused.
- Issue #44 — the docs-r-us SessionStart Mach-O bash bug that
  prompted the narrowing. Resolution lands when this FDR's hook
  tunnel ships.
- Issues eng#107, eng#108, eng#112 — the mount-list churn that this
  narrowing eliminates. Each was a reactive bind-mount addition
  forced by something in-tent reaching for a host path. With the
  tent self-contained, mount-list changes stop being load-bearing
  for plugin compatibility.
- moxy — peer dependency providing grit (the MCP git surface) and
  likely other host-tool MCP surfaces the in-tent agent will need.

[fdr-0007]: ./0007-tent.md
[fdr-0004]: ./0004-hook-sandbox-escape-bridge.md
