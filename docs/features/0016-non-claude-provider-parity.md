---
status: testing
date: 2026-07-27
---

# Non-Claude Provider Parity — Plugins, Prompt Fragments, and Job-Wakeup for opencode/crush

## Abstract

This record captures research into closing three related capability gaps
between the claude-family providers and the opencode/crush providers:
the clown plugin protocol (MCP tool injection), plugin-contributed
system-prompt fragments, and ringmaster/troupe job-wakeup notifications.

Status is **testing**: phases 0 and 1 are implemented
(`docs/plans/2026-07-27-non-claude-provider-parity-design.md` and its
implementation plan, issue #202), covered by Go unit tests, and smoke-
verified against live opencode 1.17.7 and crush 0.86.0 — `clown
--provider opencode -- mcp list` reports clown's three built-in servers
(`clown-builtin-jobs__ringmaster`, `clown-builtin-jobs__troupe`,
`clown-builtin-juggler__juggler`) as **connected**, and crush's workspace
config at `<data-dir>/crush.json` carries the same three in its `mcp`
block. What is NOT yet covered is an automated regression lane: that is
phase 1b and needs the **dumbo** fixture (see Open Questions). Phases 2
and 3 have not started.
Agreed phasing: **phase 0** make clown's generated provider config
authoritative (a precondition this record originally missed — see finding
0), **phase 1** plugin/MCP compat, **phase 2** static + dynamic prompt
fragments, **phase 3** job-wakeup.

Several claims in the original research have since been checked against
upstream source and live experiment; where they were wrong they are
corrected inline below and marked.

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

### 0. Clown's generated configs are NOT hermetic (added 2026-07-27, verified)

This record originally assumed clown's generated provider configs were
authoritative, as does `cmd/clown/crush.go`'s header comment. Both are
wrong, and the consequence gates finding 1.

Verified live: a repo-local `opencode.json` **replaces** a same-named
entry in clown's `mcp` map. A probe named `clownprobe`, written by clown
pointing at `127.0.0.1:19001`, resolved instead to the project config's
`127.0.0.1:19002`; `opencode mcp list` reported "2 server(s)" — an
override, not a duplicate. crush likewise merges a project `crush.json`
over clown's `CRUSH_GLOBAL_CONFIG`.

So any repository clown is run in can silently repoint a clown-managed
MCP server name at a URL of its choosing. This is latent today only
because clown injects no `mcp` entries into these providers; finding 1 is
the change that arms it.

Levers (both verified): opencode honors
`OPENCODE_DISABLE_PROJECT_CONFIG`; crush has no such switch, but its
workspace config at `<data-dir>/crush.json` is loaded last and overrides
the project config in both directions. Note that `--data-dir` also holds
crush's session state, so clown needs a stable per-project data dir, not
a `mkdtemp`, or `crush --continue` breaks.

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
- **Candidate extension path (superseded — see the design doc):** add
  `CompileForOpencode` / `CompileForCrush` methods to
  `internal/pluginhost` (siblings to the existing `CompileForClaude`,
  `host.go:444`); wire `runOpencode`/`runCrush` through
  `runWithPluginHost`; teach the two config writers to include the
  compiled sections. **The approved design rejects the sibling-compilers
  half of this**: `pluginhost` instead exposes one neutral
  `ServerEntries()` accessor and each provider's existing config writer in
  `cmd/clown` does its own translation, so `pluginhost` does not learn
  three providers' JSON schemas. The design also found `runWithPluginHost`
  is more claude-coupled than "wire it through" implies — its tail is
  `CompileForClaude` → `prependPluginDirs` — so it grows a `pluginBinding`
  seam rather than a new caller.

- **The `mcp` shapes are a translation, not a re-serialization**
  (verified against upstream source):

  | | type vocabulary | timeout unit | default |
  |---|---|---|---|
  | clown `MCPServerEntry` | `http` / `sse` | ms | none emitted |
  | opencode `McpRemoteConfig` | `remote` (literal) | ms | 5000 |
  | crush `MCPConfig` | `stdio` / `sse` / `http` | **seconds** | 15 |

  crush needs ms→s conversion (a naive copy of clown's `30000` becomes an
  8-hour timeout), and opencode's 5s default is shorter than clown's 30s,
  so clown must emit an explicit `timeout`.

- **Flat `mcp` maps force a key-naming decision.** Claude namespaces MCP
  servers per plugin dir; opencode and crush have one flat map. Worse, the
  two derive tool names from the key by different rules — opencode
  sanitizes (`catalog.ts:117-119`, `[^a-zA-Z0-9_-]` → `_`), crush does not
  (`mcp-tools.go:58-60`, `fmt.Sprintf("mcp_%s_%s", ...)`). A `/` key is
  silently mangled by one and invalid under the other. Resolved: keys are
  `<plugin>__<server>`, sanitized clown-side to `[A-Za-z0-9_-]`, with
  collisions detected at compile time.

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

**Resolved 2026-07-27** (issue #202 brainstorm):

- *Should finding 1 and finding 3 ship as separate phases?* Yes — and a
  phase 0 was inserted ahead of both (finding 0). Phase 2 is the prompt
  work.
- *Is job-wakeup worth pursuing at all?* Deferred, not dropped: phase 1
  ships the `clown-builtin-jobs` **MCP tools** to opencode/crush without
  the wake, so those sessions can start and poll jobs (`job_wait` blocks)
  but are not woken. Accepted as degraded-not-broken until phase 3.

Still open:

- Does clown need to **launch** a new `opencode serve`/`crush serve`
  process, or can it **discover and connect to** the server every
  `opencode`/`crush` TUI invocation already starts implicitly? The
  latter would be a much smaller change if opencode/crush expose their
  running instance's address (port/socket) in a way clown can read
  (e.g. a pidfile, a well-known local port, `--print-logs`-style
  stdout). Unconfirmed — needs direct investigation before scoping an
  implementation.
- Where does **dumbo** live — in-tree (`cmd/dumbo`) or as a standalone
  repo in the ringmaster/troupe style (FDR 0014)? Specified in RFC 0017
  (`docs/rfcs/0017-dumbo-mock-model-api.md`), which leaves the home
  question open because it depends on whether phase 3 needs dumbo on the
  ringmaster side too.
  - Correction: this record previously said the phase-1 bats lane *needs*
    dumbo. It does not. `clown --provider opencode -- mcp list` exercises
    config synthesis, key naming, schema validation, plugin-host startup
    and the MCP handshake with no model involved, so issue #203 can land
    without dumbo. dumbo is required only for phases 2 and 3, where the
    agent must actually take a turn.
- Does clown's blanket project-config suppression (phase 0) eventually
  become a **safe merge** — honoring project entries except where they
  would clobber a clown-owned key? That is the stated end state, but the
  merge semantics are unspecified.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| Plugin/MCP compat vs. job-wakeup: same phase or separate | **Settled: separate.** Phases 0-3 as in the Abstract | MCP compat is additive and low-risk; job-wakeup changes process invocation | — (resolved) |
| Launch-new-server vs. discover-existing-server for opencode/crush | Unknown — not yet investigated | Discovery would be much cheaper than a new subprocess-management architecture | Direct investigation of what `opencode`/`crush`'s bare CLI invocation exposes about its embedded server's address |
| MCP key scheme | `<plugin>__<server>`, `[A-Za-z0-9_-]` | union of both providers' constraints; makes opencode's sanitize a no-op | tool-name length overruns near the ~64-char cap (crush's `mcp_` prefix eats 4) |
| opencode per-entry `timeout` | explicit, from `clown.json` | opencode's 5s default is shorter than clown's 30s | per-entry proves noisy → switch to `experimental.mcp_timeout` |
| crush data-dir location | stable, clown-owned, per-project | a `mkdtemp` would break `crush --continue` | session-continuity complaints, or collisions across worktrees |
| Project-config suppression (phase 0) | on | closes the MCP-name override in finding 0 | a user legitimately needs repo-local config → build the safe merge sooner |

## More Information

- `docs/plans/2026-07-27-non-claude-provider-parity-design.md` — the
  approved phase 0 + phase 1 design (issue #202), including the evidence
  table distinguishing observed from source-read claims.
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
- Upstream source consulted for findings 0 and 1 (anomalyco/opencode,
  charmbracelet/crush): opencode config loading
  `packages/opencode/src/config/config.ts`, MCP schema
  `packages/core/src/v1/config/mcp.ts`, tool naming
  `packages/opencode/src/mcp/catalog.ts`; crush config loading
  `internal/config/load.go`, MCP schema `internal/config/config.go`, tool
  naming `internal/agent/tools/mcp-tools.go`.
