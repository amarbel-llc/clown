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
// Bind receives BOTH server sets: allDiscovered is everything Discover found,
// before --cheap-context dropped anything, and selected is what survived and is
// actually running. claude needs the difference to know which plugin dirs to
// exclude; the config-file providers only care about what is running. Passing
// both keeps that claude-specific bookkeeping inside claudeBinding instead of
// leaking back into the shared pipeline.
//
// Bind is ALSO called on the three fallback paths (nothing discovered, nothing
// healthy, everything deselected) with a nil host and empty sets. An
// implementation MUST treat that as "launch with no clown-managed MCP servers",
// not as an error.
type pluginBinding interface {
	Bind(host *pluginhost.Host, allDiscovered, selected []pluginhost.DiscoveredServer) (Command, error)
}

// Command is a fully-formed provider invocation. Args and Env travel together
// because any locus that rewrites one must rewrite the other; keeping them in
// separate parameters is precisely how clown#205 happened — argv went through
// Executor.FormatArgs (so tent rewrote it into `podman run … <inner> <args>`)
// while env went straight to runProvider (so tent did not, and OPENCODE_CONFIG
// landed on the container runtime rather than on the agent inside it).
//
// Be precise about what this buys. Bundling alone does NOT force correctness:
// an executor can still pass Env through untouched, and directExecutor /
// passthroughExecutor do exactly that — correctly, since neither crosses a
// namespace. What it buys is that the env is now IN FRONT OF the one component
// that knows it is crossing a boundary. tentExecutor is that component, and it
// can only refuse an env it cannot translate (see its FormatArgs) because the
// env is now in its hands at all.
//
// See docs/plans/2026-07-28-containment-primitive-design.md part 1a.
type Command struct {
	Args []string
	Env  []string // additional entries; empty means inherit unchanged
}

// claudeBinding is the plugin-dir delivery the claude family (claude,
// clownbox, tent) has always used. It is a straight extraction of what
// runManaged did inline, so the existing claude tests are its control: any
// behavior change here is a bug, not a configuration choice.
type claudeBinding struct {
	baseArgs           []string
	pluginDirs         []string
	logger             *slog.Logger
	cheapContextActive bool
}

// newClaudeBinding builds the claude-family binding. cheapContextActive is
// derived here rather than passed so the two claude call sites cannot disagree
// with what runWithPluginHost computes for the same flags.
func newClaudeBinding(baseArgs, pluginDirs []string, flags parsedFlags, logger *slog.Logger) *claudeBinding {
	return &claudeBinding{
		baseArgs:           baseArgs,
		pluginDirs:         pluginDirs,
		logger:             logger,
		cheapContextActive: cheapContextShouldActivate(flags.cheapContext, flags.cheapContextProfile),
	}
}

func (b *claudeBinding) Bind(host *pluginhost.Host, allDiscovered, selected []pluginhost.DiscoveredServer) (Command, error) {
	// Fallback path: no managed servers to deliver. Hand claude the original,
	// uncompiled plugin dirs — matching the pre-seam behavior exactly.
	if host == nil || len(selected) == 0 {
		return Command{Args: prependPluginDirs(b.baseArgs, b.pluginDirs, nil)}, nil
	}

	dirMap, err := host.CompileForClaude(selected)
	if err != nil {
		return Command{}, fmt.Errorf("compiling plugin manifests: %w", err)
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
		allDirs := pluginDirSet(allDiscovered)
		excluded := make(map[string]bool, len(allDirs))
		for dir := range allDirs {
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

	return Command{Args: prependPluginDirs(b.baseArgs, pluginDirs, dirMap)}, nil
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

// allDiscovered is unused: the pre-filter set only matters for claude's
// plugin-dir exclusion, and these providers have no plugin dirs to exclude from
// — a deselected server simply never appears in the `mcp` block.
func (b *configFileBinding) Bind(host *pluginhost.Host, _, selected []pluginhost.DiscoveredServer) (Command, error) {
	var entries map[string]pluginhost.MCPServerEntry
	if host != nil {
		var err error
		entries, err = host.ServerEntries(selected)
		if err != nil {
			return Command{}, err
		}
	}
	// Always write: the provider needs its config (provider, model, token) even
	// when there are no MCP servers to add, so the empty case still produces a
	// file — it just carries no `mcp` block.
	env, err := b.writeConfig(entries)
	if err != nil {
		return Command{}, err
	}
	return Command{Args: b.baseArgs, Env: env}, nil
}
