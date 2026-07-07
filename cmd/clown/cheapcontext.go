package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/amarbel-llc/clown/internal/pluginhost"
)

// toolGroup is one row of a multi-group server's picker section: a set of
// tool names that share a name-prefix group (e.g. all of moxy's "folio"
// tools), or the catch-all "ungrouped" bucket for names that don't parse.
type toolGroup struct {
	name  string // e.g. "folio", or "" for the ungrouped bucket
	tools []string
}

// groupToolsByPrefix splits a server's fetched tool catalog into groups by
// parsing the tool-name-prefix convention clown's own MCP client-facing
// names follow: "mcp__plugin_<plugin>_<server>__<group>_<rest>" (observed
// live against moxy: "mcp__plugin_moxy_moxy__folio_read" -> group "folio").
// This is a client-side convention, not an MCP protocol field (confirmed by
// research: tools/list carries no grouping metadata) — a formal
// clown-plugin-protocol toolGroups declaration is deferred to a follow-up
// issue rather than built here.
//
// Names that don't start with the expected "mcp__plugin_<plugin>_<server>__"
// prefix for this DiscoveredServer, or have no further "_"-delimited segment
// after it, fall into the ungrouped bucket (group name ""). Group order is
// first-seen in tools' order; within a group, tool order is preserved.
func groupToolsByPrefix(d pluginhost.DiscoveredServer, tools []pluginhost.ToolInfo) []toolGroup {
	prefix := fmt.Sprintf("mcp__plugin_%s_%s__", d.PluginName, d.ServerName)

	order := make([]string, 0, 4)
	byGroup := make(map[string][]string, 4)
	for _, t := range tools {
		group := ""
		rest, ok := strings.CutPrefix(t.Name, prefix)
		if ok {
			if idx := strings.Index(rest, "_"); idx > 0 {
				group = rest[:idx]
			}
		}
		if _, seen := byGroup[group]; !seen {
			order = append(order, group)
		}
		byGroup[group] = append(byGroup[group], t.Name)
	}

	groups := make([]toolGroup, 0, len(order))
	for _, g := range order {
		groups = append(groups, toolGroup{name: g, tools: byGroup[g]})
	}
	return groups
}

// isMultiGroup reports whether groups warrants the v2 per-group picker
// section rather than v1's flat per-server row: more than one distinct
// group name (the ungrouped bucket alone, or a single named group covering
// every tool, isn't worth a sub-picker).
func isMultiGroup(groups []toolGroup) bool {
	named := 0
	for _, g := range groups {
		if g.name != "" {
			named++
		}
	}
	return named > 1
}

// serverCatalog pairs a started server with its fetched tool groups (v2) so
// the picker can decide, per server, whether to offer a flat row (v1
// fallback: fetch failed, or the catalog isn't multi-group) or a per-group
// sub-section (moxy's case).
type serverCatalog struct {
	server pluginhost.DiscoveredServer
	groups []toolGroup // nil/empty when the catalog fetch failed
}

// selectionResult is selectServers' v2 return value: which servers survive
// at all, plus which groups (if any) were deselected within each kept,
// multi-group server. A server present in kept but absent from
// excludedGroups was either not multi-group (v1-style all-or-nothing) or had
// every group kept.
type selectionResult struct {
	kept           []pluginhost.DiscoveredServer
	excludedGroups map[string][]toolGroup // keyed by DiscoveredServer.Name()
}

// selectServers renders --cheap-context's picker: a flat per-server row for
// servers with no fetched catalog or a single tool group (v1 behavior), and
// a per-group sub-section for a multi-group server (v2; moxy's ~170 tools
// split into ~17 moxin groups). Requires an interactive TTY — mirrors the
// TTY gate in profileAddInteractive/profileEditInteractive
// (cmd/clown/profileform.go). catalogs must cover every started server
// (fetched via host.FetchToolCatalog in runManaged, after StartAll — see
// cheapcontext design notes); a nil/empty groups field degrades that
// server to a flat row.
func selectServers(catalogs []serverCatalog) (selectionResult, error) {
	if !pluginhost.IsInteractive() {
		return selectionResult{}, fmt.Errorf("--cheap-context requires an interactive TTY")
	}
	if len(catalogs) == 0 {
		return selectionResult{}, nil
	}

	var groups []*huh.Group

	// Flat per-server row, one huh.MultiSelect covering every server with no
	// per-group breakdown (v1 behavior). serverSelected backs its Value.
	var flatOptions []huh.Option[string]
	serverSelected := make([]string, 0, len(catalogs))
	for _, c := range catalogs {
		if isMultiGroup(c.groups) {
			continue
		}
		name := c.server.Name()
		flatOptions = append(flatOptions, huh.NewOption(name, name).Selected(true))
		serverSelected = append(serverSelected, name)
	}
	if len(flatOptions) > 0 {
		groups = append(groups, huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Select MCP servers to load for this session").
				Description("Deselect a server to keep its tools out of the agent's context").
				Options(flatOptions...).
				Value(&serverSelected),
		))
	}

	// One MultiSelect per multi-group server: a row per group, keyed by
	// "<server-name>\x00<group-name>" so different servers' identically
	// named groups (unlikely, but not impossible) never collide. Each
	// server's selection needs its own addressable *[]string for
	// huh.MultiSelect.Value, so use a slice of pointers rather than a map
	// (a map-index expression isn't addressable in Go).
	type groupSelection struct {
		serverName string
		selected   []string
	}
	var groupSelections []*groupSelection
	for _, c := range catalogs {
		if !isMultiGroup(c.groups) {
			continue
		}
		name := c.server.Name()
		var opts []huh.Option[string]
		sel := &groupSelection{serverName: name}
		for _, g := range c.groups {
			label := g.name
			if label == "" {
				label = "(ungrouped)"
			}
			key := name + "\x00" + g.name
			opts = append(opts, huh.NewOption(fmt.Sprintf("%s: %s (%d tools)", name, label, len(g.tools)), key).Selected(true))
			sel.selected = append(sel.selected, key)
		}
		groupSelections = append(groupSelections, sel)
		groups = append(groups, huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title(fmt.Sprintf("%s: select tool groups to load", name)).
				Description("Deselect a group to keep its tools out of the agent's context").
				Options(opts...).
				Value(&sel.selected),
		))
	}

	if len(groups) == 0 {
		// Every server was somehow neither flat nor multi-group (empty
		// catalogs slice already handled above); nothing to ask.
		return selectionResult{}, nil
	}

	form := huh.NewForm(groups...)
	if err := form.Run(); err != nil {
		return selectionResult{}, fmt.Errorf("cheap-context prompt: %w", err)
	}

	flatChosen := make(map[string]bool, len(serverSelected))
	for _, name := range serverSelected {
		flatChosen[name] = true
	}
	groupSelected := make(map[string][]string, len(groupSelections))
	for _, sel := range groupSelections {
		groupSelected[sel.serverName] = sel.selected
	}

	result := selectionResult{excludedGroups: map[string][]toolGroup{}}
	for _, c := range catalogs {
		name := c.server.Name()
		if !isMultiGroup(c.groups) {
			if flatChosen[name] {
				result.kept = append(result.kept, c.server)
			}
			continue
		}

		chosenKeys := make(map[string]bool, len(groupSelected[name]))
		for _, key := range groupSelected[name] {
			chosenKeys[key] = true
		}
		var excluded []toolGroup
		anyKept := false
		for _, g := range c.groups {
			key := name + "\x00" + g.name
			if chosenKeys[key] {
				anyKept = true
			} else {
				excluded = append(excluded, g)
			}
		}
		if !anyKept {
			continue // every group deselected: drop the whole server
		}
		result.kept = append(result.kept, c.server)
		if len(excluded) > 0 {
			result.excludedGroups[name] = excluded
		}
	}
	return result, nil
}

// excludeToolsPushBudget bounds how long a single POST
// /clown/exclude-tools push may take before pushExcludeTools gives up on
// that server (the caller logs and continues — a push failure degrades to
// "server kept fully loaded", not a launch failure).
const excludeToolsPushBudget = 3 * time.Second

// applyCheapContextSelection fetches each started server's tool catalog,
// runs the --cheap-context picker, and applies the result: a
// fully-deselected server is stopped (host.Servers/report.Started both
// still reference it, but it no longer answers) and dropped from the
// returned slice; a partially-deselected server's exclusions are pushed via
// POST /clown/exclude-tools to whatever process is answering that server's
// URL — clown-stdio-bridge for a bridged plugin, or the plugin's own HTTP
// server directly for a native httpServers plugin that implements the route
// itself (e.g. moxy, amarbel-llc/moxy#399). A plugin that implements
// neither 404s the push, which is logged and treated as "exclusions not
// applied" rather than a launch failure — v1's whole-server deselection
// still works regardless.
func applyCheapContextSelection(
	ctx context.Context,
	host *pluginhost.Host,
	discovered []pluginhost.DiscoveredServer,
	started []*pluginhost.ManagedServer,
	logger *slog.Logger,
) ([]pluginhost.DiscoveredServer, error) {
	byName := make(map[string]pluginhost.DiscoveredServer, len(discovered))
	for _, d := range discovered {
		byName[d.Name()] = d
	}

	catalogs := make([]serverCatalog, 0, len(started))
	serversByName := make(map[string]*pluginhost.ManagedServer, len(started))
	for _, srv := range started {
		d, ok := byName[srv.Name]
		if !ok {
			continue // shouldn't happen: every started server came from discovered
		}
		serversByName[srv.Name] = srv
		tools, ok := host.FetchToolCatalog(ctx, srv)
		var groups []toolGroup
		if ok {
			groups = groupToolsByPrefix(d, tools)
		}
		catalogs = append(catalogs, serverCatalog{server: d, groups: groups})
	}

	result, err := selectServers(catalogs)
	if err != nil {
		return nil, err
	}

	keptNames := make(map[string]bool, len(result.kept))
	for _, d := range result.kept {
		keptNames[d.Name()] = true
	}
	for name, srv := range serversByName {
		if !keptNames[name] {
			logger.Info("cheap-context: stopping deselected server", "server", name)
			srv.Stop()
		}
	}

	for name, excludedGroups := range result.excludedGroups {
		srv, ok := serversByName[name]
		if !ok {
			continue
		}
		names := excludeToolsPayload(excludedGroups)
		if err := pushExcludeTools(ctx, srv, names); err != nil {
			// Best-effort: a push failure means this server's deselected
			// tools stay visible, not that the launch fails.
			logger.Warn("cheap-context: failed to push tool exclusions; server keeps its full catalog",
				"server", name, "err", err)
		} else {
			logger.Info("cheap-context: excluded groups from server", "server", name, "groups", len(excludedGroups))
		}
	}

	return result.kept, nil
}

// excludeToolsPayload flattens excluded groups into the flat name list
// /clown/exclude-tools expects, sending BOTH representations so the same
// payload works against either endpoint shape without clown needing to know
// which one it's talking to:
//   - the bare group name (e.g. "folio") — what moxy's dotted/bare-name
//     convention (amarbel-llc/moxy#399, internal/toolexclude.Parse) expects
//     to exclude a whole moxin; a no-op entry for clown-stdio-bridge, which
//     only matches exact tool names.
//   - every individual mangled tool name in the group (e.g.
//     "mcp__plugin_moxy_moxy__folio_read") — what clown-stdio-bridge's
//     exact-match filterToolsListResponse expects; harmless extra entries
//     for moxy, which only recognizes its own bare/dotted names.
//
// The ungrouped bucket (group name "") contributes only its tool names,
// since an empty string is never a meaningful exclusion entry on either
// side.
func excludeToolsPayload(groups []toolGroup) []string {
	var names []string
	for _, g := range groups {
		if g.name != "" {
			names = append(names, g.name)
		}
		names = append(names, g.tools...)
	}
	return names
}

// pushExcludeTools POSTs the exclusion list to a running server's
// /clown/exclude-tools endpoint — either clown-stdio-bridge
// (cmd/clown-stdio-bridge/http.go) for a bridged plugin, or the plugin's own
// HTTP server for a native httpServers plugin that implements the route
// itself (e.g. moxy, amarbel-llc/moxy#399; both share one wire contract:
// body {"exclude": [...]}, 200 response echoing the resulting set). A
// plugin that implements neither the bridge nor its own version of this
// route 404s, which the caller logs and treats as "exclusions not applied"
// rather than a launch failure.
func pushExcludeTools(ctx context.Context, srv *pluginhost.ManagedServer, names []string) error {
	reqCtx, cancel := context.WithTimeout(ctx, excludeToolsPushBudget)
	defer cancel()

	body, err := json.Marshal(struct {
		Exclude []string `json:"exclude"`
	}{Exclude: names})
	if err != nil {
		return err
	}
	url := srv.Handshake().URL()
	url = strings.TrimSuffix(url, "/mcp") + "/clown/exclude-tools"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}
