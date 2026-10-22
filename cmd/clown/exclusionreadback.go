package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/clown/internal/pluginhost"
)

// exclusionReadback records what happened to one server's saved exclusions
// under a profile: which saved names resolved against the live catalog, and
// whether a fresh tools/list after the push still advertises any of them.
// Problem is empty only when the exclusions are confirmed applied; a launch
// under a profile refuses on any Problem (design v1 item 0).
type exclusionReadback struct {
	Server       string   `json:"server"`
	Saved        int      `json:"saved"`
	Matched      []string `json:"matched"`
	Unmatched    []string `json:"unmatched,omitempty"`
	Ambiguous    []string `json:"ambiguous,omitempty"`
	CatalogAfter int      `json:"catalog_after"`
	StillListed  []string `json:"still_listed,omitempty"`
	Problem      string   `json:"problem,omitempty"`
}

// exclusionReadbackRecord is the file a launch writes: one entry per server
// whose profile carried exclusions. Checks says what the readback proves:
// "listing", i.e. that the excluded tools are no longer advertised, not that
// a call naming one is refused (clown-stdio-bridge filters listing only).
type exclusionReadbackRecord struct {
	Version int                 `json:"version"`
	Checks  string              `json:"checks"`
	Profile string              `json:"profile,omitempty"`
	Servers []exclusionReadback `json:"servers"`
}

const (
	exclusionReadbackVersion = 1
	exclusionReadbackChecks  = "listing"
)

// applyAndReadBackExclusions resolves saved against live (the catalog
// fetched before the push), pushes the matched names, fetches the catalog
// again and checks that none of them is still listed.
func applyAndReadBackExclusions(ctx context.Context, host *pluginhost.Host, srv *pluginhost.ManagedServer, saved, live []string) exclusionReadback {
	rb := exclusionReadback{
		Server:    srv.Name,
		Saved:     len(saved),
		Matched:   resolveExcludedTools(saved, live),
		Unmatched: unmatchedExclusions(saved, live),
	}
	if live == nil {
		rb.Problem = "its tool catalog could not be fetched"
		return rb
	}
	if rb.Ambiguous = ambiguousExclusions(saved, live); len(rb.Ambiguous) > 0 {
		rb.Problem = "saved exclusions that could mean a live tool they do not match exactly: " + strings.Join(rb.Ambiguous, "; ")
		return rb
	}
	if len(rb.Matched) == 0 {
		rb.Problem = fmt.Sprintf("none of the profile's %d excluded tool names matches its %d-tool catalog", len(saved), len(live))
		return rb
	}
	if err := pushExcludeTools(ctx, srv, rb.Matched); err != nil {
		rb.Problem = fmt.Sprintf("pushing the exclusions failed: %v", err)
		return rb
	}
	after, ok := host.FetchToolCatalog(ctx, srv)
	if !ok {
		rb.Problem = "its tool catalog could not be fetched after the push"
		return rb
	}
	listed := make(map[string]bool, len(after))
	for _, t := range after {
		listed[t.Name] = true
	}
	rb.CatalogAfter = len(after)
	for _, name := range rb.Matched {
		if listed[name] {
			rb.StillListed = append(rb.StillListed, name)
		}
	}
	if len(rb.StillListed) > 0 {
		rb.Problem = fmt.Sprintf("%d excluded tools are still listed after the push", len(rb.StillListed))
	}
	return rb
}

// ambiguousExclusions describes each saved exclusion name that could mean a
// live tool without naming exactly one: an unmatched name whose loose form
// equals a live tool's (the profile meant that tool, so it would stay
// visible), and a name that matches more than one live tool. An unmatched
// name with no loose counterpart is harmless: the tool is not there.
func ambiguousExclusions(saved, live []string) []string {
	loose := map[string][]string{}
	for _, name := range live {
		k := looseToolName(name)
		loose[k] = append(loose[k], name)
	}
	unmatched := map[string]bool{}
	for _, name := range unmatchedExclusions(saved, live) {
		unmatched[name] = true
	}
	var out []string
	for _, name := range saved {
		if unmatched[name] {
			if cands := loose[looseToolName(name)]; len(cands) > 0 {
				out = append(out, fmt.Sprintf("%q matches no tool exactly but could mean %s", name, strings.Join(cands, ", ")))
			}
			continue
		}
		if hits := resolveExcludedTools([]string{name}, live); len(hits) > 1 {
			out = append(out, fmt.Sprintf("%q matches %d tools: %s", name, len(hits), strings.Join(hits, ", ")))
		}
	}
	return out
}

// looseToolName folds the ways one tool's name gets written: case, claude's
// "mcp__<plugin>__" prefix, and '.', '-' and '_' as one separator.
func looseToolName(name string) string {
	name = strings.ToLower(name)
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		if _, tool, ok := strings.Cut(rest, "__"); ok {
			name = tool
		}
	}
	return strings.Map(func(r rune) rune {
		if r == '.' || r == '-' {
			return '_'
		}
		return r
	}, name)
}

// toolNames returns the names of tools, or nil when ok is false, so a failed
// fetch stays distinguishable from an empty catalog.
func toolNames(tools []pluginhost.ToolInfo, ok bool) []string {
	if !ok {
		return nil
	}
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

// exclusionRefusal is the launch-refusing error for the readbacks with a
// Problem, or nil when every one is confirmed.
func exclusionRefusal(profileName string, rbs []exclusionReadback) error {
	var problems []string
	for _, rb := range rbs {
		if rb.Problem != "" {
			problems = append(problems, fmt.Sprintf("%s: %s", rb.Server, rb.Problem))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("profile %s: tool exclusions not confirmed, refusing to launch: %s", profileName, strings.Join(problems, "; "))
}

// recordFrontendExclusionReadback writes the readbacks of the servers this
// frontend started to $XDG_STATE_HOME/clown/readback/<principal>.json. A
// launch with no profile exclusions writes nothing; a write failure is logged,
// never fatal (the refusal, not the record, is what protects the launch).
func recordFrontendExclusionReadback(flags parsedFlags, rbs []exclusionReadback, logger *slog.Logger) {
	if len(rbs) == 0 || flags.cheapContextProfile == nil {
		return
	}
	path, err := userStateWritePath("readback", flags.identity.Key+".json")
	if err == nil {
		err = writeExclusionReadback(path, exclusionReadbackRecord{
			Version: exclusionReadbackVersion,
			Checks:  exclusionReadbackChecks,
			Profile: flags.cheapContextProfile.Name,
			Servers: rbs,
		})
	}
	if err != nil {
		logger.Warn("writing the exclusion readback record failed", "err", err)
		return
	}
	logger.Info("wrote the exclusion readback record", "path", path, "servers", len(rbs))
}

// writeExclusionReadback writes rec to path (0600), creating its directory.
func writeExclusionReadback(path string, rec exclusionReadbackRecord) error {
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}
