package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// promptFetchBudget caps how long FetchPromptFragments will block the launch
// while collecting dynamic system-prompt fragments. A prompt fragment is not
// load-bearing for the session booting, so this is deliberately short — far
// below the 30 s healthcheck timeout — and a server that overruns it is
// simply skipped (degrade to static).
const promptFetchBudget = 3 * time.Second

// maxPromptFragmentBytes bounds a single fragment body so a misbehaving
// server cannot balloon the prompt.
const maxPromptFragmentBytes = 64 * 1024

// toolCatalogFetchBudget caps how long FetchToolCatalog will block per
// server while fetching its tool list for --cheap-context's picker. Not
// load-bearing for the session booting (a fetch failure just means that
// server's picker entry has no per-tool grouping, per FetchToolCatalog's
// doc comment) so this stays short like promptFetchBudget.
const toolCatalogFetchBudget = 3 * time.Second

// maxToolCatalogBytes bounds a tools/list response body so a misbehaving
// or very large server cannot balloon memory while --cheap-context is
// building its picker.
const maxToolCatalogBytes = 1024 * 1024

type DiscoveredServer struct {
	PluginDir  string
	PluginName string
	ServerName string
	Def        ServerDef
}

// Name returns the canonical "<plugin>/<server>" identifier used for logs
// and messages.
func (d DiscoveredServer) Name() string {
	return d.PluginName + "/" + d.ServerName
}

type StartFailure struct {
	Server DiscoveredServer
	Err    error
}

// StartReport is the outcome of Host.StartAll. Started holds the servers
// that came up healthy (also mirrored into h.Servers). Failed holds one
// entry per DiscoveredServer that never started, failed the handshake, or
// failed the healthcheck.
type StartReport struct {
	Started []*ManagedServer
	Failed  []StartFailure
}

type Host struct {
	PluginDirs []string
	Logger     *slog.Logger
	Servers    []*ManagedServer
	// BridgePath, when set, is the absolute path to clown-stdio-bridge.
	// It is required when any discovered clown.json declares stdioServers
	// entries; Discover passes it to Desugar so those entries are
	// rewritten as httpServers entries pointing at the bridge.
	BridgePath string

	// BaseEnv is clown-injected identity (CLOWN_SESSION_ID, CLOWN_BIN) applied
	// to every managed server clown spawns (clown#136). Threading it here is
	// what lets clown stop stamping the per-instance key onto its own process
	// env — producers get it explicitly, while the claude subtree (bash,
	// subagents) no longer inherits it.
	BaseEnv map[string]string

	// URLHostRewrite, when non-empty, replaces the host portion of
	// each MCP server URL written into the compiled plugin manifest
	// (the URL component sent to claude-code, NOT the address
	// plugin-host itself dials when starting/healthchecking the
	// server). The port and path are preserved.
	//
	// Use case: when claude-code runs inside a container that
	// cannot resolve the host-side loopback its plugin-host bound
	// to. The canonical example is `clown --tent` on darwin: the
	// plugin servers bind to 127.0.0.1 on the mac, but the
	// container's 127.0.0.1 is the podman-machine VM's loopback,
	// not the mac's. Setting URLHostRewrite to
	// "host.containers.internal" routes claude's requests through
	// gvproxy back to the mac. See amarbel-llc/clown#70.
	//
	// Empty disables rewriting (current default on linux native).
	URLHostRewrite string

	// compiledDirs tracks staging directories produced by
	// CompileForClaude; Shutdown removes them.
	compiledDirs []string

	// monitorsByDir holds each discovered plugin's monitor declarations
	// keyed by plugin dir. Populated by Discover even when the plugin
	// has no MCP servers, so monitors-only plugins still flow through
	// CompileForClaude.
	monitorsByDir map[string][]MonitorDef
}

func (h *Host) Discover() ([]DiscoveredServer, error) {
	h.monitorsByDir = make(map[string][]MonitorDef)
	var found []DiscoveredServer
	for _, dir := range h.PluginDirs {
		cfg, err := LoadClownConfig(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("plugin dir %s: %w", dir, err)
		}

		if err := Desugar(cfg, h.BridgePath, dir); err != nil {
			return nil, fmt.Errorf("plugin dir %s: %w", dir, err)
		}

		pluginName, err := PluginName(dir)
		if err != nil {
			return nil, fmt.Errorf("plugin dir %s: %w", dir, err)
		}

		if len(cfg.Monitors) > 0 {
			h.monitorsByDir[dir] = cfg.Monitors
		}

		for serverName, def := range cfg.HTTPServers {
			found = append(found, DiscoveredServer{
				PluginDir:  dir,
				PluginName: pluginName,
				ServerName: serverName,
				Def:        def,
			})
		}
	}
	return found, nil
}

// StartAll launches every discovered server concurrently and returns a
// StartReport describing which came up healthy and which did not. It does
// not call Shutdown on partial failure: the caller decides whether to
// continue with the healthy subset, prompt the user, or abort and shut
// down. Servers that started successfully are also stored on h.Servers so
// ServerURLs() and Shutdown() keep working.
func (h *Host) StartAll(ctx context.Context, discovered []DiscoveredServer) StartReport {
	type startResult struct {
		server *ManagedServer
		src    DiscoveredServer
		err    error
	}

	results := make(chan startResult, len(discovered))
	var wg sync.WaitGroup

	for _, d := range discovered {
		wg.Add(1)
		go func(d DiscoveredServer) {
			defer wg.Done()
			srv := &ManagedServer{
				Name:      d.Name(),
				Def:       d.Def,
				PluginDir: d.PluginDir,
				Logger:    h.Logger,
				BaseEnv:   h.BaseEnv,
			}
			err := srv.Start(ctx)
			results <- startResult{server: srv, src: d, err: err}
		}(d)
	}

	wg.Wait()
	close(results)

	var report StartReport
	for res := range results {
		if res.err != nil {
			report.Failed = append(report.Failed, StartFailure{Server: res.src, Err: res.err})
		} else {
			report.Started = append(report.Started, res.server)
			h.Servers = append(h.Servers, res.server)
		}
	}
	return report
}

// serverEntryForManaged builds an MCPServerEntry from a running server's
// handshake. The Type field maps "streamable-http" to "http"; other
// protocols pass through unmodified so schema errors are legible. When
// h.URLHostRewrite is non-empty, the URL's host portion is replaced
// before the entry is returned (see URLHostRewrite for context).
func (h *Host) serverEntryForManaged(srv *ManagedServer) MCPServerEntry {
	hs := srv.Handshake()
	typ := hs.Protocol
	if typ == "streamable-http" {
		typ = "http"
	}
	return MCPServerEntry{
		Type:    typ,
		URL:     hs.URLWithHostRewrite(h.URLHostRewrite),
		Timeout: srv.Def.Timeout,
	}
}

// FetchPromptFragments collects dynamic system-prompt fragments from every
// started server that opted in via Def.SystemPromptPath, in h.Servers order
// (which mirrors discovered/plugin-list order). For each, it GETs
// http://<handshake-addr><SystemPromptPath> under a per-call slice of ctx
// bounded by promptFetchBudget. A non-200 (e.g. the bridge's 204 when the
// child exposes no such prompt), an empty body, or any transport error is
// skipped silently — the fetch degrades to static rather than blocking the
// launch. Returned fragments are trimmed and non-empty.
//
// Call this after StartAll (so handshakes are resolved) and before the
// provider is exec'd, so appended fragments reach the same prompt file.
func (h *Host) FetchPromptFragments(ctx context.Context) []string {
	var frags []string
	for _, srv := range h.Servers {
		path := srv.Def.SystemPromptPath
		if path == "" {
			continue
		}
		frag, ok := h.fetchOnePromptFragment(ctx, srv, path)
		if ok && frag != "" {
			frags = append(frags, frag)
		}
	}
	return frags
}

func (h *Host) fetchOnePromptFragment(ctx context.Context, srv *ManagedServer, path string) (string, bool) {
	url := "http://" + srv.Handshake().Address + path
	reqCtx, cancel := context.WithTimeout(ctx, promptFetchBudget)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		h.logPromptSkip(srv, err)
		return "", false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.logPromptSkip(srv, err)
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPromptFragmentBytes))
	if err != nil {
		h.logPromptSkip(srv, err)
		return "", false
	}
	return strings.TrimSpace(string(body)), true
}

func (h *Host) logPromptSkip(srv *ManagedServer, err error) {
	if h.Logger != nil {
		h.Logger.Info("skipping dynamic system-prompt fragment", "server", srv.Name, "err", err)
	}
}

// ToolInfo is the subset of an MCP tools/list entry --cheap-context's
// picker needs: enough to group and label a tool, not the full inputSchema.
type ToolInfo struct {
	Name        string
	Description string
}

// mcpSessionIDHeader is the MCP streamable-HTTP transport's session
// continuity header. moxy's native httpServers implementation
// (internal/streamhttp, amarbel-llc/moxy) requires it on every request
// after initialize — the initialize response carries the session id to
// echo back on subsequent calls (tools/list here). clown-stdio-bridge
// doesn't require it (its handlePost has no session concept), so always
// forwarding it when present is harmless there too.
const mcpSessionIDHeader = "Mcp-Session-Id"

// FetchToolCatalog issues a minimal MCP handshake (initialize, then
// tools/list) against srv and returns its tool catalog. Modeled on
// FetchPromptFragments/fetchOnePromptFragment: bounded by
// toolCatalogFetchBudget, and any failure (transport error, non-200,
// malformed JSON-RPC) degrades to (nil, false) rather than blocking the
// --cheap-context picker — a server whose catalog can't be fetched just
// falls back to v1's flat per-server selection instead of per-tool
// grouping (cmd/clown/cheapcontext.go).
//
// Call this after StartAll (so the handshake address is resolved) and
// before compiling plugin manifests, mirroring FetchPromptFragments'
// ordering constraint.
func (h *Host) FetchToolCatalog(ctx context.Context, srv *ManagedServer) ([]ToolInfo, bool) {
	reqCtx, cancel := context.WithTimeout(ctx, toolCatalogFetchBudget)
	defer cancel()

	url := srv.Handshake().URL()
	// initialize is required by the MCP spec before tools/list is valid on a
	// fresh session; the response BODY is unused, but the Mcp-Session-Id
	// header it returns (when the server implements session continuity,
	// e.g. moxy's internal/streamhttp) must be captured and echoed back on
	// tools/list, or the follow-up call 400s ("missing Mcp-Session-Id
	// header") even though initialize itself succeeded.
	_, sessionID, err := h.postJSONRPC(reqCtx, url, "", `{"jsonrpc":"2.0","id":"cheap-context-init","method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"clown-cheap-context","version":"1"}}}`)
	if err != nil {
		h.logToolCatalogSkip(srv, err)
		return nil, false
	}

	body, _, err := h.postJSONRPC(reqCtx, url, sessionID, `{"jsonrpc":"2.0","id":"cheap-context-tools","method":"tools/list","params":{}}`)
	if err != nil {
		h.logToolCatalogSkip(srv, err)
		return nil, false
	}

	var parsed struct {
		Result struct {
			Tools []ToolInfo `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		h.logToolCatalogSkip(srv, err)
		return nil, false
	}
	if h.Logger != nil {
		h.Logger.Info("fetched tool catalog", "server", srv.Name, "tool_count", len(parsed.Result.Tools))
	}
	return parsed.Result.Tools, true
}

// postJSONRPC POSTs a single JSON-RPC request body to url and returns the
// extracted JSON-RPC message body plus the response's Mcp-Session-Id header
// (empty if absent), bounded by maxToolCatalogBytes. A non-200 status is
// treated as an error. sessionID, when non-empty, is echoed back on the
// request via mcpSessionIDHeader (see FetchToolCatalog).
//
// The MCP streamable-HTTP transport lets a server answer a POST with either
// a plain application/json body or a text/event-stream response framing
// the same JSON-RPC message as one or more "data: <json>\n\n" events
// (clown-stdio-bridge defaults to the latter — see heartbeatMode in
// cmd/clown-stdio-bridge/http.go, which streams unless
// CLOWN_BRIDGE_HEARTBEAT_INTERVAL=0 is set on that server). Response
// Content-Type decides which framing to parse; clown doesn't control that
// env var per-fetch, so both must be handled here rather than assuming
// plain JSON.
func (h *Host) postJSONRPC(ctx context.Context, url, sessionID, body string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set(mcpSessionIDHeader, sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	respSessionID := resp.Header.Get(mcpSessionIDHeader)
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxToolCatalogBytes))
	if err != nil {
		return nil, "", err
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		data, err := extractSSEData(raw)
		return data, respSessionID, err
	}
	return raw, respSessionID, nil
}

// extractSSEData returns the payload of the last "data: <payload>" event
// in an SSE stream body. handlePostStreaming (cmd/clown-stdio-bridge)
// emits zero or more heartbeat/progress events before exactly one final
// event carrying the actual JSON-RPC response or error — the last "data:"
// line is always that final message.
func extractSSEData(raw []byte) ([]byte, error) {
	var last []byte
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			last = []byte(data)
		} else if data, ok := strings.CutPrefix(line, "data:"); ok {
			last = []byte(strings.TrimPrefix(data, " "))
		}
	}
	if last == nil {
		return nil, fmt.Errorf("no data: event found in SSE response")
	}
	return last, nil
}

func (h *Host) logToolCatalogSkip(srv *ManagedServer, err error) {
	if h.Logger != nil {
		h.Logger.Info("skipping tool catalog fetch", "server", srv.Name, "err", err)
	}
}

func (h *Host) Shutdown() {
	var wg sync.WaitGroup
	for _, srv := range h.Servers {
		wg.Add(1)
		go func(srv *ManagedServer) {
			defer wg.Done()
			srv.Stop()
		}(srv)
	}
	wg.Wait()

	for _, dir := range h.compiledDirs {
		if err := os.RemoveAll(dir); err != nil && h.Logger != nil {
			h.Logger.Warn("failed to remove compiled plugin dir",
				"dir", dir, "err", err)
		}
	}
	h.compiledDirs = nil
}

// CompileForClaude produces a map from each plugin-dir to a staging
// directory containing a compiled plugin.json. For plugins with running
// HTTP servers (via h.Servers), the mcpServers block is replaced with
// url-based entries using the original server names from clown.json.
// For plugins without running servers, the mcpServers block is stripped.
// Plugins whose clown.json declares monitors are also compiled, even
// when they have no MCP servers, so the monitors array is injected
// into the staged plugin.json.
//
// Call this after StartAll so server URLs are available.
// Dirs that appear in multiple DiscoveredServer entries are compiled once.
// Compiled dirs are tracked on the Host and removed by Shutdown.
func (h *Host) CompileForClaude(discovered []DiscoveredServer) (map[string]string, error) {
	serversByDir := h.serverEntriesByPluginDir(discovered)

	dirOrder, dirSet := pluginDirOrder(discovered, h.monitorsByDir)

	result := make(map[string]string, len(dirOrder))
	for _, dir := range dirOrder {
		if _, done := result[dir]; done {
			continue
		}
		if !dirSet[dir] {
			continue
		}
		staged, err := CompilePluginDir(dir, CompileInputs{
			Servers:  serversByDir[dir],
			Monitors: h.monitorsByDir[dir],
		})
		if err != nil {
			return nil, fmt.Errorf("compiling %s: %w", dir, err)
		}
		h.compiledDirs = append(h.compiledDirs, staged)
		result[dir] = staged
		if h.Logger != nil {
			h.Logger.Info("compiled plugin manifest",
				"source", dir, "staged", staged)
		}
	}
	return result, nil
}

// pluginDirOrder returns the deduplicated set of plugin dirs that need
// staging — every dir that appears in discovered, plus every dir that
// owns monitors. Order: discovered first (preserving discovered's
// order), then any monitor-only dirs in alphabetical order so the
// staging sequence is deterministic.
func pluginDirOrder(discovered []DiscoveredServer, monitorsByDir map[string][]MonitorDef) ([]string, map[string]bool) {
	seen := make(map[string]bool, len(discovered)+len(monitorsByDir))
	order := make([]string, 0, len(discovered)+len(monitorsByDir))
	for _, d := range discovered {
		if seen[d.PluginDir] {
			continue
		}
		seen[d.PluginDir] = true
		order = append(order, d.PluginDir)
	}
	var extra []string
	for dir := range monitorsByDir {
		if seen[dir] {
			continue
		}
		extra = append(extra, dir)
	}
	sort.Strings(extra)
	for _, dir := range extra {
		seen[dir] = true
		order = append(order, dir)
	}
	return order, seen
}

// serverEntriesByPluginDir builds a map from plugin directory to the
// MCPServerEntry map that should be injected into that plugin's
// compiled plugin.json. Keys in the inner map are the original server
// names from clown.json (not the plugin/server composite).
func (h *Host) serverEntriesByPluginDir(discovered []DiscoveredServer) map[string]map[string]MCPServerEntry {
	nameByComposite := make(map[string]serverOrigin, len(discovered))
	for _, d := range discovered {
		nameByComposite[d.Name()] = serverOrigin{
			pluginDir:  d.PluginDir,
			serverName: d.ServerName,
		}
	}

	result := make(map[string]map[string]MCPServerEntry)
	for _, srv := range h.Servers {
		origin, ok := nameByComposite[srv.Name]
		if !ok {
			continue
		}
		if result[origin.pluginDir] == nil {
			result[origin.pluginDir] = make(map[string]MCPServerEntry)
		}
		result[origin.pluginDir][origin.serverName] = h.serverEntryForManaged(srv)
	}
	return result
}

type serverOrigin struct {
	pluginDir  string
	serverName string
}
