package main

import (
	"slices"
	"strings"
)

// sessionIdentityHeading opens the identity section troupe's chat fragment
// and ringmaster's job fragment each carry (clown#294).
const sessionIdentityHeading = "## Session Identity"

// identitySection is one fragment split around its Session Identity section.
type identitySection struct {
	before, body, after string
}

// cutIdentitySection splits frag at its "## Session Identity" line: the text
// before it, the section body (up to the next "## " heading), and the rest.
func cutIdentitySection(frag string) (identitySection, bool) {
	lines := strings.Split(frag, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == sessionIdentityHeading {
			start = i
			break
		}
	}
	if start < 0 {
		return identitySection{}, false
	}
	end := len(lines)
	for j := start + 1; j < len(lines); j++ {
		if strings.HasPrefix(lines[j], "## ") {
			end = j
			break
		}
	}
	trim := func(ls []string) string { return strings.TrimSpace(strings.Join(ls, "\n")) }
	return identitySection{before: trim(lines[:start]), body: trim(lines[start+1 : end]), after: trim(lines[end:])}, true
}

// mergeSessionIdentity composes the dynamic fragments under ONE "## Session
// Identity" heading (clown#294): the first fragment that carries the section
// keeps it, with every distinct body from the others under it in fragment
// order; the others lose their copy (a fragment left empty is dropped). Each
// plugin's fragment is self-contained, so without this the heading repeats
// once per job-platform server. Fragments are otherwise unchanged, and a
// launch with at most one such section gets them back as they were.
func mergeSessionIdentity(frags []string) []string {
	sections := make([]*identitySection, len(frags))
	n := 0
	for i, f := range frags {
		if s, ok := cutIdentitySection(f); ok {
			sections[i] = &s
			n++
		}
	}
	if n < 2 {
		return frags
	}
	var bodies []string
	for _, s := range sections {
		if s != nil && s.body != "" && !slices.Contains(bodies, s.body) {
			bodies = append(bodies, s.body)
		}
	}
	join := func(parts ...string) string {
		var keep []string
		for _, p := range parts {
			if p != "" {
				keep = append(keep, p)
			}
		}
		return strings.Join(keep, "\n\n")
	}
	out := make([]string, 0, len(frags))
	merged := false
	for i, f := range frags {
		s := sections[i]
		switch {
		case s == nil:
			out = append(out, f)
		case !merged:
			merged = true
			out = append(out, join(s.before, sessionIdentityHeading, join(bodies...), s.after))
		default:
			if rest := join(s.before, s.after); rest != "" {
				out = append(out, rest)
			}
		}
	}
	return out
}
