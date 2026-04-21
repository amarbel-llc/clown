package pluginhost

import (
	"context"
	"fmt"
	"os"
	"sync"
)

type DiscoveredServer struct {
	PluginDir  string
	PluginName string
	ServerName string
	Def        ServerDef
}

type Host struct {
	PluginDirs []string
	Servers    []*ManagedServer
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

func (h *Host) StartAll(ctx context.Context, discovered []DiscoveredServer) error {
	type startResult struct {
		server *ManagedServer
		err    error
	}

	results := make(chan startResult, len(discovered))
	var wg sync.WaitGroup

	for _, d := range discovered {
		wg.Add(1)
		go func(d DiscoveredServer) {
			defer wg.Done()
			srv := &ManagedServer{
				Name:      d.PluginName + "/" + d.ServerName,
				Def:       d.Def,
				PluginDir: d.PluginDir,
			}
			err := srv.Start(ctx)
			results <- startResult{server: srv, err: err}
		}(d)
	}

	wg.Wait()
	close(results)

	var errs []error
	for res := range results {
		if res.err != nil {
			errs = append(errs, res.err)
		} else {
			h.Servers = append(h.Servers, res.server)
		}
	}

	if len(errs) > 0 {
		h.Shutdown()
		return fmt.Errorf("failed to start %d server(s): %v", len(errs), errs)
	}

	return nil
}

func (h *Host) ServerURLs() map[string]string {
	urls := make(map[string]string, len(h.Servers))
	for _, srv := range h.Servers {
		urls[srv.Name] = srv.Handshake().URL()
	}
	return urls
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
}
