package main

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// catalogTrim is what a cheap-context selection removed from the agent's MCP
// tool catalog (clown#265): the servers it stopped and, per kept server, the
// tool names it hid. profile names the profile whose saved selection applied,
// "" for the interactive picker.
type catalogTrim struct {
	profile string
	stopped []string
	hidden  map[string][]string
}

// Bounds on the notice: names come from profiles.toml and plugin manifests,
// so each is shape-checked, each list shows at most maxCatalogTrimRows rows
// and each server row at most maxCatalogTrimGroups groups, and the whole
// notice stays within maxCatalogTrimBytes.
const (
	maxCatalogTrimRows   = 16
	maxCatalogTrimGroups = 24
	maxCatalogTrimBytes  = 2048
)

var (
	trimNameShape   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	trimServerShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}/[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
)

// toolGroupName is the group a tool name belongs to: the part before its
// first "." (moxy's own rendering, "folio.read") or "_" (the claude-safe form
// a saved selection uses, "folio_read"), or the whole name.
func toolGroupName(tool string) string {
	if i := strings.IndexAny(tool, "._"); i > 0 {
		return tool[:i]
	}
	return tool
}

// catalogTrimFragment renders the prompt notice for t: which profile (or the
// picker) narrowed the catalog, the servers it stopped and the tool groups it
// hid, by name only. It is empty when nothing was stopped or hidden, so a
// launch without a narrowing selection keeps its prompt byte for byte.
func catalogTrimFragment(t catalogTrim) string {
	var stopped []string
	for _, s := range t.stopped {
		if trimServerShape.MatchString(s) {
			stopped = append(stopped, "- "+s)
		}
	}
	slices.Sort(stopped)
	stopped = slices.Compact(stopped)

	servers := make([]string, 0, len(t.hidden))
	for s := range t.hidden {
		servers = append(servers, s)
	}
	sort.Strings(servers)
	var hidden []string
	for _, s := range servers {
		if !trimServerShape.MatchString(s) {
			continue
		}
		count := 0
		var groups []string
		for _, tool := range t.hidden[s] {
			if !trimNameShape.MatchString(tool) {
				continue
			}
			count++
			if g := toolGroupName(tool); !slices.Contains(groups, g) {
				groups = append(groups, g)
			}
		}
		if count == 0 {
			continue
		}
		sort.Strings(groups)
		noun := "tools"
		if count == 1 {
			noun = "tool"
		}
		shown := groups
		suffix := ""
		if len(groups) > maxCatalogTrimGroups {
			shown = groups[:maxCatalogTrimGroups]
			suffix = fmt.Sprintf("; %d more groups", len(groups)-maxCatalogTrimGroups)
		}
		hidden = append(hidden, fmt.Sprintf("- %s: %d %s (%s%s)", s, count, noun, strings.Join(shown, ", "), suffix))
	}
	if len(stopped) == 0 && len(hidden) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## MCP tool catalog narrowed\n\n")
	switch {
	case t.profile != "" && trimNameShape.MatchString(t.profile):
		fmt.Fprintf(&b, "This session's MCP tools were narrowed on purpose by profile %q (its saved selection).", t.profile)
	case t.profile != "":
		b.WriteString("This session's MCP tools were narrowed on purpose by a profile's saved selection.")
	default:
		b.WriteString("This session's MCP tools were narrowed on purpose in the --cheap-context picker.")
	}
	b.WriteString(" A tool missing for that reason is not broken: do not look for a way around it." +
		" If a task needs one, say so to whoever launched this session.")
	const noteRoom = 40
	fits := func(line string) bool { return b.Len()+1+len(line)+noteRoom <= maxCatalogTrimBytes }
	writeList := func(label string, rows []string) {
		if len(rows) == 0 || !fits("\n"+label) {
			return
		}
		b.WriteString("\n\n" + label)
		shown := 0
		for _, r := range rows {
			if shown == maxCatalogTrimRows || !fits(r) {
				break
			}
			b.WriteString("\n" + r)
			shown++
		}
		if more := len(rows) - shown; more > 0 {
			fmt.Fprintf(&b, "\n- (%d more not shown)", more)
		}
	}
	writeList("Servers stopped:", stopped)
	writeList("Tools hidden, by server (tool groups):", hidden)
	return b.String()
}

// dynamicPromptFragments is what runManaged appends to the prompt file once
// the servers are up: the servers' own fragments, then the catalog notice
// when a selection narrowed the catalog. Without one it is the fetched
// fragments alone, as before clown#265.
func dynamicPromptFragments(fetched []string, trim catalogTrim) []string {
	frags := mergeSessionIdentity(fetched)
	if notice := catalogTrimFragment(trim); notice != "" {
		frags = append(frags, notice)
	}
	return frags
}
