package mcpcollapse

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"code.linenisgreat.com/clown/internal/mcphttp"
)

// enumerateBudget caps how long one upstream's initialize+tools/list handshake
// may take before it is abandoned and recorded as degraded. It is the default
// applied when NewAggregator is passed a non-positive perUpstreamTimeout, so a
// single hung upstream cannot block startup forever — mirroring pluginhost's
// toolCatalogFetchBudget, but as a fan-out-wide default rather than a constant
// hard-wired into every call.
const enumerateBudget = 3 * time.Second

// maxEnumerateBytes bounds a single upstream's initialize or tools/list response
// so a misbehaving or very large upstream cannot balloon memory during startup.
// Matches pluginhost's maxToolCatalogBytes.
const maxEnumerateBytes = 1 << 20

// Upstream names one MCP server the aggregator fronts: the caller-assigned
// server name (the {server} half of every dotted tool id it contributes) and
// the MCP HTTP URL where its tools live. The aggregator is handed URLs only —
// pluginhost owns spawning and lifecycle — so the name must be assigned by the
// caller wiring the upstreams, which is the layer that knows each server's
// plugin/server identity.
type Upstream struct {
	Name string
	URL  string
}

// DegradedUpstream records one upstream that failed to enumerate during the
// startup fan-out: its name and URL, plus the error that skipped it. The
// aggregator is fail-open, so a degraded upstream is dropped from the registry
// rather than aborting construction; a later task surfaces these names in a
// system-prompt fragment so the agent knows which servers are unreachable.
type DegradedUpstream struct {
	Name string
	URL  string
	Err  error
}

// Aggregator holds the result of the startup fan-out: the registry built from
// the upstreams that enumerated successfully and the list of those that did
// not. It is immutable after NewAggregator returns — the fan-out is complete by
// then, which IS the health gate — so the verbs (a later task) read it without
// locking.
type Aggregator struct {
	registry *Registry
	degraded []DegradedUpstream
}

// NewAggregator performs the startup fan-out across upstreams and returns the
// ready Aggregator once every upstream has either enumerated or been recorded as
// degraded. The constructor blocking until the fan-out completes IS the health
// gate: by the time it returns, Registry() and Degraded() are fully populated
// and no enumeration is still in flight.
//
// Each upstream is enumerated concurrently (initialize, then tools/list —
// sequential within one upstream because tools/list needs initialize's session
// id, but parallel across upstreams). Every upstream's handshake is bounded by
// perUpstreamTimeout (or enumerateBudget when that is non-positive), so one hung
// upstream cannot stall startup.
//
// Enumeration failure is fail-open: an upstream whose initialize or tools/list
// fails (transport error, non-200, malformed JSON-RPC) is skipped and recorded
// in Degraded rather than aborting construction — the registry is built only
// from the upstreams that enumerated. Construction fails outright only on a
// genuine config error: two upstreams sharing a server name, which the registry
// Builder rejects because it would silently drop one server's whole tool set.
func NewAggregator(ctx context.Context, upstreams []Upstream, perUpstreamTimeout time.Duration) (*Aggregator, error) {
	if perUpstreamTimeout <= 0 {
		perUpstreamTimeout = enumerateBudget
	}

	type enumResult struct {
		upstream Upstream
		tools    []ToolSpec
		err      error
	}

	results := make([]enumResult, len(upstreams))
	var wg sync.WaitGroup
	for i, up := range upstreams {
		wg.Add(1)
		go func(i int, up Upstream) {
			defer wg.Done()
			tools, err := enumerateUpstream(ctx, up, perUpstreamTimeout)
			results[i] = enumResult{upstream: up, tools: tools, err: err}
		}(i, up)
	}
	wg.Wait()

	// Build the registry from the successful upstreams in input order, so
	// AddServer sees a deterministic sequence, and collect the failures.
	var builder Builder
	var degraded []DegradedUpstream
	for _, res := range results {
		if res.err != nil {
			degraded = append(degraded, DegradedUpstream{
				Name: res.upstream.Name,
				URL:  res.upstream.URL,
				Err:  res.err,
			})
			continue
		}
		builder.AddServer(res.upstream.Name, res.upstream.URL, res.tools)
	}

	registry, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("mcpcollapse: building aggregator registry: %w", err)
	}

	return &Aggregator{registry: registry, degraded: degraded}, nil
}

// Registry returns the registry built from the upstreams that enumerated
// successfully. It is fully populated by the time NewAggregator returns.
func (a *Aggregator) Registry() *Registry {
	return a.registry
}

// Degraded returns the upstreams that failed to enumerate during the fan-out,
// each with the error that skipped it. It returns a fresh copy per call so a
// caller mutating the result cannot corrupt the Aggregator's internal slice.
// Empty when every upstream enumerated.
func (a *Aggregator) Degraded() []DegradedUpstream {
	out := make([]DegradedUpstream, len(a.degraded))
	copy(out, a.degraded)
	return out
}

// enumerateUpstream runs one upstream's MCP handshake: initialize (to establish
// a session and capture the Mcp-Session-Id the server may require echoed), then
// tools/list, parsing the result into the ToolSpecs the registry Builder
// consumes. Modeled on pluginhost.FetchToolCatalog, but keeps each tool's raw
// inputSchema (as json.RawMessage) rather than dropping it, because the
// aggregator hands that schema back verbatim from mcp_describe. Any failure is
// returned as an error for the caller to record as degraded — this function
// does not itself decide fail-open policy.
func enumerateUpstream(ctx context.Context, up Upstream, timeout time.Duration) ([]ToolSpec, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// initialize is required before tools/list is valid on a fresh session; the
	// response body is unused, but the Mcp-Session-Id header it returns (when
	// the server implements session continuity) must be echoed on tools/list or
	// that call 400s even though initialize succeeded.
	_, sessionID, err := mcphttp.PostJSONRPC(reqCtx, up.URL, "", `{"jsonrpc":"2.0","id":"mcp-collapse-init","method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"clown-mcp-collapse","version":"1"}}}`, maxEnumerateBytes)
	if err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}

	body, _, err := mcphttp.PostJSONRPC(reqCtx, up.URL, sessionID, `{"jsonrpc":"2.0","id":"mcp-collapse-tools","method":"tools/list","params":{}}`, maxEnumerateBytes)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}

	var parsed struct {
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parsing tools/list response: %w", err)
	}

	specs := make([]ToolSpec, 0, len(parsed.Result.Tools))
	for _, t := range parsed.Result.Tools {
		specs = append(specs, ToolSpec{
			Name:        t.Name,
			Description: t.Description,
			Schema:      t.InputSchema,
		})
	}
	return specs, nil
}
