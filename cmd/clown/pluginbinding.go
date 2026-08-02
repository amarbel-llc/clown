package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"code.linenisgreat.com/clown/internal/buildcfg"
	"code.linenisgreat.com/clown/internal/pluginhost"
	"code.linenisgreat.com/clown/internal/staging"
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

// collapseBinding is the claude-family binding for --mcp-collapse mode. It is a
// pure decorator over the claude plugin-dir delivery: the aggregator process is
// already running (runManaged spawned it via host.StartAggregator, parallel to
// the cheap-context step, before Bind is called), so Bind itself has no side
// effects — it only compiles a single synthesized plugin dir pointing the
// harness at that running aggregator and hands claude a --plugin-dir set that
// (a) excludes every dir that owned a discovered upstream MCP server (those are
// now fronted by the aggregator, one-in-N-out), (b) keeps every non-server dir
// (job monitor, juggler, skills-only plugins), and (c) adds the aggregator dir.
//
// This mirrors claudeBinding's own cheap-context dir-exclusion — a dir whose
// only contribution was a now-collapsed MCP server must be dropped, or claude
// would load its ORIGINAL plugin.json whose mcpServers block still declares the
// flat upstream tools, defeating the collapse.
type collapseBinding struct {
	baseArgs   []string
	pluginDirs []string
	aggregator *pluginhost.ManagedServer
	root       *staging.Root
	logger     *slog.Logger
}

// newCollapseBinding builds the --mcp-collapse binding. aggregator is the
// already-started clown-mcp-collapse ManagedServer; root is the launch staging
// root under which the synthesized aggregator plugin dir is compiled.
func newCollapseBinding(baseArgs, pluginDirs []string, aggregator *pluginhost.ManagedServer, root *staging.Root, logger *slog.Logger) *collapseBinding {
	return &collapseBinding{
		baseArgs:   baseArgs,
		pluginDirs: pluginDirs,
		aggregator: aggregator,
		root:       root,
		logger:     logger,
	}
}

func (b *collapseBinding) Bind(host *pluginhost.Host, allDiscovered, selected []pluginhost.DiscoveredServer) (Command, error) {
	// Fallback path: nothing discovered / healthy / running. With no upstreams
	// there is nothing to collapse, so behave exactly like the plain claude
	// fallback — hand claude the original, uncompiled plugin dirs. runManaged
	// only ever installs a collapseBinding when it also started an aggregator,
	// but the interface contract (nil host on the fallback paths) still routes
	// here, and a nil aggregator would otherwise panic below.
	if host == nil || b.aggregator == nil || len(selected) == 0 {
		return Command{Args: prependPluginDirs(b.baseArgs, b.pluginDirs, nil)}, nil
	}

	aggDir, err := b.synthAggregatorPluginDir(host)
	if err != nil {
		return Command{}, fmt.Errorf("mcp-collapse: synthesizing aggregator plugin dir: %w", err)
	}

	// Drop every plugin dir that contributed a now-collapsed upstream MCP server.
	// allDiscovered is the full pre-selection set, so this catches all of them
	// regardless of cheap-context (which is not combined with --mcp-collapse in
	// the prototype). A dir that ALSO ships non-server contributions (skills,
	// monitors) is still dropped here — the prototype is all-or-nothing and a
	// dir mixing a collapsed server with skills is out of scope; keeping it would
	// reintroduce its flat mcpServers block.
	excluded := pluginDirSet(allDiscovered)
	pluginDirs := dropExcludedDirs(b.pluginDirs, excluded)
	if b.logger != nil {
		b.logger.Info("mcp-collapse: fronting upstreams behind aggregator",
			"upstreams", len(selected), "excluded_dirs", len(excluded), "aggregator_dir", aggDir)
	}

	// Append the synthesized aggregator dir last so it cannot shadow a
	// user-supplied dir, and pass it through prependPluginDirs' compiled-dir map
	// so its --plugin-dir points at the staged copy (it IS the staged copy, so
	// the map is identity here, but this keeps the arg-shape uniform).
	pluginDirs = append(pluginDirs, aggDir)
	return Command{Args: prependPluginDirs(b.baseArgs, pluginDirs, nil)}, nil
}

// aggregatorPluginManifest is the minimal .claude-plugin/plugin.json for the
// synthesized aggregator plugin dir. Only the name is load-bearing (claude
// namespaces the collapsed server under it); version mirrors the other
// synthesized builtin plugins.
type aggregatorPluginManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// synthAggregatorPluginDir writes a fresh plugin dir under the staging root
// whose compiled plugin.json carries exactly ONE mcpServers entry — the running
// aggregator, via host.ServerEntry so it inherits URLHostRewrite/tent treatment
// — and returns the dir. Unlike synthJugglerPluginDir / synthJobMonitorPluginDir
// it does NOT write a clown.json: the aggregator process is already spawned and
// lifecycle-managed (host.StartAggregator), so pluginhost must NOT rediscover
// and re-spawn it. The mcpServers block is baked directly into the compiled
// manifest instead, the same shape CompileForClaude produces for a discovered
// server.
func (b *collapseBinding) synthAggregatorPluginDir(host *pluginhost.Host) (string, error) {
	if b.root == nil {
		return "", fmt.Errorf("staging root is required")
	}
	srcDir, err := b.root.Dir("clown-mcp-collapse-plugin-src-*")
	if err != nil {
		return "", err
	}
	manifestDir := filepath.Join(srcDir, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		return "", err
	}
	manifest := aggregatorPluginManifest{Name: "clown-mcp-collapse", Version: "1"}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), raw, 0o600); err != nil {
		return "", err
	}

	// Added for mcp-collapse permission-mux POC. When the clown-hook-collapse
	// binary path is baked in (nix builds), ship a PreToolUse hook THROUGH THIS
	// aggregator plugin so the collapsed mcp_call tool can be demuxed by tool_id
	// back to a per-upstream-tool allow/ask/deny decision (restoring the
	// permission granularity collapse otherwise erases). This mirrors
	// synthJobMonitorPluginDir's clown-hook-allow wire-up: claude loads a
	// plugin's hooks/hooks.json via --plugin-dir in every session. The hooks/
	// dir is written into srcDir BEFORE CompilePluginDir, which symlinks all
	// non-plugin.json entries (including hooks/) into the staged dir claude
	// loads. The `.*` matcher routes every tool through clown-hook-collapse,
	// which self-filters to the collapsed mcp_call and defers otherwise.
	//
	// POC GATE — OFF BY DEFAULT: the hook and its hardcoded 3-entry stage1Policy
	// are throwaway scaffolding (stage-1 mechanics proof), so they must NOT gate
	// real tools for anyone using the shipped --mcp-collapse feature. The hooks.json
	// is written ONLY when CLOWN_MCP_COLLAPSE_POC_HOOK=1 is explicitly set (in
	// addition to the binary path being baked in). With the env var unset — the
	// default — the aggregator plugin dir ships NO hooks.json and this POC hook
	// never engages, so master's --mcp-collapse is byte-identical to before the POC.
	// The `debug-mcp-collapse-perms` justfile recipe sets the env var to exercise it.
	// This gate is removed once the real policy source lands (moxy#432 — moxy
	// advertising per-tool permission policy in tool _meta, which clown's hook will
	// eventually read instead of a hardcoded map). Skipped in dev builds (empty
	// McpCollapseHookPath).
	if buildcfg.McpCollapseHookPath != "" && os.Getenv("CLOWN_MCP_COLLAPSE_POC_HOOK") == "1" {
		hooksDir := filepath.Join(srcDir, "hooks")
		if err := os.MkdirAll(hooksDir, 0o700); err != nil {
			return "", err
		}
		hooksCfg := map[string]any{
			"hooks": map[string]any{
				"PreToolUse": []any{
					map[string]any{
						"matcher": ".*",
						"hooks": []any{
							map[string]any{
								"type":    "command",
								"command": buildcfg.McpCollapseHookPath,
								"timeout": 5,
							},
						},
					},
				},
			},
		}
		hb, err := json.MarshalIndent(hooksCfg, "", "  ")
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), hb, 0o600); err != nil {
			return "", err
		}
	}

	staged, err := pluginhost.CompilePluginDir(srcDir, b.root, pluginhost.CompileInputs{
		Servers: map[string]pluginhost.MCPServerEntry{
			"mcp-collapse": host.ServerEntry(b.aggregator),
		},
	})
	if err != nil {
		return "", err
	}
	return staged, nil
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
