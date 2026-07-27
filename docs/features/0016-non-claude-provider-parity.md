---
status: exploring
date: 2026-07-27
---

# Non-Claude Provider Parity — Plugins, Prompt Fragments, and Job-Wakeup for opencode/crush

## Abstract

This record captures research into closing three related capability gaps
between the claude-family providers and the opencode/crush providers:
the clown plugin protocol (MCP tool injection), plugin-contributed
system-prompt fragments, and ringmaster/troupe job-wakeup notifications.
Status is **exploring** — the research below identifies concrete,
plausible extension paths for each gap, but no implementation has
started and no scope/phasing decision has been made.

## Problem Statement

Today, `internal/pluginhost`'s plugin protocol (RFC 0002: HTTP MCP
server lifecycle, tool injection, and FDR 0003's system-prompt-fragment
contribution) only runs for the claude family (`claude`, `clownbox`).
`providerUsesPluginDirs` (`cmd/clown/jobmonitor.go`) explicitly excludes
`opencode`, `openrouter`, and `crush`; `runOpencode`/`runCrush`
(`cmd/clown/opencode.go`, `cmd/clown/crush.go`) never call
`runWithPluginHost` and their config writers emit only
provider/model/token. Separately, clown's job-wakeup channel
(ringmaster/troupe, FDR 0013) — the mechanism that lets a background job
asynchronously notify a running session — has no path into opencode or
crush sessions at all today. A user running `clown --provider opencode`
or `--provider crush` gets none of: clown-managed MCP tools, plugin
system-prompt fragments, or job-wakeup notifications, regardless of
which model backend (Anthropic, OpenRouter, a local gateway) that
session talks to.

## Findings

### 1. Plugin protocol / MCP compat: both providers have a native landing spot

- **opencode** (`opencode.ai/config.json` schema): a top-level `mcp`
  object keyed by server name. Each entry is either `McpLocalConfig`
  (stdio: `command`, `environment`, `cwd`) or `McpRemoteConfig`
  (`type: "remote"`, `url`, `headers`, `oauth`) — the remote shape is a
  direct match for what `internal/pluginhost` already produces: an HTTP
  MCP server bound to a loopback port with a known URL. opencode's
  config also has a top-level `instructions: string[]` field (file paths
  or patterns to include) — a plausible home for FDR 0003's
  plugin-contributed prompt fragments.
- **crush** (`charm.land/crush.json` schema): the same shape. A native
  `mcp` object (`MCPConfig`, `type: "stdio" | "sse" | "http"`, `url` +
  `headers` for the http/sse case), and `options.context_paths` /
  `options.global_context_paths` (file-based context injection) as
  crush's fragment-injection analog.
- **Current gap is wiring, not capability.** `writeOpencodeConfigFile`
  and `writeCrushConfig` don't populate `mcp`/`instructions`/
  `context_paths` at all today, and neither provider is routed through
  `runWithPluginHost`. RFC 0002 §5.5 already documents this scope
  boundary explicitly: "Dynamic contribution applies only to downstream
  providers that run under the plugin host... the claude family."
- **Candidate extension path:** add `CompileForOpencode` /
  `CompileForCrush` methods to `internal/pluginhost` (siblings to the
  existing `CompileForClaude`, `host.go:444`) that emit the
  provider-native `mcp` shapes above instead of Claude's `plugin.json`
  shape; wire `runOpencode`/`runCrush` through `runWithPluginHost`; teach
  the two config writers to include the compiled `mcp` and
  `instructions`/`context_paths` sections.

### 2. OpenRouter is orthogonal — no separate work needed

OpenRouter is purely a URL/token gateway selection
(`resolveOpencodeGateway`, `openrouterGatewayURL` in
`cmd/clown/opencode.go`) for whichever frontend is already running. It
has no interaction with plugin/MCP wiring, which lives entirely on the
frontend-process side. Once finding 1's extension lands, any
OpenRouter-backed `opencode`/`crush` profile gets plugin/MCP support for
free — there is no OpenRouter-specific piece of this feature.

### 3. Ringmaster/troupe job-wakeup: both providers have a real primitive, gated on a bigger architectural shift

Researched whether opencode or crush have an equivalent to Claude
Code's background-task-notification wake (the mechanism ringmaster's
job-wakeup channel, FDR 0013, piggybacks on for claude).

- **opencode**: `opencode serve` exposes `POST /session/:id/prompt_async`
  — pushes a new message into a session and returns `204` immediately,
  no waiting for a reply (`opencode.ai/docs/server`). Architecturally
  this is the same shape as what Claude Code's task-notification
  injection achieves: a new turn pushed in from outside while the
  session is otherwise idle. opencode's plugin system (`opencode.ai/docs/plugins`)
  also exposes a `session.idle` event a plugin can react to, though the
  documented examples only show it firing an outbound OS notification,
  not injecting back into the session.
- **crush**: `crush serve` exposes `POST /v1/workspaces/{id}/agent` —
  dispatches the run on a goroutine, returns `202 Accepted` immediately,
  and queues the prompt if the workspace is mid-turn.
  Same story: a real, usable primitive.
  - Neither has a literal "inject a system-reminder without it
  counting as a new turn" mechanism — both primitives are "submit a new
  prompt," not "annotate the existing transcript." For ringmaster's
  actual need (wake an idle session with news), this distinction likely
  doesn't matter in practice.
- **The actual gap:** `runOpencode`/`runCrush` today `exec` the CLI
  directly (replacing the process or running it as a plain child) with
  no persistent server clown manages or holds a handle to — there is no
  running server instance for ringmaster to target. Supporting
  job-wakeup for these two providers requires clown to run them in
  server mode and keep a client connection to that server — an
  architecture closer to how the claude family already works under the
  plugin host, but a materially bigger shift than finding 1's config-file
  changes, since it changes how these providers are invoked at all.

## Open Questions

- Does clown need to **launch** a new `opencode serve`/`crush serve`
  process, or can it **discover and connect to** the server every
  `opencode`/`crush` TUI invocation already starts implicitly? The
  latter would be a much smaller change if opencode/crush expose their
  running instance's address (port/socket) in a way clown can read
  (e.g. a pidfile, a well-known local port, `--print-logs`-style
  stdout). Unconfirmed — needs direct investigation before scoping an
  implementation.
- Should finding 1 (plugin/MCP compat) and finding 3 (job-wakeup) ship
  as separate phases? They have very different blast radius — finding 1
  is additive config-writer changes plus new `pluginhost` methods;
  finding 3 changes the process-invocation model for two providers.
  Provisional lean: yes, phase them — finding 1 first as a
  self-contained, lower-risk slice.
- Is job-wakeup support for opencode/crush worth the architectural
  shift at all, or should it stay a claude-family-only capability
  indefinitely (documented as a deliberate limitation rather than a gap
  to close)? No usage signal yet either way.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| Plugin/MCP compat vs. job-wakeup: same phase or separate | Provisionally separate, MCP first | MCP compat is additive and low-risk; job-wakeup changes process invocation | A concrete user request ties the two together, or MCP-first turns out to require server-mode plumbing anyway |
| Launch-new-server vs. discover-existing-server for opencode/crush | Unknown — not yet investigated | Discovery would be much cheaper than a new subprocess-management architecture | Direct investigation of what `opencode`/`crush`'s bare CLI invocation exposes about its embedded server's address |

## More Information

- RFC 0002 — Clown Plugin Protocol: HTTP MCP Server Lifecycle Management
  (`docs/rfcs/0002-clown-plugin-protocol.md`), especially §5.5's explicit
  claude-family scope boundary and §3.6-3.7's manifest compilation.
- FDR 0003 — Plugin-Contributed System Prompt Fragments
  (`docs/features/0003-plugin-contributed-prompt-fragments.md`).
- FDR 0013 — Job-Wakeup Channel (`docs/features/0013-job-wakeup-channel.md`).
- `docs/plans/2026-07-24-openrouter-non-anthropic-design.md` and
  `docs/plans/2026-07-26-openrouter-model-picker-design.md` — the
  OpenRouter provider work this research grew out of; confirms finding 2's
  orthogonality claim from the gateway-resolution side.
- opencode config schema: `https://opencode.ai/config.json`; server API:
  `https://opencode.ai/docs/server/`; plugin API: `https://opencode.ai/docs/plugins/`.
- crush config schema: `https://charm.land/crush.json`.
