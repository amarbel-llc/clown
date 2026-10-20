# Unused tools per session (toolcost follow-up)

Status: research / plan only. No code. Date: 2026-10-20.

Question: which tools were **loaded** into a session's context but never
**called**, and what fraction of context is tool definitions? Motivation:
`--cheap-context` — tool definitions cost context on every turn whether or not
they are used. The first question (which tools burn context) is already
answered by `clown-hook-toolcost` (`internal/toolcost`, `cmd/clown-hook-toolcost`).
Note: there is no dedicated toolcost plan/FDR file; the package doc in
`internal/toolcost/toolcost.go` is the design record.

Evidence below was read on this host from this session's transcript
(`~/.claude/projects/-home-sasha-eng-repos-clown--worktrees-kind-hemlock/a773136f-….jsonl`,
Claude Code `version":"2.1.301"`), `moxy status`, and stats-me series.
Anything not read is marked **UNVERIFIED**.

## TL;DR

1. The transcript **does** record the loaded tool list — the earlier assumption
   "transcript only has calls" is wrong for this Claude Code version. Records
   of `type":"api-request-shape"` carry `shape.tools[]` (full name + description
   + input_schema) and `shape.system[]`. Each `api-request` references one by
   `shapeHash`. So the toolcost hook can compute loaded-vs-called from the one
   file it already tails, with no new data source.
2. Called set = `tool_use` blocks, already parsed by the hook.
3. Definition size is **exact in bytes** (we have the schema as sent), only
   **estimable in tokens** (bytes/ratio, calibrated; a local tokenizer is not
   available for Claude models). Total tool+system+first-message cost is exact
   from the first request's usage.
4. Emit at final (SessionEnd/SubagentStop) only; per-tool *session counters*
   (not gauges) so the fleet query works in stats-me.
5. The data source is "the transcript's shape record", which is Claude-Code
   specific. For clown#263 (model management exclusively in juggler) put the
   extraction behind a small `LoadedTools` interface; juggler's own loop knows
   its tool list directly. See §5.

## 1. Where the LOADED set is observable

### 1a. Claude Code transcript — YES (verified)

Record types seen in this session's transcript (jq over the file): `user`,
`assistant`, `attachment` (subtypes `environment`, `skill_listing`,
`agent_listing_delta`, `mcp_instructions_delta`, `hook_success`, `prompt_snapshot`,
…), `api-request-shape`, `api-request`, `api-request-blob`, `last-prompt`, …

`api-request-shape` (2 in this session):

```
{"type":"api-request-shape","sessionId":"a773136f-…","shapeHash":"90c0d34b…",
 "timestamp":"2026-10-20T01:57:54.094Z","version":"2.1.301",
 "shape":{"system":[…],"tools":[…]}}
```

`shape` keys = `["system","tools"]`. Each tool object has keys
`description, eager_input_streaming, input_schema, name`. Observed:

| shapeHash | time | tools | tools JSON bytes | system JSON bytes |
|---|---|---|---|---|
| 90c0d34b… | 01:57:54 | 287 | 484,667 | 63,065 |
| 62d9630f… | 01:58:04 | 306 | 505,369 | 63,065 |

The second shape appears when the `claude.ai Krone` connector's 19 tools
(`mcp__claude_ai_Krone__*`) came online mid-session. **The loaded set is
dynamic within a session.** `api-request` records point at the shape in force
(`shapeHash` 90c0d34b for requests 1–4, 62d9630f for 5–8).

Per-prefix counts (shape 1): builtin 31, `plugin_moxy_moxy` 152, claude_ai_Fastmail 31,
claude_ai_Zoom_for_Claude 20, `plugin_spinclass_spinclass` 16, claude_ai_Claude_Docs 8,
`plugin_clown-builtin-jobs_ringmaster` 8, … Largest single definitions:
`mcp__claude_ai_Zoom_for_Claude__meeting_create` 35,928 B, `Artifact` 35,306 B,
`mcp__claude_ai_Zoom_for_Claude__meeting_update` 33,392 B.

Coverage: `rg '"type":"api-request-shape"' ~/.claude/projects` hits ~98 files
(main sessions and `subagents/agent-*.jsonl`), so subagent transcripts carry
their own shapes (which matters: the hook's SubagentStop reads
`agent_transcript_path`). Older transcripts / older Claude Code versions:
**UNVERIFIED** (I only saw version 2.1.301). The hook must treat "no shape
record" as "unknown", emitting nothing, not "nothing loaded".

Caveat: the shape is what Claude Code *intended* to send. See §3 for why byte
size may not equal what the model billed.

### 1b. moxy — partial (verified static; dynamic UNVERIFIED)

- `moxy status` on this host shows the merged moxyfile (`~/.config/moxy/moxyfile`:
  `chrest`; others empty) plus 16 moxins / 125 tools merged from MOXIN_PATH.
  That is the **configured/available** set, not what a given client saw.
- The session prompt's own moxy blurb lists per-child tool counts
  (chrest 19, chix 22, folio 16, …) and `moxy://tools/{server}` for discovery.
- moxy's own logs under `~/.local/log/moxy` exist (the directory listing was
  47,900 lines, so I did not read them). Whether they record the `tools/list`
  actually served per client: **UNVERIFIED**.
- moxy's view is only one server's contribution. The client's loaded set also
  includes builtins and other MCP servers (claude.ai connectors, spinclass,
  ringmaster…), so moxy cannot be the sole source. Use it only as a
  cross-check (moxy tool count in the shape should equal `moxy status` total
  for that moxyfile; here shape has 152 `plugin_moxy_moxy` tools vs. moxin
  125 + `chrest` 19 + moxy's own meta tools (async, batch, status, restart,
  …) — plausible but **I did not reconcile exactly**).

### 1c. clown session-start knowledge — partial

clown synthesizes the `clown-builtin-jobs` plugin (`cmd/clown/jobmonitor.go`) and
knows its plugin set/`--cheap-context` keep-set, but not builtin tools, claude.ai
connectors, or what moxy proxies. Not a complete source. Useful later as the
juggler-side source (§5).

### Decision

Primary source = transcript `api-request-shape` records. Moxy/clown knowledge
are cross-checks only.

## 2. Where the CALLED set is observable — confirmed

`internal/toolcost/toolcost.go` already parses every assistant `tool_use` block
(`id`, `name`, `input`) and maps names via `names.go` to keys
`builtin.<Name>` / `<server>.<tool>` (plugin segment dropped, e.g.
`moxy.folio_read`; non-plugin MCP keeps server, e.g. `claude_ai_Krone.x`).
The same mapping function must be applied to the shape's tool names so the two
sets compare on identical keys. Caveat: `names.go` only splits subkeys for two
tools; unused-detection must use the *base* key, not the subkeyed one.

Edge cases to handle: moxy tool names inside the shape are
`mcp__plugin_moxy_moxy__<child>_<tool>` → key `moxy.<child>_<tool>`; the
existing stats series confirm that form (`stats.clown.tool_result.flic.moxy.folio_read.ok`).
Calls through `moxy` meta tools (`async`, `batch`) hide the inner tool: an
`async`/`batch` call counts as a call of `moxy.async`/`moxy.batch` only. Inner
tools would appear "unused" though they were used. Mitigation: for those two
tools, also parse `input.tool` / `input.calls[].tool` and credit the inner name
(namespaced `folio.read` → `moxy.folio_read`). **Mapping rule UNVERIFIED** — needs a test against a real
transcript containing `moxy.batch`.

## 3. Per-tool definition cost

- **Bytes: exact.** `len(json(tool))` of each `shape.tools[i]` (`description` +
  `input_schema` + `name`). Verified computable (numbers above).
- **Tokens: estimated only.** Evidence from this transcript: first request usage
  is `input_tokens 2 + cache_creation 44,363 + cache_read 48,839 = 93,204`
  tokens for the *entire* prompt (system + tools + first user message). The shape
  alone is 484,667 + 63,065 ≈ 548 KB → ≥ 5.9 bytes/token even before counting the
  user turn, which is denser than JSON schema text normally tokenizes. So either
  the tokenizer compresses this text unusually well, or not all tool definitions
  are sent in full (the betas list in `api-request.params` includes
  `inline-tools-2026-09-15` and `mid-conversation-tool-changes-2026-07-01`, and the
  system prompt says "some tools are deferred"). **I cannot tell which from the
  transcript: UNVERIFIED.** This means `bytes/4` (what toolcost uses for results)
  would overstate tool-definition tokens and must not be presented as exact.
- **Total is exact, split is not.** Per-request total input is exact from `usage`.
  Per-tool tokens would need a calibration: ratio = (first-request total − known
  non-tool parts) / tool bytes. Cheaper and honest: report `definition_bytes`
  exactly, and a single per-session `tools_bytes_per_token` factor derived from a
  controlled calibration run (same prompt, tool set A vs. B, diff in first-request
  total). Local tokenization of Claude schemas is not possible without an API
  count-tokens call (an option, but it makes the hook network-dependent and
  billable — recommend against).
- Mid-session additions are not cleanly measurable from usage deltas: the Krone
  step went from 232,759 to 246,362 total input (+13,603), but that request also
  contains new messages and tool results, and the cache read dropped to 0
  (`cache_creation 246,360`), so a clean delta is unavailable. A calibration
  needs a dedicated experiment, not production data.

Recommendation: ship bytes as the exact primary metric; derive token estimates in
the query layer via an explicit, documented factor (so it can be revised without
re-emitting).

## 4. Proposed metrics

Naming follows existing families (`clown.<family>.<host>[.<repo>.<model>]…`;
tool key = `<server>.<tool>` as in `clown.tool_result.*`). Emitted **once per
transcript at final** (SessionEnd for main; SubagentStop for agent transcripts),
never on non-final Stop, because the loaded set can change until the end and
"unused" is only meaningful then. Idempotence follows the hook's existing
per-transcript state (add a `tools_emitted` flag in the state file).

Per-tool (cardinality ≈ hosts × ~300 tools, same order as existing
`tool_result` series):

| Metric | Type | Value |
|---|---|---|
| `clown.tools.loaded.<host>.<key>` | c | 1 per session in which the tool appeared in any shape |
| `clown.tools.called.<host>.<key>` | c | 1 per session in which the tool was called ≥ 1 time |
| `clown.tools.unused.<host>.<key>` | c | 1 per session where loaded and never called |
| `clown.tools.definition_bytes.<host>.<key>` | g | bytes of the definition (latest shape; per-session last value) |

Per-session aggregate (existing `<host>.<repo>.<model>` scheme):

| Metric | Type | Value |
|---|---|---|
| `clown.tools.count.<host>.<repo>.<model>` | g | tools in the final shape |
| `clown.tools.unused_count.<host>.<repo>.<model>` | g | loaded-but-never-called |
| `clown.tools.definition_bytes_total.<host>.<repo>.<model>` | g | Σ tool bytes in the final shape |
| `clown.tools.prompt_tokens_first.<host>.<repo>.<model>` | g | first request total input (exact) |

Why counters for per-tool unused: stats-me stores gauges as latest-value; a fleet
"never used" query needs a durable per-session count that sums over time.
Gauges (`stats.gauges.clown.context.…` is the existing pattern) are fine for the
aggregates.

### Queries (stats-me / VictoriaMetrics; series naming follows what exists here:
counters appear as `stats_counts.clown.<…>` and `stats.clown.<…>` (rate),
gauges as `stats.gauges.clown.<…>` — all verified for existing families;
the new families' names are proposed, **not yet emitted**)

Which tools are never used across the fleet (loaded in ≥ 1 session, called in
none) over 30 days:

```promql
sum by (__name__) (increase({__name__=~"stats_counts\\.clown\\.tools\\.loaded\\..*"}[30d]))
unless
sum by (__name__) (increase({__name__=~"stats_counts\\.clown\\.tools\\.called\\..*"}[30d]))
```

(The two name sets differ by the `loaded`/`called` segment, so in practice use
`label_replace` to normalize the metric name to the `<host>.<key>` suffix before
`unless`; or do it in jq over `/api/v1/export`. Finalize in implementation.)

Unused ratio per tool (high = candidate to drop from `--cheap-context` keep-set):

```promql
sum(increase(stats_counts_clown_tools_unused…[30d])) / sum(increase(…loaded…[30d]))
```

Fraction of the first request that is tool definitions (estimate; factor
`F` = bytes/token from calibration):

```promql
(stats.gauges.clown.tools.definition_bytes_total.* / F) / stats.gauges.clown.tools.prompt_tokens_first.*
```

Exact upper-bound sanity check available today with no new metrics: first-request
total input (93k here) vs. `clown.context` gauge (latest, up to 262k in this
session): the static prefix is the floor of every turn.

Dollar view: tool-definition tokens × cache-read price × turns ≈ carry cost, parallel
to the existing `carry_tokens` for results. Needs the request count
(`clown.responses.*` sum) — derivable in the query layer.

## 5. Moving to juggler (clown#263) — make it a swap

Design the extractor behind one narrow seam in `internal/toolcost`:

```go
// LoadedTools reports the tools the model could see over the session's life.
type LoadedTools interface {
    // Snapshots returns tool definitions in force, in order; each with the
    // time range or request id it applied to.
    Snapshots() ([]ToolSnapshot, error)
}
type ToolSnapshot struct {
    Name  string // raw provider name, mapped with names.go
    Bytes int    // exact size of the serialized definition
}
```

- Implementation A (now): `transcriptShapes`, reading `api-request-shape`
  records from the transcript the hook already tails.
- Implementation B (after #263): juggler's loop owns the tool list it sends in
  each request (it composes the request itself — FDR 0019's `juggler run`
  drives the tool-use loop against moxy), so it can emit the same
  `ToolSnapshot`s from its own state (or write an equivalent record to its
  transcript). Whether juggler transcripts will be Claude-Code-shaped:
  **UNVERIFIED**; decide when #263 lands.

Metric names and the emission step (loaded/called/unused counters) depend only
on the seam, so the swap touches the source adapter, not metrics or queries.
Keep names provider-neutral (`<server>.<tool>`), already true of `names.go`.

## Open items before implementation

1. Calibrate bytes→tokens (controlled two-run experiment; resolves the
   "deferred/inline tools" question in §3).
2. Confirm `api-request-shape` presence across Claude Code versions; define
   behaviour when absent (emit nothing).
3. Implement + test the `moxy.async`/`moxy.batch` inner-tool crediting (§2).
4. Reconcile moxy's served tools/list with the shape's moxy count; read moxy
   logs if they record `tools/list` per client.
5. Decide per-tool series cardinality budget (≈ 300 tools × hosts × 4 metrics).
6. Subagent shapes: confirm SubagentStop transcripts' shapes are the subagent's
   own (restricted) tool set, so subagent sessions are reported separately.

Out of scope: implementing these metrics, changing the toolcost hook, juggler.
