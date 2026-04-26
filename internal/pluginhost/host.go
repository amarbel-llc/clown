package pluginhost

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
)

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
	Verbose    bool
	Servers    []*ManagedServer
	// BridgePath, when set, is the absolute path to clown-stdio-bridge.
	// It is required when any discovered clown.json declares stdioServers
	// entries; Discover passes it to Desugar so those entries are
	// rewritten as httpServers entries pointing at the bridge.
	BridgePath string

	// compiledDirs tracks staging directories produced by
	// CompileForClaude; Shutdown removes them.
	compiledDirs []string
}

func (h *Host) Discover() ([]DiscoveredServer, error) {
	var found []DiscoveredServer
	for _, dir := range h.PluginDirs {
		cfg, err := LoadClownConfig(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("plugin dir %s: %w", dir, err)
		}

		if err := Desugar(cfg, h.BridgePath); err != nil {
			return nil, fmt.Errorf("plugin dir %s: %w", dir, err)
		}

		pluginName, err := PluginName(dir)
		if err != nil {
			return nil, fmt.Errorf("plugin dir %s: %w", dir, err)
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
				Verbose:   h.Verbose,
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
// protocols pass through unmodified so schema errors are legible.
func serverEntryForManaged(srv *ManagedServer) MCPServerEntry {
	hs := srv.Handshake()
	typ := hs.Protocol
	if typ == "streamable-http" {
		typ = "http"
	}
	return MCPServerEntry{
		Type:    typ,
		URL:     hs.URL(),
		Timeout: srv.Def.Timeout,
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
//
// Call this after StartAll so server URLs are available.
// Dirs that appear in multiple DiscoveredServer entries are compiled once.
// Compiled dirs are tracked on the Host and removed by Shutdown.
func (h *Host) CompileForClaude(discovered []DiscoveredServer) (map[string]string, error) {
	serversByDir := h.serverEntriesByPluginDir(discovered)

	result := make(map[string]string)
	for _, d := range discovered {
		if _, done := result[d.PluginDir]; done {
			continue
		}
		staged, err := CompilePluginDir(d.PluginDir, serversByDir[d.PluginDir])
		if err != nil {
			return nil, fmt.Errorf("compiling %s: %w", d.PluginDir, err)
		}
		h.compiledDirs = append(h.compiledDirs, staged)
		result[d.PluginDir] = staged
		if h.Logger != nil {
			h.Logger.Info("compiled plugin manifest",
				"source", d.PluginDir, "staged", staged)
		}
	}
	return result, nil
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
		result[origin.pluginDir][origin.serverName] = serverEntryForManaged(srv)
	}
	return result
}

type serverOrigin struct {
	pluginDir  string
	serverName string
}
