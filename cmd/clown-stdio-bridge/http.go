package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"code.linenisgreat.com/clown/internal/mcphttp"
)

// httpHandler owns the bridge-specific HTTP surface: the tool-exclusion
// control endpoint (--cheap-context) and the tools/list response filter it
// feeds into the reusable mcphttp.Server spine. The streamable-HTTP
// request/response machinery — POST/GET/DELETE routing, SSE framing,
// heartbeats, origin checks, JSON-RPC error framing — lives in
// internal/mcphttp; this handler wires the bridge's *translator into it as
// the RequestHandler and supplies the filter as a ResponseFilter.
//
// mcp holds the constructed spine so main.go can route /mcp at it via
// mcp.HandleMCP; the httpHandler retains only the exclude-tools set and its
// endpoint.
type httpHandler struct {
	t      *translator
	logger logger
	// stats emits per-request duration + outcome metrics to statsd
	// (stats-me). Nil disables emission (see statsd.go).
	stats *statsdClient

	// mcp is the reusable MCP-over-HTTP spine, constructed once from this
	// handler's translator/logger/stats/filter (see newHTTPHandler). main.go
	// routes /mcp at mcp.HandleMCP.
	mcp *mcphttp.Server

	// excludeTools backs --cheap-context v2's per-tool filtering
	// (cmd/clown/cheapcontext.go): a set of MCP tool names that the tools/list
	// response filter strips from every tools/list response before writing it
	// out, so a
	// deselected tool never reaches claude's discovery at all. Empty by
	// default (no filtering). Set via POST /clown/exclude-tools, which
	// REPLACES the set wholesale — one picker decision per launch, no
	// incremental add/remove. Guarded by excludeMu since it's written from
	// the HTTP handler goroutine handling that POST and read from every
	// concurrent tools/list request.
	//
	// Wire contract matches moxy's independently-shipped
	// /clown/exclude-tools (amarbel-llc/moxy#399,
	// docs/features/0010-tool-exclude-endpoint.md in that repo): body key
	// "exclude", GET/POST both echo back the resulting set with 200 — so
	// clown's client (cmd/clown/cheapcontext.go pushExcludeTools) has one
	// contract regardless of whether the target is this bridge or a native
	// httpServers plugin like moxy that implements the route itself.
	excludeMu    sync.RWMutex
	excludeTools map[string]bool
}

// excludeToolsBody is the JSON shape of both the POST request and the
// GET/POST response for /clown/exclude-tools, matching moxy's
// independently-shipped contract (amarbel-llc/moxy#399) exactly so clown's
// client needs only one code path.
type excludeToolsBody struct {
	Exclude []string `json:"exclude"`
}

// setExcludeTools replaces the handler's tool-exclusion set wholesale.
func (h *httpHandler) setExcludeTools(names []string) {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	h.excludeMu.Lock()
	h.excludeTools = set
	h.excludeMu.Unlock()
}

// excludedToolNames returns the current exclusion set as a sorted-by-nothing
// flat list, for the GET/POST readback response.
func (h *httpHandler) excludedToolNames() []string {
	h.excludeMu.RLock()
	defer h.excludeMu.RUnlock()
	if len(h.excludeTools) == 0 {
		return nil
	}
	names := make([]string, 0, len(h.excludeTools))
	for n := range h.excludeTools {
		names = append(names, n)
	}
	return names
}

// handleExcludeTools implements GET/POST /clown/exclude-tools, matching
// moxy's independently-shipped contract (amarbel-llc/moxy#399): GET reads
// back the current excluded set, POST replaces it wholesale and echoes the
// resulting set — both respond 200 with excludeToolsBody.
func (h *httpHandler) handleExcludeTools(w http.ResponseWriter, r *http.Request) {
	if !mcphttp.ValidateOrigin(r) {
		http.Error(w, "origin not permitted", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(excludeToolsBody{Exclude: h.excludedToolNames()})
	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var req excludeToolsBody
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		h.setExcludeTools(req.Exclude)
		h.logger.Printf("clown-stdio-bridge: exclude-tools set to %d tool(s)", len(req.Exclude))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(excludeToolsBody{Exclude: h.excludedToolNames()})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// filterToolsListResponse drops entries from a tools/list JSON-RPC response
// body whose name is in the current exclude set. Returns the body
// unmodified (including on any parse error — fail open, since a malformed
// response is the wrapped server's problem, not this filter's to surface)
// when the exclude set is empty or the body isn't a tools/list result
// shape.
func (h *httpHandler) filterToolsListResponse(body json.RawMessage) json.RawMessage {
	h.excludeMu.RLock()
	exclude := h.excludeTools
	h.excludeMu.RUnlock()
	if len(exclude) == 0 {
		return body
	}

	var parsed struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return body
	}
	if len(parsed.Result.Tools) == 0 {
		return body
	}

	filtered := make([]json.RawMessage, 0, len(parsed.Result.Tools))
	for _, raw := range parsed.Result.Tools {
		var probe struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			filtered = append(filtered, raw) // fail open per-entry too
			continue
		}
		if !exclude[probe.Name] {
			filtered = append(filtered, raw)
		}
	}

	// Re-marshal by patching just the tools array back into the original
	// structure via a generic map, so any other result fields (e.g. a future
	// nextCursor) survive untouched.
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(body, &generic); err != nil {
		return body
	}
	var resultGeneric map[string]json.RawMessage
	if err := json.Unmarshal(generic["result"], &resultGeneric); err != nil {
		return body
	}
	toolsJSON, err := json.Marshal(filtered)
	if err != nil {
		return body
	}
	resultGeneric["tools"] = toolsJSON
	resultBytes, err := json.Marshal(resultGeneric)
	if err != nil {
		return body
	}
	generic["result"] = resultBytes
	out, err := json.Marshal(generic)
	if err != nil {
		return body
	}
	return out
}

// bridgeStats adapts the bridge's *statsdClient to mcphttp.Stats so the spine
// can emit the bridge's per-request metrics without importing statsd. A nil
// underlying client is nil-safe (metricLabel is pure; the EmitOutcome forward
// is itself a no-op on a nil *statsdClient).
type bridgeStats struct {
	c *statsdClient
}

func (b bridgeStats) Label(method string, body []byte) string {
	return metricLabel(method, body)
}

func (b bridgeStats) EmitOutcome(label string, started time.Time, outcome string) {
	b.c.emitOutcome(label, started, outcome)
}

// newHTTPHandler builds the bridge's HTTP handler and its embedded mcphttp
// spine over the given translator. The spine is configured with the bridge's
// log prefix, resolved heartbeat policy (from the bridge's CLOWN_BRIDGE_*
// env vars), statsd adapter, and the tools/list exclusion filter so the
// externally observable behavior is identical to the pre-extraction inline
// handler.
func newHTTPHandler(t *translator, log logger, stats *statsdClient) *httpHandler {
	h := &httpHandler{t: t, logger: log, stats: stats}
	h.mcp = mcphttp.NewServer(mcphttp.Config{
		Handler:   t,
		Logger:    log,
		LogPrefix: "clown-stdio-bridge",
		Heartbeat: resolveHeartbeat(),
		Stats:     bridgeStats{c: stats},
		Filter:    h.filterResponse,
	})
	return h
}

// filterResponse is the ResponseFilter the bridge hands the spine: it applies
// the tool-exclusion filter to tools/list responses only, leaving every other
// method's response untouched — matching the pre-extraction inline behavior.
func (h *httpHandler) filterResponse(method string, body json.RawMessage) json.RawMessage {
	if method != "tools/list" {
		return body
	}
	return h.filterToolsListResponse(body)
}

// handleMCP routes the /mcp endpoint at the mcphttp spine. Retained as a
// method so the wiring in main.go and the bridge's tests keep a stable entry
// point.
func (h *httpHandler) handleMCP(w http.ResponseWriter, r *http.Request) {
	h.mcp.HandleMCP(w, r)
}
