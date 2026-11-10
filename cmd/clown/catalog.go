package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"code.linenisgreat.com/clown/internal/0/buildcfg"
	"code.linenisgreat.com/clown/internal/0/catalogcontract"
	"code.linenisgreat.com/clown/internal/alfa/catalogsnap"
	"code.linenisgreat.com/clown/internal/alfa/pluginhost"
	"code.linenisgreat.com/clown/internal/alfa/sessionstatus"
	"code.linenisgreat.com/clown/internal/bravo/catalogserve"
)

// The live capability catalog (clown#350, RFC 0018 §10): at launch, the
// session's started servers' tools/list results are recorded as a snapshot
// (internal/*/catalogsnap), and `clown catalog traversal-serve`, a
// cutting-garden traversal plugin, serves the live sessions' snapshots as
// clown-catalog://<principal>/<plugin>/<server>/<tool>/<input>.

const catalogUsage = "usage: clown catalog traversal-serve (started by cutting-garden, with TRAVERSAL_PLUGIN_COOKIE set)"

// catalogDisabled is the kill switch: CLOWN_DISABLE_CATALOG=1 records no
// snapshot.
func catalogDisabled() bool { return os.Getenv("CLOWN_DISABLE_CATALOG") == "1" }

// runCatalogVerb is `clown catalog traversal-serve`.
func runCatalogVerb(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "traversal-serve" {
		fmt.Fprintln(stderr, catalogUsage)
		return 2
	}
	dir, err := catalogsnap.Dir()
	if err != nil {
		fmt.Fprintf(stderr, "clown catalog: %v\n", err)
		return 1
	}
	c, err := catalogcontract.Load()
	if err != nil {
		fmt.Fprintf(stderr, "clown catalog: %v\n", err)
		return 1
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = "/tmp" // RFC 0013: a short path, never a nested temp dir
	}
	err = catalogserve.Serve(stdin, stdout, catalogserve.Config{
		Cookie:     os.Getenv("TRAVERSAL_PLUGIN_COOKIE"),
		Dir:        dir,
		Alive:      catalogserve.FrontendAlive,
		Contract:   c,
		RuntimeDir: runtimeDir,
		Version:    buildcfg.Version,
	})
	if err != nil {
		fmt.Fprintf(stderr, "clown catalog traversal-serve: %v\n", err)
		return 1
	}
	return 0
}

// catalogHarness is the snapshot's harness for a provider: the built-ins are
// listed for claude only.
func catalogHarness(provider string) string {
	if provider == "claude" {
		return catalogsnap.HarnessClaudeCode
	}
	return provider
}

// catalogServers is the started servers still bound after --cheap-context
// (kept), each with the tools fn fetches for it; a server whose fetch fails is
// left out rather than listed toolless.
func catalogServers(started []*pluginhost.ManagedServer, kept []pluginhost.DiscoveredServer, fn func(*pluginhost.ManagedServer) ([]pluginhost.ToolInfo, bool)) []catalogsnap.Server {
	keep := map[string]bool{}
	for _, d := range kept {
		keep[d.Name()] = true
	}
	var out []catalogsnap.Server
	for _, srv := range started {
		plugin, server, ok := strings.Cut(srv.Name, "/")
		if !ok || !keep[srv.Name] {
			continue
		}
		tools, ok := fn(srv)
		if !ok {
			continue
		}
		s := catalogsnap.Server{Plugin: plugin, Server: server}
		for _, t := range tools {
			s.Tools = append(s.Tools, catalogsnap.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema, Meta: t.Meta})
		}
		out = append(out, s)
	}
	return out
}

// recordCatalogSnapshot writes the session's catalog snapshot in the
// background, so the fetches never hold the launch. Servers reserved for an
// agent unit are not started here, so they are not in it.
func recordCatalogSnapshot(ctx context.Context, host *pluginhost.Host, started []*pluginhost.ManagedServer, kept []pluginhost.DiscoveredServer, flags parsedFlags, logf func(format string, args ...any)) {
	if catalogDisabled() || flags.naked || flags.printLaunchPlan || flags.identity.Key == "" {
		return
	}
	dir, err := catalogsnap.Dir()
	if err != nil {
		logf("catalog snapshot not written: %v", err)
		return
	}
	go func() {
		servers := catalogServers(started, kept, func(srv *pluginhost.ManagedServer) ([]pluginhost.ToolInfo, bool) {
			return host.FetchToolCatalog(ctx, srv)
		})
		if ctx.Err() != nil {
			return
		}
		pid := os.Getpid()
		snap := catalogsnap.Snapshot{
			Principal:          flags.identity.Key,
			Harness:            catalogHarness(flags.provider),
			FrontendPID:        pid,
			FrontendStartTicks: sessionstatus.StartTicks(pid),
			Servers:            servers,
		}
		if err := catalogsnap.Write(dir, snap); err != nil {
			logf("catalog snapshot not written: %v", err)
		}
	}()
}
