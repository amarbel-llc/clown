---
status: proposed
date: 2026-07-27
---

# dumbo — Mock Model API for Provider Integration Tests

## Abstract

dumbo is a deterministic, scriptable HTTP server that impersonates an
OpenAI-compatible (and optionally Anthropic-compatible) model API, so that
integration tests can drive a real coding-agent frontend — `opencode`,
`crush`, and any other provider clown configures with a `base_url` — through
complete turns without a live model, a network dependency, or a paid API key.
Its primary output is not the responses it returns but the **journal of
requests it received**: what a frontend sends upstream is the observable proof
that clown wired that frontend correctly.

## Introduction

### The problem this solves

clown generates provider configuration and expects the provider to act on it.
Verifying that end to end has, until now, required either a live model or a
narrow probe.

FDR 0016 phase 1 (issue #202) shipped MCP tool injection for `opencode` and
`crush`. It was verified by `clown --provider opencode -- mcp list`, which
proves the provider parsed clown's `mcp` block, dialed the servers, and
completed MCP handshakes — a genuinely strong signal that needs no model at
all. Phase 1b (issue #203) can therefore build a bats regression lane on that
probe alone; **dumbo is not required for it**.

What that probe cannot show is anything that only becomes observable when the
agent takes a turn:

- **Did clown's MCP tools reach the model?** `mcp list` proves the *client*
  connected. It does not prove the tool definitions were forwarded in the
  request the frontend sends upstream.
- **Did a system-prompt fragment land?** FDR 0016 phase 2 injects static and
  plugin-contributed fragments via opencode's `instructions` and crush's
  `options.context_paths`. The only place that is observable is the `system`
  content of an outbound request.
- **Did an async wake actually push a turn?** FDR 0016 phase 3 needs a
  server-mode invocation model. Asserting that a wake produced a *new turn*
  requires seeing that turn arrive.

Each of those is a request-shape assertion. That is what dumbo exists to make
testable.

### Scope

This document specifies dumbo's HTTP surface, its script format, and its
journal format. It does not specify the bats lanes that consume it, nor
clown-side changes; those are tracked in issues #203 and #202 respectively.

### Non-goals

dumbo is a **test fixture**, not a model server, a proxy, or a local-inference
gateway. It MUST NOT be positioned as an alternative to `juggler` (local
llama-server management, FDR 0011) or to an OpenAI-compatible gateway. It
performs no inference and returns only what its script tells it to.

## Requirements Language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in RFC 2119.

## Specification

### 1. Invocation and lifecycle

dumbo MUST bind a TCP listener on loopback and MUST support binding an
ephemeral port (`:0`), so parallel test lanes never collide on a fixed port.

It MUST report its resolved address on startup in a form a test harness can
consume without racing the listener — either by writing a portfile whose
presence signals readiness, or by emitting a single line on stdout after the
listener is bound. `juggler`'s portfile convention
(`readJugglerPortfile`, `cmd/clown/opencode.go`) is the in-tree precedent and
is RECOMMENDED for consistency.

dumbo MUST exit non-zero if its script file is missing or malformed, rather
than starting and failing at first request — a fixture that starts and then
misbehaves produces a confusing test failure far from the cause.

### 2. HTTP surface

All paths are relative to the configured base URL. clown points providers at a
base URL ending in `/v1` (see `writeOpencodeConfigFile` and `writeCrushConfig`
in `cmd/clown`), so dumbo MUST serve its OpenAI-compatible routes beneath the
mount point it is given rather than assuming a bare root.

#### 2.1 OpenAI-compatible surface (REQUIRED)

This is the surface both target providers use today: `opencode` via the
`@ai-sdk/openai-compatible` npm provider, and `crush` via its `openai-compat`
provider type.

| Method | Path | Requirement |
|---|---|---|
| `POST` | `/chat/completions` | MUST — the turn endpoint |
| `GET` | `/models` | SHOULD — see §6 open question |

`POST /chat/completions` MUST accept a JSON body containing at least
`model`, `messages`, and OPTIONALLY `tools`, `tool_choice`, and `stream`.

When `stream` is absent or `false`, dumbo MUST respond `200` with a single
JSON object carrying `choices[0].message`.

When `stream` is `true`, dumbo MUST respond `200` with
`Content-Type: text/event-stream` and emit `data: <json>` events terminated by
a final `data: [DONE]`. Each event's payload MUST use the streaming delta
shape (`choices[0].delta`) rather than the non-streaming `message` shape.

dumbo MUST support streaming. Both target frontends are interactive TUIs and
stream by default; a non-streaming-only fixture would not exercise the path
under test.

#### 2.2 Anthropic-compatible surface (OPTIONAL)

`crush` also ships a builtin `anthropic` provider type, which clown selects
for `--provider crush --backend anthropic` (`crushBackendAnthropic`). An
implementation MAY serve `POST /messages` in the Anthropic Messages format to
cover that backend. It is OPTIONAL because clown's `anthropic` crush backend
authenticates against the real Anthropic API via `$ANTHROPIC_API_KEY` and does
not take a clown-supplied `base_url` today, so there is no configuration seam
to point it at dumbo without a clown-side change.

### 3. Script format

dumbo MUST be deterministic. It MUST NOT generate text, and its behavior MUST
be fully determined by its script plus the sequence of requests received.

A script is an ordered list of **turns**. dumbo maintains a cursor and MUST
consume one turn per `POST /chat/completions` request, in order.

Each turn MUST be one of:

- **`text`** — reply with assistant content. The canonical no-op turn.
- **`tool_call`** — reply with one or more tool invocations, naming the tool
  and its arguments. This is the turn type that makes clown's MCP wiring
  observable end to end: dumbo asks for a tool, the frontend routes the call
  to the clown-managed MCP server, and the result comes back in the next
  request's `messages`.
- **`error`** — reply with a non-2xx status and an error body, so tests can
  exercise the frontend's failure handling.

When the script is exhausted, dumbo MUST fail closed: respond with an error
identifying script exhaustion rather than looping, replaying the last turn, or
inventing a reply. A test that drives more turns than it scripted has a bug,
and silently absorbing that would hide it.

A turn MAY declare a `match` predicate constraining which request it applies
to (for example, requiring that the incoming `messages` contain a tool result
for a named tool). When a `match` is present and the incoming request does not
satisfy it, dumbo MUST fail the request rather than proceed, so ordering
violations surface at the point they occur.

Example script:

```json
{
  "turns": [
    {
      "kind": "tool_call",
      "calls": [
        { "name": "clown-builtin-jobs__ringmaster_job_status", "arguments": {} }
      ]
    },
    {
      "kind": "text",
      "match": { "has_tool_result_for": "clown-builtin-jobs__ringmaster_job_status" },
      "content": "done"
    }
  ]
}
```

### 4. Request journal

This is dumbo's primary assertion surface and its main reason to exist.

dumbo MUST record every request it receives, and MUST make the recording
available to a test after the frontend has exited. Writing newline-delimited
JSON to a path given at startup is RECOMMENDED, since it needs no client
library and is greppable from bats.

Each journal entry MUST capture at least:

| Field | Purpose |
|---|---|
| `path`, `method` | Which endpoint was hit |
| `model` | Confirms clown pinned the model it intended |
| `messages` | Carries the system prompt — the phase-2 assertion surface |
| `tools` | The advertised tool definitions — the phase-1/2 MCP assertion |
| `stream` | Confirms the streaming path was exercised |
| `authorization_present` | Whether a credential was sent |

The journal MUST NOT record credential *values*. It MUST record only whether
an `Authorization` header was present, never its contents. A fixture journal
is written to disk, is likely to be attached to a CI artifact or a bug report,
and a test key today is a real key tomorrow when someone points dumbo at a
gateway to compare behavior.

### 5. Conformance-relevant behavior

dumbo MUST NOT validate the caller's API key. clown supplies `"local"` as the
token for local backends (`cmd/clown/opencode.go`, `cmd/clown/crush.go`), and
a fixture that rejected it would fail for a reason unrelated to the test.

dumbo SHOULD echo a plausible `usage` object. Frontends commonly display token
counts, and a missing or malformed `usage` risks a frontend-side error that
looks like a clown bug.

dumbo SHOULD respond within a few milliseconds. Both providers apply request
timeouts, and clown's own MCP timeouts (30s default, translated per provider —
see FDR 0016) are unrelated to and much longer than anything dumbo should need.

## Security Considerations

dumbo is a test fixture that performs no authentication and returns
attacker-controlled-by-construction content. Three consequences follow:

**It MUST bind loopback only.** Binding a routable interface would expose an
unauthenticated endpoint that answers to any caller. There is no scenario in
which a test fixture needs off-host reachability; if a container needs to reach
it, that is a port-forwarding decision made explicitly by the harness, not a
default.

**It MUST NOT be reachable from a production clown session.** dumbo returns
scripted tool calls. A session that talked to dumbo believing it was a model
would execute whatever tool calls the script names, with the user's real tool
permissions. The `[providers] hermetic-config` work in FDR 0016 phase 0 exists
precisely because a repo-local config could silently repoint a provider at an
arbitrary URL; dumbo is exactly the kind of endpoint that finding was about.
Test harnesses MUST point providers at dumbo through explicit, test-scoped
configuration, never by writing into a user's real config.

**It MUST NOT log credential values** (§4). Journals outlive the test run.

dumbo performs no inference and holds no model weights, so it introduces no
supply-chain or model-provenance surface.

## Conformance Testing

Once implemented, conformance tests SHOULD live in `zz-tests_bats/` alongside
dumbo, using `bats-emo`'s `require_bin` for binary injection rather than a
hardcoded build path, so the suite survives a reimplementation:

    require_bin DUMBO_BIN dumbo

### Requirements worth covering

| Requirement | Description |
|---|---|
| §1, MUST exit non-zero on a malformed script | Fixture fails at startup, not at first request |
| §2.1, MUST support streaming | SSE framing terminated by `data: [DONE]` |
| §3, MUST fail closed on exhaustion | Over-driving the script is an error, not a replay |
| §3, MUST fail on unsatisfied `match` | Ordering violations surface where they occur |
| §4, MUST NOT record credential values | Journal contains no `Authorization` contents |
| Security, MUST bind loopback only | No routable listener |

## Open Questions

These MUST be settled before implementation; each is a place where this
document asserts a requirement that has not been verified against a real
frontend.

1. **Exact wire shape required by `@ai-sdk/openai-compatible`.** §2.1 specifies
   the conventional OpenAI streaming shape, but which fields that provider
   treats as mandatory (`id`, `created`, `finish_reason`, `usage`) has not been
   verified. Confirm against the SDK before implementing, not after.
2. **Does either frontend probe `/models` at startup?** §2.1 marks it SHOULD on
   the assumption that one of them might. clown pins a single model in the
   generated config, which may make discovery unnecessary. Unverified.
3. **Does crush's `openai-compat` client require streaming, or tolerate
   non-streaming?** Affects whether §2.1's non-streaming branch is reachable in
   practice for that provider.
4. **Where does dumbo live?** In-tree (`cmd/dumbo`) or a standalone repo in the
   ringmaster/troupe style (FDR 0014). This is deliberately unresolved: it
   depends on whether phase 3 needs dumbo on the ringmaster side too, which is
   not yet known. Deciding now would be a guess. In-tree is the cheaper start
   and can be extracted later, exactly as `internal/jobwake` was extracted to
   ringmaster.
5. **Is a companion stub MCP server also needed?** dumbo fakes the *model*, not
   the *tool server*. Phase 1b's lane can use clown's real built-in MCP servers
   (`ringmaster`, `troupe`), which is arguably better coverage; a stub would
   only be needed to assert against a controlled tool catalog.

## References

### Informative

- FDR 0016 — Non-Claude Provider Parity
  (`docs/features/0016-non-claude-provider-parity.md`), whose phases 2 and 3
  are dumbo's actual consumers.
- `docs/plans/2026-07-27-non-claude-provider-parity-design.md` — the phase 0/1
  design, including the evidence table distinguishing observed from
  source-read claims about both providers' config handling.
- RFC 0002 — Clown Plugin Protocol
  (`docs/rfcs/0002-clown-plugin-protocol.md`), for how clown-managed MCP
  servers reach a provider in the first place.
- Issue #203 — the phase-1b bats lane, which this document argues does **not**
  block on dumbo.
- Issue #206 — the non-hermetic-config finding, which motivates the
  reachability constraint in Security Considerations.
