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
// parsing a tool-name-prefix convention. host.FetchToolCatalog talks
// directly to the plugin's own /mcp endpoint (not through Claude Code), so
// names arrive in whatever form the plugin itself renders — NOT the
// "mcp__plugin_<plugin>_<server>__<rest>" mangling Claude Code's harness
// applies afterward. Two conventions are recognized, tried in order:
//
//  1. moxy's own rendering (internal/naming.Template, amarbel-llc/moxy):
//     "<group>.<tool>", dot-separated (e.g. "folio.read" -> group "folio").
//     This is the form actually observed from moxy's tools/list (confirmed
//     live: a 198-tool catalog grouped to 1 bucket under the old
//     mangled-prefix check, since moxy never emits that mangled form
//     itself).
//  2. The mangled "mcp__plugin_<plugin>_<server>__<group>_<rest>" form, kept
//     as a fallback in case some other server's own /mcp responses happen
//     to already carry that shape (defensive; not known to occur today).
//
// Neither is an MCP protocol field — tools/list carries no grouping
// metadata (confirmed by research) — so this stays a client-side
// heuristic; a formal clown-plugin-protocol toolGroups declaration is
// deferred to a follow-up issue rather than built here.
//
// A name matching neither convention falls into the ungrouped bucket
// (group name ""). Group order is first-seen in tools' order; within a
// group, tool order is preserved.
func groupToolsByPrefix(d pluginhost.DiscoveredServer, tools []pluginhost.ToolInfo) []toolGroup {
	mangledPrefix := fmt.Sprintf("mcp__plugin_%s_%s__", d.PluginName, d.ServerName)

	order := make([]string, 0, 4)
	byGroup := make(map[string][]string, 4)
	for _, t := range tools {
		group := ""
		if idx := strings.Index(t.Name, "."); idx > 0 {
			// moxy's own "<group>.<tool>" rendering.
			group = t.Name[:idx]
		} else if rest, ok := strings.CutPrefix(t.Name, mangledPrefix); ok {
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
// the picker can decide, per server, whether to offer a bare on/off row (v1
// fallback: fetch failed, or the catalog isn't multi-group) or an
// on/off-plus-individual-tools section (moxy's case).
type serverCatalog struct {
	server pluginhost.DiscoveredServer
	groups []toolGroup // nil/empty when the catalog fetch failed
}

// selectionResult is selectServers' v2 return value: which servers survive
// at all, plus which individual tool names (if any) were deselected within
// each kept, multi-group server. A server present in kept but absent from
// excludedTools was either not multi-group (v1-style all-or-nothing) or had
// every tool kept.
type selectionResult struct {
	kept          []pluginhost.DiscoveredServer
	excludedTools map[string][]string // keyed by DiscoveredServer.Name()
}

// allRowKey is the option value for a multi-group server's top "(all)" row:
// unchecking it means "drop this whole server," independent of which
// individual tool rows are checked (mirrors v1's whole-server semantics).
const allRowKey = "\x00all"

// selectServers renders --cheap-context's picker as ONE screen: a single
// huh.Group whose body is one huh.MultiSelect per server. A server with no
// fetched catalog or a single tool group gets a bare on/off row (v1
// behavior). A multi-group server (v2; moxy's ~170 tools) gets its own
// MultiSelect with a top "(all N tools)" row plus one indented row per
// individual tool — unchecking the top row drops the whole server;
// unchecking specific tool rows (with the top row still checked) excludes
// just those tools. Everything renders on one page — no huh.Form group
// pagination — per user feedback that a separate page per multi-group
// server ("staged on a second screen") was confusing. A true collapsible
// outline/tree (per-moxin expand/collapse) is deferred to clown#176.
//
// Requires an interactive TTY — mirrors the TTY gate in
// profileAddInteractive/profileEditInteractive (cmd/clown/profileform.go).
// catalogs must cover every started server (fetched via
// host.FetchToolCatalog in runManaged, after StartAll); a nil/empty groups
// field degrades that server to a bare on/off row.
func selectServers(catalogs []serverCatalog, logger *slog.Logger) (selectionResult, error) {
	if !pluginhost.IsInteractive() {
		return selectionResult{}, fmt.Errorf("--cheap-context requires an interactive TTY")
	}
	if len(catalogs) == 0 {
		return selectionResult{}, nil
	}
	if logger != nil {
		for _, c := range catalogs {
			logger.Info("cheap-context: server catalog", "server", c.server.Name(),
				"groups", len(c.groups), "multi_group", isMultiGroup(c.groups))
		}
	}

	// Each MultiSelect needs its own addressable *[]string for
	// huh.MultiSelect.Value, so use a slice of pointers rather than a map (a
	// map-index expression isn't addressable in Go).
	type fieldSelection struct {
		serverName string
		multiGroup bool
		selected   []string
	}
	var fields []*fieldSelection
	var multiSelects []*huh.MultiSelect[string]

	for _, c := range catalogs {
		name := c.server.Name()
		if !isMultiGroup(c.groups) {
			sel := &fieldSelection{serverName: name, selected: []string{name}}
			fields = append(fields, sel)
			multiSelects = append(multiSelects, huh.NewMultiSelect[string]().
				Title("Servers").
				Options(huh.NewOption(name, name).Selected(true)).
				Value(&sel.selected))
			continue
		}

		totalTools := 0
		var opts []huh.Option[string]
		sel := &fieldSelection{serverName: name, multiGroup: true, selected: []string{allRowKey}}
		opts = append(opts, huh.NewOption(name, allRowKey).Selected(true))
		for _, g := range c.groups {
			for _, toolName := range g.tools {
				totalTools++
				label := "    " + toolName // indent under the server's "(all)" row
				opts = append(opts, huh.NewOption(label, toolName).Selected(true))
				sel.selected = append(sel.selected, toolName)
			}
		}
		fields = append(fields, sel)
		multiSelects = append(multiSelects, huh.NewMultiSelect[string]().
			Title(fmt.Sprintf("%s (%d tools)", name, totalTools)).
			Options(opts...).
			Value(&sel.selected))
	}

	if len(fields) == 0 {
		return selectionResult{}, nil
	}

	fieldsAny := make([]huh.Field, len(multiSelects))
	for i, ms := range multiSelects {
		fieldsAny[i] = ms
	}
	form := huh.NewForm(huh.NewGroup(fieldsAny...).
		Title("Select MCP servers/tools to load for this session").
		Description("Deselect a row to keep it out of the agent's context"))

	if logger != nil {
		logger.Info("cheap-context: rendering picker form", "fields", len(fields))
	}
	err := form.Run()
	if logger != nil {
		logger.Info("cheap-context: picker form returned", "err", err)
	}
	if err != nil {
		return selectionResult{}, fmt.Errorf("cheap-context prompt: %w", err)
	}

	result := selectionResult{excludedTools: map[string][]string{}}
	for _, sel := range fields {
		chosen := make(map[string]bool, len(sel.selected))
		for _, v := range sel.selected {
			chosen[v] = true
		}

		if !sel.multiGroup {
			if chosen[sel.serverName] {
				for _, c := range catalogs {
					if c.server.Name() == sel.serverName {
						result.kept = append(result.kept, c.server)
						break
					}
				}
			}
			continue
		}

		if !chosen[allRowKey] {
			continue // top row deselected: drop the whole server
		}
		var excluded []string
		for _, c := range catalogs {
			if c.server.Name() != sel.serverName {
				continue
			}
			result.kept = append(result.kept, c.server)
			for _, g := range c.groups {
				for _, toolName := range g.tools {
					if !chosen[toolName] {
						excluded = append(excluded, toolName)
					}
				}
			}
			break
		}
		if len(excluded) > 0 {
			result.excludedTools[sel.serverName] = excluded
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

	logger.Info("cheap-context: fetching tool catalogs", "server_count", len(started))
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
	logger.Info("cheap-context: catalogs fetched; entering picker", "catalog_count", len(catalogs))

	result, err := selectServers(catalogs, logger)
	logger.Info("cheap-context: picker returned", "err", err)
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

	for name, excludedTools := range result.excludedTools {
		srv, ok := serversByName[name]
		if !ok {
			continue
		}
		if err := pushExcludeTools(ctx, srv, excludedTools); err != nil {
			// Best-effort: a push failure means this server's deselected
			// tools stay visible, not that the launch fails.
			logger.Warn("cheap-context: failed to push tool exclusions; server keeps its full catalog",
				"server", name, "err", err)
		} else {
			logger.Info("cheap-context: excluded tools from server", "server", name, "count", len(excludedTools))
		}
	}

	return result.kept, nil
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
