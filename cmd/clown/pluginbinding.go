package main

import (
	"fmt"
	"log/slog"

	"code.linenisgreat.com/clown/internal/pluginhost"
)

// pluginBinding delivers the started MCP servers to a provider in that
// provider's native form. It owns everything downstream of "the servers are
// healthy": claude stages plugin dirs and receives --plugin-dir flags, while
// opencode and crush receive a generated JSON config plus an env var pointing
// at it.
//
// Everything UPSTREAM of that — discovery, StartAll, the failure policy,
// --cheap-context — is provider-agnostic and stays in runWithPluginHost /
// runManaged, which is the point of the seam: phases 2 (prompt fragments) and 3
// (job-wakeup) then have exactly one place to land rather than one per
// provider.
//
// Bind is called with the post-cheap-context server set, and ALSO on the three
// fallback paths (nothing discovered, nothing healthy, everything deselected)
// with a nil host and empty set. An implementation MUST treat that as "launch
// with no clown-managed MCP servers", not as an error.
type pluginBinding interface {
	Bind(host *pluginhost.Host, discovered []pluginhost.DiscoveredServer) (bindResult, error)
}

// bindResult is what a binding produces: the final argv, plus any additional
// environment the child needs. Env is nil for providers that need none.
type bindResult struct {
	Args []string
	Env  []string
}

// claudeBinding is the plugin-dir delivery the claude family (claude,
// clownbox, tent) has always used. It is a straight extraction of what
// runManaged did inline, so the existing claude tests are its control: any
// behavior change here is a bug, not a configuration choice.
type claudeBinding struct {
	baseArgs   []string
	pluginDirs []string
	logger     *slog.Logger

	// cheapContextActive and allDiscoveredDirs drive the dir-exclusion step
	// below. allDiscoveredDirs is the PRE-filter set of dirs owning at least one
	// clown.json server, which is only knowable inside runWithPluginHost (after
	// Discover, before --cheap-context drops anything), so runWithPluginHost
	// stamps it on after construction.
	cheapContextActive bool
	allDiscoveredDirs  map[string]bool
}

func (b *claudeBinding) Bind(host *pluginhost.Host, discovered []pluginhost.DiscoveredServer) (bindResult, error) {
	// Fallback path: no managed servers to deliver. Hand claude the original,
	// uncompiled plugin dirs — matching the pre-seam behavior exactly.
	if host == nil || len(discovered) == 0 {
		return bindResult{Args: prependPluginDirs(b.baseArgs, b.pluginDirs, nil)}, nil
	}

	dirMap, err := host.CompileForClaude(discovered)
	if err != nil {
		return bindResult{}, fmt.Errorf("compiling plugin manifests: %w", err)
	}

	pluginDirs := b.pluginDirs

	// --cheap-context: a dir that owned a clown.json server before selection but
	// has no compiled entry after it was deselected (and carries no monitors —
	// CompileForClaude/pluginDirOrder already keeps monitor-only dirs in dirMap)
	// must be dropped from pluginDirs entirely. Otherwise prependPluginDirs'
	// fallback for a dir absent from dirMap — pass it through unmodified — hands
	// claude the dir's ORIGINAL plugin.json, whose own mcpServers block still
	// declares the tools the user just opted out of, silently reintroducing them.
	if b.cheapContextActive {
		excluded := make(map[string]bool, len(b.allDiscoveredDirs))
		for dir := range b.allDiscoveredDirs {
			if _, compiled := dirMap[dir]; !compiled {
				excluded[dir] = true
			}
		}
		if len(excluded) > 0 {
			pluginDirs = dropExcludedDirs(pluginDirs, excluded)
			if b.logger != nil {
				b.logger.Info("cheap-context excluded plugin dirs", "count", len(excluded))
			}
		}
	}

	return bindResult{Args: prependPluginDirs(b.baseArgs, pluginDirs, dirMap)}, nil
}

// configFileBinding is the binding for providers configured by a generated JSON
// file plus an env var naming it (opencode, crush). Both share this; only
// writeConfig differs, because the only real difference between them is the
// shape of the JSON and the name of the variable.
//
// writeConfig receives the flat entry map — possibly empty, on the fallback
// paths — and returns the env entries the child needs to find what it wrote.
// Translation into each provider's schema happens inside writeConfig rather
// than here, so this type stays ignorant of both schemas.
type configFileBinding struct {
	baseArgs    []string
	writeConfig func(mcp map[string]pluginhost.MCPServerEntry) ([]string, error)
}

func (b *configFileBinding) Bind(host *pluginhost.Host, discovered []pluginhost.DiscoveredServer) (bindResult, error) {
	var entries map[string]pluginhost.MCPServerEntry
	if host != nil && len(discovered) > 0 {
		var err error
		entries, err = host.ServerEntries(discovered)
		if err != nil {
			return bindResult{}, err
		}
	}
	// Always write: the provider needs its config (provider, model, token) even
	// when there are no MCP servers to add, so the empty case still produces a
	// file — it just carries no `mcp` block.
	env, err := b.writeConfig(entries)
	if err != nil {
		return bindResult{}, err
	}
	return bindResult{Args: b.baseArgs, Env: env}, nil
}
