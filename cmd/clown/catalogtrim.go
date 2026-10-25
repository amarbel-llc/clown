package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// catalogTrim is what a cheap-context selection removed from the agent's MCP
// tool catalog (clown#265): the servers it stopped and, per kept server, how
// many tools it hid. profile names the profile whose saved selection applied,
// "" for the interactive picker.
//
// Tool and group names are deliberately absent: they come from the servers'
// own catalogs, which a server controls, so nothing of them reaches the
// prompt but a count.
type catalogTrim struct {
	profile string
	stopped []string
	hidden  map[string]hiddenTools
}

// hiddenTools is how many tools a kept server hides. upTo marks a count of
// requested names rather than names matched against the live catalog (an
// in-unit server, whose catalog only the unit sees).
type hiddenTools struct {
	count int
	upTo  bool
}

// Bounds on the notice: server and profile names come from plugin manifests
// and profiles.toml, so each is shape-checked; each list shows at most
// maxCatalogTrimRows rows, and the whole notice stays within
// maxCatalogTrimBytes.
const (
	maxCatalogTrimRows  = 16
	maxCatalogTrimBytes = 2048
)

var (
	trimNameShape   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	trimServerShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}/[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
)

// catalogTrimFragment renders the prompt notice for t: which profile (or the
// picker) narrowed the catalog, the servers it stopped and how many tools
// each kept server hides. It is empty when nothing was stopped or hidden, so
// a launch without a narrowing selection keeps its prompt byte for byte.
func catalogTrimFragment(t catalogTrim) string {
	var stopped []string
	for _, s := range t.stopped {
		if trimServerShape.MatchString(s) {
			stopped = append(stopped, "- "+s)
		}
	}
	slices.Sort(stopped)
	stopped = slices.Compact(stopped)

	var hidden []string
	for s, h := range t.hidden {
		if h.count <= 0 || !trimServerShape.MatchString(s) {
			continue
		}
		noun := "tools"
		if h.count == 1 {
			noun = "tool"
		}
		upTo := ""
		if h.upTo {
			upTo = "up to "
		}
		hidden = append(hidden, fmt.Sprintf("- %s: %s%d %s hidden", s, upTo, h.count, noun))
	}
	slices.Sort(hidden)
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
	writeList("Tools hidden, by server:", hidden)
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
