---
status: exploring
date: 2026-04-22
promotion-criteria: gemma3:12b runs a full Claude Code session on M2 Pro (16GB); qwen3:32b runs a full Claude Code session on Xeon VM (64GB RAM, CPU-only)
---

# Local Model Provider

## Problem Statement

Claude Code depends entirely on Anthropic's API, which requires internet access, incurs per-token cost, and sends code to external infrastructure. For use cases requiring air-gapped operation, cost control, or low-latency local inference, there is no path today to run a clown-managed coding session against a locally-hosted open model. This feature adds a `llama` provider to clown that manages a long-lived `llama-server` daemon and routes Claude Code through it via its Anthropic-compatible API shim.

## Interface

A new `--provider llama` (or `CLOWN_PROVIDER=llama`) dispatches to Claude Code backed by a local `llama-server` instance rather than Anthropic's API. The provider:

- Checks whether a `llama-server` daemon is already running (via pidfile and health check)
- If not running, starts the daemon with the configured model and waits for it to become ready
- Sets `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, and a dummy `ANTHROPIC_API_KEY` to point Claude Code at the local server
- Execs into `claude` as normal — the rest of clown's machinery (circus prompt injection, MCP plugin hosting) is unchanged

The daemon is long-lived: it survives after `clown` exits so subsequent sessions avoid the model load penalty. A separate `clown llama stop` subcommand tears it down explicitly.

Model selection and `llama-server` flags (context length, GPU layers, threads) are configured via a config file or environment variables, not per-invocation flags.

## Examples

```sh
# Start a session backed by gemma3:12b on M2 Pro
CLOWN_PROVIDER=llama clown

# Explicit provider flag
clown --provider llama

# Stop the daemon when done
clown llama stop

# Check daemon status
clown llama status
```

## Limitations

**Protocol translation required.** `llama-server` speaks OpenAI-compatible protocol; Claude Code expects Anthropic protocol. A translation layer must sit between them. The preferred approach is a lightweight proxy embedded in the clown binary (avoiding a separate process), but an external proxy is acceptable during the experimental phase.

**Single model at a time.** The daemon loads one model. Switching models requires stopping and restarting the daemon. Two concurrent sessions with different models are not supported.

**No GPU on the VM target.** The Xeon VM (64GB RAM) runs CPU-only inference. Token generation will be slow (~5–15 tok/s for 32b models) but acceptable for long-lived agentic sessions where the model thinks in bulk rather than interactively.

**Rejected model targets:**
- `gemma3:27b` on M2 Pro (16GB): ~16GB VRAM at Q4 — consumes the entire system RAM, leaves nothing for OS and other processes. Not viable as a daily driver.
- `qwen3:235b` on Xeon VM (64GB): Q4_K_M requires ~140GB+; Q2 degrades quality unacceptably. Not feasible on either target machine.

**Realistic tracer bullet targets:**
- M2 Pro (16GB unified memory): `gemma3:12b-it-qat` — fits comfortably, Metal acceleration
- Xeon VM (64GB RAM, CPU-only): `qwen3:32b` Q4 — fits with headroom

## More Information

- Prior art: `amarbel-llc/maneater` uses llama.cpp CGo bindings for embeddings — the CGo integration pattern is established in this org
- llama.cpp server API: speaks OpenAI-compatible `/v1/chat/completions`; requires a proxy to translate to Anthropic protocol for Claude Code
- Claude Code supports `ANTHROPIC_BASE_URL` for pointing at custom backends (Bedrock, Vertex, LLM gateways)
