# Juggler Subagent-Delegation Tool — Design

Date: 2026-07-11
Status: approved (brainstorm with Sasha, session live-catalpa)

## Problem

We just shipped juggler's model registry and `juggler prompt` (a CLI that resolves
a registered model — local llama-server or an OpenRouter/remote gateway entry —
and sends it one prompt). The natural next question: can a running Claude Code
session delegate work to one of these models as a subagent, instead of Claude
Code's builtin Task-tool subagents (which always share the same model family)?

**The hard constraint, already documented in
`docs/plans/2026-07-07-juggler-model-registry-design.md`:** Claude Code is a
single process with one process-wide `ANTHROPIC_BASE_URL`/`ANTHROPIC_AUTH_TOKEN`.
There is no hook point clown can reach to make Claude Code's *native* Task tool
route a specific subagent invocation to a different endpoint — `CLAUDE_CODE_SUBAGENT_MODEL`
only ever selects a different model *name* against the one shared endpoint.
Confirmed again this session: `cmd/clown-hook-allow/main.go` can intercept and
*rewrite* a Task-tool call's input (it already does this for `Explore`→`Discover`),
but it cannot redirect where the resulting inference request goes — that's decided
entirely inside the `claude` binary.

**Reframing, confirmed with Sasha:** "subagent" here means a *new, explicit tool*
the main agent calls to delegate a task to a named juggler model and get text
back — conceptually subagent-shaped (a task handed off, a result returned),
mechanically a tool call, not a Task-tool redirect. This works today with zero
Claude Code changes.

## Decision summary

1. **New `juggler mcp` subcommand** (same binary as `juggler prompt`/`model add`):
   a hand-rolled, line-delimited JSON-RPC 2.0 server on stdin/stdout, mirroring
   `ringmaster mcp`'s `jobmcp.Serve` implementation almost exactly (verified this
   session by reading the real ringmaster source via the nix store:
   `initialize` → `tools/list` → `tools/call`, `toolText`/`toolErr` content
   wrapping so tool-level failures surface as agent-readable output, not
   transport errors). Living in `cmd/juggler` means it calls
   `sendAnthropicPrompt`/`sendOpenAICompatPrompt` directly — the exact same
   functions `juggler prompt` already uses and already has full test coverage
   for. Zero protocol-handling duplication.

2. **New `clown-builtin-juggler` synthesized plugin** (a sibling to the existing
   `clown-builtin-jobs`, not folded into it — this is model delegation, a
   different concern from job/chat platform control). `cmd/clown` synthesizes
   its `clown.json` at session launch exactly like `jobmonitor.go` does for
   `clown-builtin-jobs`: one `stdioServers` entry,
   `{"command": buildcfg.JugglerCliPath, "args": ["mcp"]}`. `buildcfg.JugglerCliPath`
   already exists (burned in via `flake.nix`'s clown-go ldflags for the existing
   `pickJugglerModel` local-model picker) — no new build wiring needed. Only
   synthesized when `buildcfg.JugglerCliPath != ""`, mirroring how ringmaster/
   troupe entries are conditional on their own path variables.

3. **One tool, `juggler-prompt(model, task, max_tokens?)`** — a model-name
   parameter rather than one generated tool per registered model, so the tool
   catalog never needs regenerating when models are added/removed (mirrors how
   the Task tool itself takes `subagent_type` as a parameter, not one tool per
   subagent type).

4. **Permission: defer to Claude Code's native per-call prompt, no auto-allow.**
   `clown-hook-allow` already has exactly the "override some, defer the rest"
   shape this needs (`cmd/clown-hook-allow/main.go`: auto-allow `/nix/store`
   reads and `clown-builtin-jobs` tools, rewrite `Explore`→`Discover`, defer
   everything else at line 236) — this tool is deliberately left out of the
   allow-list, so it falls through to the existing defer path with zero new
   code. A comment at the defer fallthrough names the tool explicitly so a
   future reader knows the omission is intentional (real third-party API cost
   per call), not an oversight.

## Interface

```
juggler mcp
```

Line-delimited JSON-RPC 2.0 on stdin/stdout. Methods: `initialize`,
`tools/list`, `tools/call`, `notifications/initialized` (no-op). Single tool:

```json
{
  "name": "juggler-prompt",
  "description": "Delegate a task to a registered juggler model (local or remote) and get its text reply.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "model": {"type": "string", "description": "a name registered via `juggler model add`, or a local GGUF name"},
      "task": {"type": "string", "description": "the task/prompt to send"},
      "max_tokens": {"type": "integer", "description": "default 1024"}
    },
    "required": ["model", "task"]
  }
}
```

`tools/call` → resolve `model` via the existing `ResolveModel` RPC, dispatch to
`sendAnthropicPrompt`/`sendOpenAICompatPrompt` exactly as `juggler prompt` does,
wrap the reply as `toolText(reply)` or any failure as `toolErr(msg)`. Each call
gets its own `context.WithTimeout` (see Tuning Levers).

## Error handling

- Daemon unreachable → `toolErr` naming the socket and the fix (`juggler daemon`),
  mirroring `resolveViaJuggler`'s existing message shape.
- Unknown model name → `toolErr` surfacing the `-32001` not-found case.
- Unsupported style / non-2xx HTTP → surfaced verbatim from the existing,
  already-tested `sendAnthropicPrompt`/`sendOpenAICompatPrompt` error paths.

All of these are `isError: true` MCP tool results, not JSON-RPC transport
errors — the agent sees them as tool output it can read and react to (e.g. try
a different registered model), matching `jobmcp`'s established convention.

## Testing

- `cmd/juggler/mcp_test.go`: drive `Serve` directly over `bytes.Buffer`s
  (mirroring how `server_test.go` drives `s.dispatch` directly) —
  `initialize`/`tools/list` shape, `tools/call` happy path against a fake
  remote endpoint (reusing `prompt_test.go`'s `fakeResolveModelDaemon` +
  `httptest.Server` harness), and each error path above.
- `cmd/clown/jugglermonitor_test.go`: mirrors whatever `jobmonitor_test.go`
  covers for synth-plugin-dir logic — manifest shape, conditional gating on
  `JugglerCliPath`.
- Manual smoke (before touching clown's plugin wiring at all): pipe a raw
  `tools/call` JSON-RPC line into `juggler mcp`'s stdin directly against this
  session's real registered OpenRouter models, confirm a real reply comes back.

## Rollback

Purely additive — new binary subcommand, new synthesized plugin, no existing
behavior changed. A single env var, `CLOWN_DISABLE_JUGGLER_MCP=1` (mirroring
the existing `CLOWN_DISABLE_JOB_WAKEUP=1` convention for `clown-builtin-jobs`),
skips synthesizing the plugin entirely — the fastest possible revert, no code
change needed to turn it off for a session.

## Tuning levers

- **`max_tokens` default (1024).** Deliberately higher than `juggler prompt`'s
  CLI default (256) — the CLI is a quick smoke/hello-world tool, this tool is
  for real delegated subagent-shaped work, and this session's own testing
  showed a reasoning model (`openai/gpt-oss-20b:free`) needs a much larger
  budget to get past its thinking tokens to an actual answer. Revisit if usage
  shows 1024 is still too small for typical delegated tasks, or unnecessarily
  large for simple ones (cost signal).
- **Per-call timeout (120s, matching `juggler prompt`'s `promptTimeout`).**
  Revisit if real delegated tasks (likely longer/more complex than a
  hello-world) routinely need more.
- **Permission default (defer, no auto-allow).** Revisit toward an explicit
  auto-allow (scoped to specific pre-registered model names, not a blanket
  allow) if the per-call prompt proves too much friction in practice — but
  start conservative given the real-money cost per call.

## Explicitly out of scope

- Making Claude Code's native Task tool itself route to juggler models —
  confirmed impossible under the current single-process-endpoint constraint.
- Streaming tool output (juggler prompt's HTTP calls are already
  non-streaming; this tool inherits that).
- Auto-allow permission tiers, model-specific cost budgets, or any
  usage-based throttling — start with the simplest safe default (defer to
  native prompt) and revisit only if real usage demands it.
