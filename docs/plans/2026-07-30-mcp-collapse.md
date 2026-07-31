# MCP-Collapse Mode (`clown --mcp-collapse`) Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** Add an opt-in `clown --mcp-collapse` mode that aggregates N upstream MCP servers behind a fixed 3-tool surface (`mcp_list` / `mcp_describe` / `mcp_call`), collapsing dozens of per-tool schemas into three generic verbs to save agent context.

**Architecture:** A new `cmd/clown-mcp-collapse` binary is both an MCP-over-HTTP *server* to the harness and an MCP *client* to N pluginhost-launched upstream URLs. The bridge's server spine and host's JSON-RPC client are first extracted into a shared `internal/mcphttp` package so the 1-child `clown-stdio-bridge` becomes the N=1 case and the aggregator the N>1 case (enabling a v2 subsume). Zero go-mcp dependency — built entirely on clown's existing MCP plumbing.

**Tech Stack:** Go, `net/http` streamable-HTTP + SSE (existing `cmd/clown-stdio-bridge`), `postJSONRPC`/`extractSSEData` (existing `internal/pluginhost/host.go`), Nix `buildGoApplication` (flake.nix), bats integration suite (bats.nix, with the existing `mock-stdio-mcp` fixture).

**Rollback:** Purely additive behind an opt-in flag. Absent `--mcp-collapse`, no behavior changes. Revert = delete `cmd/clown-mcp-collapse`, the flag wiring, and (optionally) re-inline `internal/mcphttp` back into the bridge. The `internal/mcphttp` extraction is behavior-preserving for the bridge and can stay even if the aggregator is reverted.

---

## Background & decisions (from the grill)

Read `docs/features/0002-stdio-mcp-bridge.md`, `docs/features/0003-plugin-contributed-prompt-fragments.md`, and `docs/rfcs/0002-clown-plugin-protocol.md` before starting — they define the bridge, the system-prompt-fragment mechanism, and the plugin protocol this mode rides on.

Locked decisions:
- **Schema-less `mcp_call` is accepted.** The aggregator VALIDATES `args` against the stored upstream schema inside `mcp_call` and returns a shaped error; `mcp_describe` output is strongly shaped. (The registry must therefore store each tool's `inputSchema` — note `internal/pluginhost/host.go`'s `ToolInfo` at ~:281 drops it; the registry needs a richer struct.)
- **Progress-forwarding regression is permitted for the prototype.** Today the bridge forwards a child's `notifications/progress` via its GET broadcast stream (`cmd/clown-stdio-bridge/translator.go` `demux`→`broadcast`). A collapsed `mcp_call` over `postJSONRPC`+`extractSSEData` keeps only the final `data:` event, so upstream progress is LOST — a regression vs direct-wire, tracked as a followup, fixed at v1. `mcp_call` should still send a progressToken upstream so heartbeat-on-progress upstreams keep their stream warm.
- **Reject duplicate server names** at startup (clear error naming both); first-wins + logged warning only as a last-resort tiebreaker.
- **Aggregator gets URLs only.** pluginhost owns upstream spawn/handshake/healthz/shutdown. v2 unifies.
- **Fail-open** on a failed upstream `tools/list`: skip it, log it, and NAME it in the dynamic system-prompt append (primary) + `mcp_list` (secondary). The aggregator HEALTH-GATES on its own enumeration completing, so the fragment GET can report failures.
- **Verbatim** upstream `result` passthrough; shaped MCP error only for aggregator-originated failures.
- **Ultra-lean `mcp_list`** (name + one-line description, grouped by server, `query`/`server` filters); full schema deferred to `mcp_describe`.
- **All-or-nothing collapse** for the prototype; selective collapse is v1, driven by en-vivo testing (not designed speculatively).

Related tracked work (separate deliverable, does NOT block this plan): the `clown tokens` subcommand that measures the token savings — filed as its own issue.

---

## Task 1: Extract `internal/mcphttp` server spine from `clown-stdio-bridge`

**Promotion criteria:** The bridge compiles and its bats suite passes using the extracted package; no behavior change. This is the prerequisite refactor — the aggregator reuses it in Task 3+.

**Files:**
- Create: `internal/mcphttp/server.go` (the HTTP handler + SSE + origin from `cmd/clown-stdio-bridge/http.go`)
- Create: `internal/mcphttp/server_test.go`
- Modify: `cmd/clown-stdio-bridge/http.go` (delegate to `internal/mcphttp`)
- Modify: `cmd/clown-stdio-bridge/main.go` (wire the extracted handler)

**Step 1: Write a failing test** for a minimal `mcphttp.Server` that serves a canned `tools/list` JSON-RPC response over `POST /mcp` and validates loopback origin. Assert: a POST with a `tools/list` body returns the canned response; a non-loopback `Origin` header returns 403 (mirror `validateOrigin`, `cmd/clown-stdio-bridge/http.go:619`).

**Step 2: Run it, verify it fails** (`mcphttp` package doesn't exist).
Run: `go test ./internal/mcphttp/...` — Expected: FAIL (no such package).

**Step 3: Extract the spine.** Move `validateOrigin`, `writeJSONRPCError`, `jsonRPCError`/`jsonRPCErrorObj`, the `handleMCP`/`handlePost`/`handleGet` request/response shape, and the SSE framing out of `cmd/clown-stdio-bridge/http.go` into `internal/mcphttp/server.go`, parameterized by a request handler interface (something like `HandleRequest(ctx, method string, body []byte) (json.RawMessage, error)`) so the *upstream transport* (single stdio child vs. N HTTP upstreams) is pluggable. Keep the heartbeat/streaming behavior. The bridge's `translator.SendRequest` becomes one implementation of that interface.

**Step 4: Re-point the bridge** at `internal/mcphttp`, passing a translator-backed handler. `go build ./cmd/clown-stdio-bridge/...` — Expected: builds.

**Step 5: Run tests.** `go test ./internal/mcphttp/... ./cmd/clown-stdio-bridge/...` — Expected: PASS.

**Step 6: `git add internal/mcphttp && git commit`** — `refactor: extract mcphttp server spine from clown-stdio-bridge`. NOTE: `git add` the new dir or `nix build` won't see it.

---

## Task 2: Extract the JSON-RPC client into `internal/mcphttp`

**Promotion criteria:** `internal/pluginhost` uses the extracted client for `FetchToolCatalog`/`postJSONRPC`; existing pluginhost tests pass.

**Files:**
- Create: `internal/mcphttp/client.go` (move `postJSONRPC`, `extractSSEData`, `mcpSessionIDHeader` from `internal/pluginhost/host.go:346-410`)
- Create: `internal/mcphttp/client_test.go`
- Modify: `internal/pluginhost/host.go` (call `mcphttp.PostJSONRPC` / re-export)

**Step 1: Write a failing test** for `mcphttp.PostJSONRPC(ctx, url, sessionID, body)` against an `httptest.Server` that answers (a) plain `application/json` and (b) `text/event-stream` with heartbeat events before the final `data:` — assert both parse to the final JSON-RPC body, and that the `Mcp-Session-Id` response header is returned. This mirrors the existing behavior at `internal/pluginhost/host.go:361-410`.

**Step 2: Run it, verify it fails.**
Run: `go test ./internal/mcphttp/... -run PostJSONRPC` — Expected: FAIL.

**Step 3: Move the client.** Relocate `postJSONRPC` + `extractSSEData` into `internal/mcphttp/client.go` as exported `PostJSONRPC` / `ExtractSSEData`; keep `internal/pluginhost/host.go` calling them (thin wrapper or direct call). Preserve `maxToolCatalogBytes` bounding.

**Step 4: Verify pluginhost still builds/tests.** `go build ./internal/pluginhost/... && go test ./internal/pluginhost/...` — Expected: PASS.

**Step 5: Commit** — `refactor: extract mcphttp JSON-RPC client from pluginhost`.

---

## Task 3: The registry + naming (copied idea from moxy, re-implemented)

**Promotion criteria:** N/A (new).

**Files:**
- Create: `internal/mcpcollapse/registry.go` (dotted `{server}.{tool}` id, first-wins, duplicate-server rejection)
- Create: `internal/mcpcollapse/registry_test.go`

**Step 1: Write failing tests:**
- `Register("grit", tool{name:"commit", schema, desc})` → id `grit.commit`; `Lookup("grit.commit")` returns the entry (url, real name, schema, desc).
- Registering two servers both named `grit` → `Build` returns an error naming both.
- Two tools same dotted id within Build → first-wins + a recorded warning (last-resort path).

**Step 2: Run, verify fail.** `go test ./internal/mcpcollapse/... -run Registry` — Expected: FAIL.

**Step 3: Implement** the `Registry` and `Entry{Server, Tool, URL, Schema json.RawMessage, Description string}`. Reject duplicate server names in `Build`.

**Step 4: Run, verify pass.**

**Step 5: Commit** — `feat: mcp-collapse registry with dotted ids + dup-server rejection`.

---

## Task 4: Aggregator startup — fan-out enumeration + health gate

**Promotion criteria:** N/A.

**Files:**
- Create: `internal/mcpcollapse/aggregator.go` (init: for each upstream URL, `initialize`+`tools/list` via `mcphttp.PostJSONRPC`, build registry, record failures)
- Create: `internal/mcpcollapse/aggregator_test.go`

**Step 1: Write a failing test** using two `httptest.Server` upstreams (one healthy returning a 2-tool `tools/list`, one returning a 500). Assert: `NewAggregator(urls)` builds a registry with the healthy server's 2 tools, records the failed server in a `Degraded()` list (fail-open, Q5), and only reports ready AFTER enumeration completes (health-gate). Reuse the `Mcp-Session-Id` echo (Task 2).

**Step 2: Run, verify fail.**

**Step 3: Implement** the fan-out (mirrors `FetchToolCatalog`'s two calls, `internal/pluginhost/host.go:308`), fail-open per upstream, health-gate on completion.

**Step 4: Run, verify pass.**

**Step 5: Commit** — `feat: mcp-collapse aggregator startup fan-out + fail-open`.

---

## Task 5: The three verbs — `mcp_list`, `mcp_describe`, `mcp_call`

**Promotion criteria:** N/A.

**Files:**
- Modify: `internal/mcpcollapse/aggregator.go` (implement the request handler: `tools/list` returns the 3 verbs; `tools/call` dispatches the verbs)
- Modify: `internal/mcpcollapse/aggregator_test.go`

**Step 1: Write failing tests:**
- `tools/list` returns EXACTLY 3 tools (`mcp_list`, `mcp_describe`, `mcp_call`), each with a strongly-shaped inputSchema.
- `mcp_call{tool:"mcp_list"}` → ultra-lean rows (id + one-line desc), grouped by server, filterable by `query`/`server`, and lists degraded servers.
- `mcp_call{tool:"mcp_describe", tool_id:"grit.commit"}` → the stored schema + description.
- `mcp_call{tool:"mcp_call", tool_id:"grit.commit", args:{...valid...}}` → validates against stored schema, dispatches a real `tools/call` to the owning upstream (httptest), returns the upstream `result` VERBATIM.
- `mcp_call` with args violating the schema → a shaped aggregator error (`isError`), NOT dispatched upstream.
- `mcp_call` with an unknown `tool_id` → shaped error.

**Step 2: Run, verify fail.**

**Step 3: Implement.** The handler branches on the verb; `mcp_call` does schema validation then `mcphttp.PostJSONRPC` to the owning upstream (send a progressToken upstream per Q2), returning the upstream result verbatim. Aggregator-originated failures → shaped MCP error only.

**Step 4: Run, verify pass.**

**Step 5: Commit** — `feat: mcp-collapse three-verb surface (list/describe/call)`.

---

## Task 6: The `clown-mcp-collapse` binary + system-prompt fragment

**Promotion criteria:** N/A.

**Files:**
- Create: `cmd/clown-mcp-collapse/main.go` (bind loopback, print the clown handshake line like the bridge, serve `internal/mcphttp` over the aggregator; serve the `systemPromptPath` GET)
- Modify: `flake.nix` (add a `clown-mcp-collapse` `buildGoApplication` mirroring `clown-stdio-bridge` at :306; bundle it into the symlinkJoins at ~:1037 and ~:1072; export a `CLOWN_MCP_COLLAPSE_BIN` like :415)
- Modify: `bats.nix` (add `cmd/clown-mcp-collapse` to the built subPackages ~:372, wire the binary ~:91 like the bridge)

**Step 1: Write a failing test** for `main`'s handshake + a GET on the system-prompt path returning the steering fragment ("MCP tools are collapsed — use mcp_list/mcp_describe/mcp_call") PLUS the degraded-server list (Q5).

**Step 2: Run, verify fail.**

**Step 3: Implement** `main.go` (reuse the bridge's handshake/listen pattern from `cmd/clown-stdio-bridge/main.go`), wire `flake.nix` + `bats.nix`. `git add cmd/clown-mcp-collapse` BEFORE `nix build`.

**Step 4: Verify** `go build ./cmd/clown-mcp-collapse/...` then `just build` (the authoritative Nix check — bundles the new binary).

**Step 5: Commit** — `feat: clown-mcp-collapse binary + steering fragment`.

---

## Task 7: Opt-in `--mcp-collapse` flag wiring

**Promotion criteria:** N/A. Absent the flag, zero behavior change.

**Files:**
- Modify: `cmd/clown/main.go` (parse `--mcp-collapse`; when set, instead of registering each upstream as its own plugin, synthesize ONE plugin pointing at `clown-mcp-collapse` handed the upstream URLs)
- Modify: `internal/pluginhost/host.go` or the compile path as needed to route the synthesized single-aggregator plugin
- Test: bats integration test under `zz-tests_bats/`

**Step 1: Write a failing bats test** (all-or-nothing, Q11): launch clown `--mcp-collapse` over two `mock-stdio-mcp` upstreams (the fixture already in bats.nix:135); assert the harness sees exactly the 3 verbs; `mcp_list` shows both catalogs; `mcp_describe` returns a real schema; `mcp_call` round-trips a real result. (This IS demo step 1.)

**Step 2: Run, verify fail.**

**Step 3: Implement** the flag + synthesis. Absent the flag: untouched path.

**Step 4: Run** the bats lane. Then `just build` (the pre-merge gate runs the full suite).

**Step 5: Commit** — `feat: opt-in --mcp-collapse flag (all-or-nothing)`.

---

## Task 8: File the progress-forwarding regression followup + demo step 2

**Files:**
- Followup issue (via eng:file-issue): "mcp-collapse loses upstream progress notifications — restore before v1" describing the Q2 regression (bridge forwards child progress via GET broadcast; collapsed mcp_call over postJSONRPC keeps only the final event). Also add a TaskCreate entry per the user's mid-sequence-followup convention.
- Demo step 2 (manual, not a code task): wrap the real clownfile-declared plugins behind `--mcp-collapse` and confirm en vivo.

---

## Open risks to verify during build (facts, not decisions)

- **New files invisible to `nix build`** until `git add`ed — applies to every Create above.
- **`ToolInfo` drops `inputSchema`** (`internal/pluginhost/host.go` ~:281) — the registry needs its own richer struct (Task 3).
- **`Mcp-Session-Id` echo** — reuse `FetchToolCatalog`'s handling (Task 2/4).
- **`just build` is the only trustworthy build check** — `build-go`/`update-gomod2nix` can fail for bridged-dep reasons unrelated to this change (see AGENTS.md).
