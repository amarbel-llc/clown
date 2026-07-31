package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"code.linenisgreat.com/clown/internal/mcphttp"
	"code.linenisgreat.com/clown/internal/staging"
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

	// Staging is the launch's staging root, under which CompileForClaude
	// places every compiled plugin dir. Required whenever CompileForClaude is
	// called; it is what makes the compiled dirs reachable through the single
	// directory a container locus has to expose, instead of scattering them
	// across $TMPDIR. The root owns their removal, so Shutdown does not.
	Staging *staging.Root

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

// AggregatorSpec parameterizes StartAggregator: the absolute path to the
// clown-mcp-collapse binary and the upstreams it should front (each becomes a
// --upstream <name>=<url> arg, in slice order). Name is the composite
// "<plugin>/<server>" identity the aggregator reports in logs and the {server}
// half of every collapsed tool id; URL is the upstream's resolved handshake MCP
// URL.
type AggregatorSpec struct {
	BinaryPath string
	Upstreams  []AggregatorUpstream
}

// AggregatorUpstream is one fronted upstream for StartAggregator.
type AggregatorUpstream struct {
	Name string
	URL  string
}

// StartAggregator launches the clown-mcp-collapse binary as a ManagedServer
// fronting spec.Upstreams, reusing the same spawn → handshake → healthz →
// reap lifecycle as any other managed server. The returned server is appended
// to h.Servers so Shutdown() reaps it alongside the upstreams it fronts (the
// upstreams STAY running — the aggregator dials them). It opts the aggregator
// into dynamic system-prompt contribution via BridgeSystemPromptPath (the fixed
// path clown-mcp-collapse serves), so FetchPromptFragments folds the collapse
// steering fragment into the agent's prompt exactly as it does for any other
// opted-in server.
//
// clown-mcp-collapse prints the clown handshake on its stdout byte-for-byte
// like clown-stdio-bridge, so no special-casing is needed here beyond building
// its argv. It is the opt-in --mcp-collapse mode's single point of process
// synthesis; absent the flag this is never called and behavior is unchanged.
func (h *Host) StartAggregator(ctx context.Context, spec AggregatorSpec) (*ManagedServer, error) {
	if spec.BinaryPath == "" {
		return nil, fmt.Errorf("mcp-collapse: aggregator binary path is empty (dev build? --mcp-collapse requires the Nix-built clown-mcp-collapse)")
	}
	if len(spec.Upstreams) == 0 {
		return nil, fmt.Errorf("mcp-collapse: no upstreams to front")
	}
	args := make([]string, 0, len(spec.Upstreams)*2)
	for _, up := range spec.Upstreams {
		args = append(args, "--upstream", up.Name+"="+up.URL)
	}
	def := ServerDef{
		Command:          spec.BinaryPath,
		Args:             args,
		Transport:        "streamable-http",
		SystemPromptPath: BridgeSystemPromptPath,
		Healthcheck: HealthcheckDef{
			Path:     "/healthz",
			Interval: JSONDuration{Duration: 1 * time.Second},
			Timeout:  JSONDuration{Duration: 30 * time.Second},
		},
	}
	srv := &ManagedServer{
		Name:    "clown-mcp-collapse",
		Def:     def,
		Logger:  h.Logger,
		BaseEnv: h.BaseEnv,
	}
	if err := srv.Start(ctx); err != nil {
		return nil, fmt.Errorf("mcp-collapse: starting aggregator: %w", err)
	}
	h.Servers = append(h.Servers, srv)
	return srv, nil
}

// NewStartedServerForTest builds a ManagedServer that looks started —
// handshake resolved to addr/protocol — WITHOUT spawning a process. It exists
// so tests in OTHER packages (notably cmd/clown's collapseBinding test) can
// exercise code that reads a running server's handshake URL, since the
// handshake field is unexported and cannot be set from outside this package.
// Not for production use: nothing here has a live process to reap.
func NewStartedServerForTest(name, addr, protocol string) *ManagedServer {
	return &ManagedServer{
		Name:      name,
		handshake: Handshake{Address: addr, Protocol: protocol},
	}
}

// ServerEntry is the exported form of serverEntryForManaged: it builds the
// MCPServerEntry (url + type, with URLHostRewrite applied) that a compiled
// plugin manifest should carry for a running server. The --mcp-collapse
// binding uses it to point the single synthesized aggregator plugin dir at the
// running clown-mcp-collapse process, so the collapsed server inherits the
// same URLHostRewrite / tent treatment as any discovered server.
func (h *Host) ServerEntry(srv *ManagedServer) MCPServerEntry {
	return h.serverEntryForManaged(srv)
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
	_, sessionID, err := mcphttp.PostJSONRPC(reqCtx, url, "", `{"jsonrpc":"2.0","id":"cheap-context-init","method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"clown-cheap-context","version":"1"}}}`, maxToolCatalogBytes)
	if err != nil {
		h.logToolCatalogSkip(srv, err)
		return nil, false
	}

	body, _, err := mcphttp.PostJSONRPC(reqCtx, url, sessionID, `{"jsonrpc":"2.0","id":"cheap-context-tools","method":"tools/list","params":{}}`, maxToolCatalogBytes)
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

	// Compiled plugin dirs are deliberately NOT removed here: they live under
	// the launch's staging root (h.Staging), which removes the whole tree on
	// Close. Removing them here as well would make two things own one
	// directory's lifetime.
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
// Compiled dirs are created under h.Staging, which must be set, and are removed
// when that root is closed — not by Shutdown.
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
		staged, err := CompilePluginDir(dir, h.Staging, CompileInputs{
			Servers:  serversByDir[dir],
			Monitors: h.monitorsByDir[dir],
		})
		if err != nil {
			return nil, fmt.Errorf("compiling %s: %w", dir, err)
		}
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

// mcpKeyDisallowed matches every character that is not legal in a flat `mcp`
// map key. See ServerEntries for why the charset is exactly this narrow.
var mcpKeyDisallowed = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// sanitizeMCPKey folds any character outside [A-Za-z0-9_-] to '_', matching
// opencode's own sanitizer (packages/opencode/src/mcp/catalog.ts) so that
// applying it here makes opencode's a no-op.
func sanitizeMCPKey(s string) string {
	return mcpKeyDisallowed.ReplaceAllString(s, "_")
}

// ServerEntries returns a flat name→entry map for every running server, for
// providers whose config has a single top-level `mcp` object (opencode, crush)
// rather than claude's per-plugin-dir namespacing. Call it after StartAll, like
// CompileForClaude.
//
// Keys are "<plugin>__<server>", each component sanitized to [A-Za-z0-9_-].
// The flat map is why the plugin name has to be in the key at all: claude
// namespaces per plugin dir, so two plugins may each declare a server called
// "mcp" without colliding, and that guarantee disappears here.
//
// The charset is not arbitrary. Both providers derive tool names from this key
// but by different rules: opencode sanitizes it
// (catalog.ts: /[^a-zA-Z0-9_-]/g -> "_"), while crush interpolates it verbatim
// into fmt.Sprintf("mcp_%s_%s", ...) (internal/agent/tools/mcp-tools.go). A key
// containing '/' would therefore be silently rewritten by one and would produce
// a tool name the model API rejects under the other. Pre-sanitizing to the
// intersection keeps clown's key identical to what both providers use.
//
// A post-sanitization collision is an error rather than last-write-wins:
// silently shadowing one plugin's server with another's would make its tools
// disappear with no diagnostic anywhere.
//
// Unlike CompileForClaude this returns provider-NEUTRAL entries. Translation to
// each provider's schema (opencode's type:"remote", crush's seconds-valued
// timeout) belongs next to that provider's config writer in cmd/clown, so this
// package does not learn three providers' JSON schemas.
func (h *Host) ServerEntries(discovered []DiscoveredServer) (map[string]MCPServerEntry, error) {
	keyByComposite := make(map[string]string, len(discovered))
	for _, d := range discovered {
		keyByComposite[d.Name()] = sanitizeMCPKey(d.PluginName) + "__" + sanitizeMCPKey(d.ServerName)
	}

	result := make(map[string]MCPServerEntry, len(h.Servers))
	origin := make(map[string]string, len(h.Servers))
	for _, srv := range h.Servers {
		key, ok := keyByComposite[srv.Name]
		if !ok {
			continue
		}
		if prev, dup := origin[key]; dup {
			return nil, fmt.Errorf(
				"mcp key collision: %q and %q both sanitize to %q; rename one plugin or server in its clown.json",
				prev, srv.Name, key,
			)
		}
		origin[key] = srv.Name
		result[key] = h.serverEntryForManaged(srv)
	}
	return result, nil
}
