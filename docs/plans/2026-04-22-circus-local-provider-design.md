# Design: circus — local model provider (PoC)

Date: 2026-04-22
Status: approved
Issue: https://github.com/amarbel-llc/clown/issues/16
FDR: docs/features/0001-local-model-provider.md

## Taxonomy

Three axes describe a clown session:

- **Harness** — the REPL/agent CLI (Claude Code, Codex, Crush, Thunderbolt). Owns tool execution, context window, UX.
- **Provider** — the API backend that serves tokens (Anthropic, OpenAI-compat, llama.cpp). Addressed via a base URL.
- **Model** — the specific model string passed to the provider (e.g. `claude-sonnet-4-6`, `gemma3:12b-it-qat`).

Today `--provider claude` conflates harness=claude-code + provider=anthropic. The new `--provider circus` value adds harness=claude-code + provider=circus (local llama-server) without changing the default.

For the PoC, only the Claude Code harness is supported.

## `circus` binary

`circus` is a new standalone Go binary at `cmd/circus`. It owns two concerns:

**Daemon management** — llama-server lifecycle via pidfile at `~/.local/state/circus/llama-server.pid`. Commands:

- `circus start` — idempotent: attaches to existing healthy daemon or launches a new one, then runs the clown-protocol handshake
- `circus stop` — SIGTERM the daemon, remove pidfile
- `circus status` — probe health endpoint, print URL or "not running"

No external pidfile library — implemented inline (~30 lines).

**Clown-protocol handshake** — when launched by clown, circus writes its `ANTHROPIC_BASE_URL` (e.g. `http://localhost:8080`) to stdout in the same format clown-plugin-host uses, then keeps running. Clown reads the port and injects it into Claude Code's environment.

**No proxy layer** — llama-server natively supports the Anthropic Messages API (`/v1/messages`). `ANTHROPIC_BASE_URL` points directly at llama-server; no translation needed.

**Model selection** — out of scope for PoC. Circus reads model from `CIRCUS_MODEL` env var or a config file.

## Clown integration

When `--provider circus` is selected:

1. Clown launches circus as a managed child (same lifecycle as plugin-host)
2. Circus performs the clown-protocol stdout handshake, announcing its `ANTHROPIC_BASE_URL`
3. Clown injects `ANTHROPIC_BASE_URL` into Claude Code's environment
4. Clown launches Claude Code as normal (plugin-host, MCP dirs, etc.)
5. On Claude Code exit, clown always shuts down circus. Circus decides internally whether to kill llama-server (if it spawned it) or leave it running (if it attached to an existing daemon).

`resolveProvider` in `main.go` gains a `"circus"` case. `buildcfg` gets a `CircusCliPath` ldflags var. The `runClaude` path is otherwise unchanged.

## Rename: `internal/circus` → `internal/promptwalk`

The existing `internal/circus` package (system-prompt fragment walker) is renamed to `internal/promptwalk` to free the `circus` name for the new binary and avoid confusion.

## Rollback strategy

`--provider circus` is opt-in. `--provider claude` (the default) is untouched. Rollback = stop using `--provider circus`. No revert needed.

**Promotion criteria** (from FDR): `gemma3:12b-it-qat` completes a full Claude Code session on M2 Pro (16GB); `qwen3:32b` Q4 completes a full session on Xeon VM (64GB RAM, CPU-only).

## Out of scope for PoC

- Model selection flag (use `CIRCUS_MODEL` env var)
- Harness generalization (Codex, Crush, Thunderbolt)
- Proxy/translation layer
- `circus` as MCP tool provider
