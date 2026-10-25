package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/huh"

	"code.linenisgreat.com/clown/internal/agentscope"
	"code.linenisgreat.com/clown/internal/pluginhost"
	"code.linenisgreat.com/clown/internal/profile"
)

// cheapContextSaveContext carries the profile data run() already loaded once
// (builtin+user merged, the raw user set, and the user set's file path)
// through to promptSaveSelection, so a --cheap-context save prompt doesn't
// re-read and re-parse profiles.toml a second time in the same launch
// (clown#178).
type cheapContextSaveContext struct {
	merged []profile.Profile
	user   []profile.Profile
	path   string
}

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
// deferred to clown#175 rather than built here.
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
	// inUnit marks a server reserved to start inside an agent unit
	// (--agent-scope, FDR 0020): the frontend never starts it, so it has no
	// catalog to fetch, and its saved exclusions are applied inside the unit
	// once `clown agent-relay` has started it (clown#290).
	inUnit bool
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

// rowKey builds a globally-unique option value for one picker row: server
// name plus a row id, NUL-separated so it can never collide with a real
// server or tool name. allRowSuffix identifies a server's whole-server
// on/off row; any other suffix is a literal tool name.
func rowKey(serverName, rowID string) string {
	return serverName + "\x00" + rowID
}

// allRowSuffix is the row id for a multi-group server's top "(all)" row:
// unchecking it means "drop this whole server," independent of which
// individual tool rows are checked (mirrors v1's whole-server semantics).
const allRowSuffix = "\x00all"

// groupRowKey builds the row key for an intermediate moxin/group row nested
// under a server (Depth 1, between the server row and its individual tool
// rows). Prefixed with "\x00group\x00" so a group named e.g. "all" can
// never collide with allRowSuffix or a same-named tool.
func groupRowKey(serverName, groupName string) string {
	return rowKey(serverName, "\x00group\x00"+groupName)
}

// cheapContextShouldActivate decides whether runManaged's --cheap-context
// selection-application path (applyCheapContextSelection) runs at all.
// --cheap-context's only remaining job, once a profile carries a saved
// selection, is "show the interactive picker" — applying an already-saved
// selection must not additionally require the flag, or a saved profile
// would be dead weight without also re-passing --cheap-context every
// launch. A resolved --profile with a non-nil ContextServers is sufficient
// on its own to activate the (picker-skipping) selection-application path.
func cheapContextShouldActivate(cheapContextFlag bool, resolvedProfile *profile.Profile) bool {
	return cheapContextFlag || (resolvedProfile != nil && resolvedProfile.ContextServers != nil)
}

// selectionFromSavedProfile builds a selectionResult directly from a
// profile's saved --cheap-context selection (profile.ContextServers /
// profile.ContextExcluded), filtered against catalogs — the CURRENT
// launch's live, fetched tool data. A saved server or tool name absent from
// catalogs is silently dropped rather than erroring: the catalog is the
// source of truth (a plugin set or a server's tool names can differ across
// directories/launches/versions), the saved selection is best-effort intent
// to replay, not a hard requirement. ok is false when saved has no
// ContextServers at all (an empty/legacy profile), signaling the caller to
// fall back to the interactive picker instead.
func selectionFromSavedProfile(saved *profile.Profile, catalogs []serverCatalog) (selectionResult, bool) {
	if saved == nil || saved.ContextServers == nil {
		return selectionResult{}, false
	}

	keepNames := make(map[string]bool, len(saved.ContextServers))
	for _, name := range saved.ContextServers {
		keepNames[name] = true
	}

	result := selectionResult{excludedTools: map[string][]string{}}
	for _, c := range catalogs {
		name := c.server.Name()
		if !keepNames[name] {
			continue // not in the saved keep-set: drop, same as v1 whole-server deselection
		}
		result.kept = append(result.kept, c.server)

		savedExcluded, hasExclusions := saved.ContextExcluded[name]
		if !hasExclusions {
			continue
		}
		if c.inUnit {
			// No live catalog on the frontend: hand the saved names to the
			// unit, whose relay resolves them against the catalog it fetches.
			result.excludedTools[name] = append([]string(nil), savedExcluded...)
			continue
		}
		var liveTools []string
		for _, g := range c.groups {
			liveTools = append(liveTools, g.tools...)
		}
		// A saved tool name absent from the live catalog is silently dropped:
		// it no longer exists, so there is nothing to exclude.
		if excluded := resolveExcludedTools(savedExcluded, liveTools); len(excluded) > 0 {
			result.excludedTools[name] = excluded
		}
	}
	return result, true
}

// resolveExcludedTools maps saved exclusion names onto the names a server
// actually renders, which is what its /clown/exclude-tools matches. A saved
// name matches a live one exactly or by the live name's claude-safe form:
// claude shows moxy's "grit.branch" as "grit_branch", and the builtin
// profiles' lists were taken from claude transcripts (clown#291). The result
// is in live order, without duplicates.
func resolveExcludedTools(saved, live []string) []string {
	want := make(map[string]bool, len(saved))
	for _, name := range saved {
		want[name] = true
	}
	var out []string
	for _, name := range live {
		if want[name] || want[claudeSafeToolName(name)] {
			out = append(out, name)
		}
	}
	return out
}

// unmatchedExclusions are the saved names resolveExcludedTools matches to no
// live name, for the log that tells "pushed" from "matched".
func unmatchedExclusions(saved, live []string) []string {
	have := make(map[string]bool, 2*len(live))
	for _, name := range live {
		have[name] = true
		have[claudeSafeToolName(name)] = true
	}
	var out []string
	for _, name := range saved {
		if !have[name] {
			out = append(out, name)
		}
	}
	return out
}

// claudeSafeToolName is name with every character outside [A-Za-z0-9_-]
// replaced by "_", the form claude gives an MCP tool name.
func claudeSafeToolName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '_'
	}, name)
}

// selectServers renders --cheap-context's picker as ONE screen and ONE
// continuously-scrollable list: a bare bubbles/list program
// (cmd/clown/cheapcontext_picker.go), not a huh.Form. Two huh limitations
// ruled it out: (1) a huh.MultiSelect's Up/Down cursor clamps at that
// field's own boundary and only crosses to the next field on Enter/Tab, so
// one field per server made j/k/arrow navigation stop dead at each
// section; (2) huh.MultiSelect exposes no hook to intercept a toggle and
// cascade it to other rows, which live "toggling a server also toggles its
// tools" needs. bubbles/list's ItemDelegate.Update runs BEFORE the list
// consumes a keypress, which is exactly that hook — see
// checklistDelegate.Update.
//
// A server with no fetched catalog or a single tool group gets a single
// bare on/off row (v1 behavior). A multi-group server (v2; moxy's ~170
// tools) gets a "<server> (N tools)" parent row plus one indented child
// row per individual tool — toggling the parent row live-cascades to every
// child (checklistDelegate.Update); toggling a child row independently
// excludes just that tool while the parent stays checked. A true
// collapsible outline/tree (per-moxin expand/collapse, so ~170 rows aren't
// always all visible) is deferred to clown#176.
//
// Requires an interactive TTY — mirrors the TTY gate in
// profileAddInteractive/profileEditInteractive (cmd/clown/profileform.go).
// catalogs must cover every started server (fetched via
// host.FetchToolCatalog in runManaged, after StartAll); a nil/empty groups
// field degrades that server to a bare on/off row.
//
// When saved carries a saved --cheap-context selection (profile.Profile's
// ContextServers is non-nil), the picker is skipped entirely and the
// selection is replayed via selectionFromSavedProfile instead — this is the
// one case where selectServers works WITHOUT an interactive TTY, since
// there is nothing left to prompt for. saved may be nil (no --profile
// resolved, or the resolved profile has no saved selection), in which case
// behavior is unchanged from before persistence existed.
func selectServers(catalogs []serverCatalog, logger *slog.Logger, saved *profile.Profile, saveCtx cheapContextSaveContext) (selectionResult, error) {
	if result, ok := selectionFromSavedProfile(saved, catalogs); ok {
		logger.Info("cheap-context: replaying saved selection from profile", "profile", saved.Name)
		return result, nil
	}

	if !pluginhost.IsInteractive() {
		return selectionResult{}, fmt.Errorf("--cheap-context requires an interactive TTY")
	}
	if len(catalogs) == 0 {
		return selectionResult{}, nil
	}

	var rows []checklistRow
	for _, c := range catalogs {
		name := c.server.Name()
		if !isMultiGroup(c.groups) {
			rows = append(rows, checklistRow{Key: rowKey(name, allRowSuffix), Label: name, Checked: true})
			continue
		}

		totalTools := 0
		for _, g := range c.groups {
			totalTools += len(g.tools)
		}
		serverKey := rowKey(name, allRowSuffix)
		rows = append(rows, checklistRow{
			Key:      serverKey,
			Label:    fmt.Sprintf("%s (%d tools)", name, totalTools),
			Checked:  true,
			IsParent: true,
			Depth:    0,
		})
		for _, g := range c.groups {
			groupLabel := g.name
			if groupLabel == "" {
				groupLabel = "(ungrouped)"
			}
			groupKey := groupRowKey(name, g.name)
			rows = append(rows, checklistRow{
				Key:       groupKey,
				Label:     fmt.Sprintf("%s (%d tools)", groupLabel, len(g.tools)),
				Checked:   true,
				IsParent:  true,
				ParentKey: serverKey,
				Depth:     1,
			})
			for _, toolName := range g.tools {
				rows = append(rows, checklistRow{
					Key:       rowKey(name, toolName),
					Label:     toolName,
					Checked:   true,
					ParentKey: groupKey,
					Depth:     2,
				})
			}
		}
	}

	if len(rows) == 0 {
		return selectionResult{}, nil
	}

	chosen, ok, err := runChecklistPicker("Select MCP servers/tools to load for this session", rows)
	if err != nil {
		return selectionResult{}, fmt.Errorf("cheap-context prompt: %w", err)
	}
	if !ok {
		// User cancelled (q/ctrl+c/esc): treat as if --cheap-context had not
		// been passed rather than silently dropping every server.
		result := selectionResult{excludedTools: map[string][]string{}}
		for _, c := range catalogs {
			result.kept = append(result.kept, c.server)
		}
		return result, nil
	}

	result := selectionResult{excludedTools: map[string][]string{}}
	for _, c := range catalogs {
		name := c.server.Name()
		if !chosen[rowKey(name, allRowSuffix)] {
			continue // whole-server row deselected: drop it entirely
		}
		result.kept = append(result.kept, c.server)
		if !isMultiGroup(c.groups) {
			continue
		}
		var excluded []string
		for _, g := range c.groups {
			for _, toolName := range g.tools {
				if !chosen[rowKey(name, toolName)] {
					excluded = append(excluded, toolName)
				}
			}
		}
		if len(excluded) > 0 {
			result.excludedTools[name] = excluded
		}
	}

	if err := promptSaveSelection(result, saveCtx); err != nil {
		// A save failure (bad name, write error, user aborts the save
		// sub-prompt) never fails the launch — the selection still applies
		// for this session, it just isn't persisted.
		logger.Warn("cheap-context: selection not saved", "err", err)
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
//
// inUnit are the servers reserved to start inside an agent unit
// (--agent-scope): they take part in the selection by name only, and their
// exclusions are returned in inUnitExcluded for the unit to push, since
// nothing here can reach them (clown#290).
//
// Under a profile's saved selection the exclusions are not best-effort: each
// started server's saved names are resolved, pushed and read back, and any
// server whose exclusions are not confirmed refuses the launch. The readbacks
// are returned either way, for the launch's readback record.
func applyCheapContextSelection(
	ctx context.Context,
	host *pluginhost.Host,
	discovered []pluginhost.DiscoveredServer,
	started []*pluginhost.ManagedServer,
	inUnit []pluginhost.DiscoveredServer,
	logger *slog.Logger,
	savedProfile *profile.Profile,
	saveCtx cheapContextSaveContext,
) (cheapContextOutcome, error) {
	byName := make(map[string]pluginhost.DiscoveredServer, len(discovered))
	for _, d := range discovered {
		byName[d.Name()] = d
	}

	catalogs := make([]serverCatalog, 0, len(started))
	serversByName := make(map[string]*pluginhost.ManagedServer, len(started))
	liveNames := make(map[string][]string, len(started))
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
		liveNames[srv.Name] = toolNames(tools, ok)
		catalogs = append(catalogs, serverCatalog{server: d, groups: groups})
	}
	for _, d := range inUnit {
		catalogs = append(catalogs, serverCatalog{server: d, inUnit: true})
	}

	result, err := selectServers(catalogs, logger, savedProfile, saveCtx)
	if err != nil {
		return cheapContextOutcome{}, err
	}

	keptNames := make(map[string]bool, len(result.kept))
	for _, d := range result.kept {
		keptNames[d.Name()] = true
	}
	replayed := savedProfile != nil && savedProfile.ContextServers != nil
	out := cheapContextOutcome{kept: result.kept, inUnitExcluded: map[string][]string{}}
	if replayed {
		out.trim.profile = savedProfile.Name
	}
	stopped := 0
	for name, srv := range serversByName {
		if !keptNames[name] {
			srv.Stop()
			stopped++
			out.trim.stopped = append(out.trim.stopped, name)
		}
	}

	inUnitNames := make(map[string]bool, len(inUnit))
	for _, d := range inUnit {
		inUnitNames[d.Name()] = true
		if !keptNames[d.Name()] {
			out.trim.stopped = append(out.trim.stopped, d.Name())
		}
	}
	slices.Sort(out.trim.stopped)
	hide := func(name string, tools []string) {
		if out.trim.hidden == nil {
			out.trim.hidden = map[string][]string{}
		}
		out.trim.hidden[name] = tools
	}
	pushedExclusions, pushFailures := 0, 0
	if replayed {
		// The raw saved lists, not result.excludedTools: a server whose saved
		// names all fail to match must still be judged, and refused.
		for _, d := range result.kept {
			name := d.Name()
			saved := savedProfile.ContextExcluded[name]
			if len(saved) == 0 {
				continue
			}
			if inUnitNames[name] {
				out.inUnitExcluded[name] = append([]string(nil), saved...)
				hide(name, out.inUnitExcluded[name])
				continue
			}
			srv, ok := serversByName[name]
			if !ok {
				continue
			}
			rb := applyAndReadBackExclusions(ctx, host, srv, saved, liveNames[name])
			logger.Info("cheap-context: tool exclusions read back", "server", name, "saved", rb.Saved,
				"matched", len(rb.Matched), "unmatched", rb.Unmatched, "still_listed", rb.StillListed, "problem", rb.Problem)
			out.readbacks = append(out.readbacks, rb)
			if rb.Problem == "" {
				hide(name, rb.Matched)
			}
		}
	} else {
		for name, excludedTools := range result.excludedTools {
			if inUnitNames[name] {
				out.inUnitExcluded[name] = excludedTools
				hide(name, excludedTools)
				continue
			}
			srv, ok := serversByName[name]
			if !ok {
				continue
			}
			if err := pushExcludeTools(ctx, srv, excludedTools); err != nil {
				// Best-effort without a profile: a push failure means this
				// server's deselected tools stay visible, not that the launch fails.
				logger.Warn("cheap-context: failed to push tool exclusions; server keeps its full catalog",
					"server", name, "err", err)
				pushFailures++
			} else {
				pushedExclusions++
				hide(name, excludedTools)
			}
		}
	}

	logger.Info("cheap-context: selection applied",
		"catalogs_fetched", len(catalogs), "servers_stopped", stopped,
		"servers_with_exclusions", pushedExclusions, "exclusion_push_failures", pushFailures,
		"readbacks", len(out.readbacks),
		"in_unit_servers", len(inUnit), "in_unit_with_exclusions", len(out.inUnitExcluded))
	if replayed {
		if err := exclusionRefusal(savedProfile.Name, out.readbacks); err != nil {
			return out, err
		}
	}
	return out, nil
}

// cheapContextOutcome is what applyCheapContextSelection decided: the kept
// servers, the exclusions the agent unit pushes for its servers, and the
// readbacks of the exclusions pushed here under a profile.
type cheapContextOutcome struct {
	kept           []pluginhost.DiscoveredServer
	inUnitExcluded map[string][]string
	readbacks      []exclusionReadback
	// trim is what the selection removed, for the prompt notice
	// (catalogTrimFragment, clown#265).
	trim catalogTrim
}

// narrowInUnitServers drops the in-unit servers a selection did not keep, so
// the relay never starts them, and gives each kept one the exclusions it
// pushes once started.
func narrowInUnitServers(servers []agentscope.InUnitServer, kept []pluginhost.DiscoveredServer, excluded map[string][]string) []agentscope.InUnitServer {
	keptNames := make(map[string]bool, len(kept))
	for _, d := range kept {
		keptNames[d.Name()] = true
	}
	var out []agentscope.InUnitServer
	for _, s := range servers {
		if !keptNames[s.Name] {
			continue
		}
		s.Exclude = excluded[s.Name]
		out = append(out, s)
	}
	return out
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
	hs := srv.Handshake()
	url := strings.TrimSuffix(hs.URL(), "/mcp") + "/clown/exclude-tools"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hs.Client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

// promptSaveSelection offers to persist a freshly-confirmed --cheap-context
// selection into a named profile, so a later `--cheap-context --profile
// <name>` launch can replay it via selectionFromSavedProfile instead of
// showing the picker again. Called only after a real (non-cancelled,
// non-replayed) picker confirmation — selectServers' cancel path and its
// saved-selection replay path both skip this, since there is nothing new to
// save in either case.
//
// Errors here are the caller's responsibility to treat as non-fatal (see
// selectServers) — a user declining to save, or a save failure, must never
// fail the launch that already has a valid, applied selection. saveCtx
// carries the profile data run() already loaded once for --profile
// resolution/the profile picker, so this function doesn't re-read
// profiles.toml a second time in the same launch (clown#178).
func promptSaveSelection(result selectionResult, saveCtx cheapContextSaveContext) error {
	if len(result.kept) == 0 {
		// Fail fast, before either interactive prompt below runs: an empty
		// ContextServers can never round-trip through Save/Load (see
		// profile.Validate's matching check, the durable backstop for every
		// caller — this early return is purely so the user doesn't answer
		// two prompts before hitting that same rejection). Deselecting every
		// server also has no meaningful "replay" semantics anyway: there
		// would be nothing left to apply an exclusion list to.
		return fmt.Errorf("nothing to save: every server was deselected")
	}

	var wantSave bool
	if err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Save this --cheap-context selection for reuse?").
			Affirmative("Save").
			Negative("Skip").
			Value(&wantSave),
	)).Run(); err != nil {
		return fmt.Errorf("save prompt: %w", err)
	}
	if !wantSave {
		return nil
	}

	merged, user, destPath := saveCtx.merged, saveCtx.user, saveCtx.path
	existingNames := userNameSet(user)

	var name string
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Save as which profile name?").
			Description("an existing name updates that profile's saved selection; a new name creates a minimal profile").
			Value(&name).
			Validate(validateProfileName(map[string]bool{}, "")), // any existing name (user or builtin) is a valid target here, unlike profile add's uniqueness check
	)).Run(); err != nil {
		return fmt.Errorf("save prompt: %w", err)
	}
	name = strings.TrimSpace(name)

	servers := make([]string, len(result.kept))
	for i, d := range result.kept {
		servers[i] = d.Name()
	}

	var target profile.Profile
	for _, p := range merged {
		if p.Name == name {
			target = p
			break
		}
	}
	if target.Name == "" {
		// New profile: minimal viable record. Provider defaults to "claude"
		// (cheap-context's own current scope — moxy/clown.json MCP servers
		// are launched under the claude provider path) since there is no
		// launch-time provider context available at this call site to
		// thread through; a user who wants a different provider on this
		// profile can adjust it afterward via `clown profile edit`.
		target = profile.Profile{Name: name, Provider: "claude", Backend: "anthropic"}
	}
	target.ContextServers = servers
	target.ContextExcluded = result.excludedTools

	// Reuse saveProfileForm's Validate -> Upsert -> Save -> print sequence
	// (cmd/clown/profileform.go) rather than re-deriving it here — v.Confirm
	// is forced true since the earlier huh.Confirm in this function already
	// captured that decision; editOriginal is empty because this path never
	// renames a profile (name IS both the lookup key and the saved name).
	v := valuesFromProfile(target)
	v.Confirm = true
	if _, err := saveProfileForm(v, user, "", destPath); err != nil {
		return fmt.Errorf("cheap-context: %w", err)
	}
	if !existingNames[name] {
		fmt.Printf("cheap-context: saved selection to new profile %q\n", name)
	} else {
		fmt.Printf("cheap-context: saved selection to profile %q\n", name)
	}
	return nil
}
