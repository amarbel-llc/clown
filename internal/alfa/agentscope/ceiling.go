package agentscope

import (
	"fmt"
	"slices"
	"strings"
)

// The in-unit moxy's ceiling (V3 structural moxy, clown#339 option B;
// docs/plans/2026-11-03-structural-moxy-v3-build-plan.md). It is the
// allow-tools list of the read-only global moxyfile the unit mounts, written
// in moxy's own form: a bare name allows a whole moxin, a dotted "moxin.tool"
// pair one tool. moxy enforces that a child moxyfile (anything the worker can
// write below the global one) only narrows it (moxyfile(5) NARROWING), and
// permission-mode = "enforce" refuses tools the ceiling does not list.

// DefaultCeiling is the operator-ratified default (2026-11-03): the pure
// applets, env and man whole; rg's search; folio's read side and the write
// side a worker needs to add, rename or remove a file. Each path argument of
// these is judged per resource by spinclass's policy broker ([folio] rules);
// the excluded folio tools (cp, link, chmod, du, df, tar) are the ones whose
// second argument or effect is not a plain path check. restart is refused
// under enforce; batch/async/status are not needed and unverified under it;
// grit stays spinclass's (FDR 0036); chix and jq are a separate decision.
var DefaultCeiling = []string{
	"toolbox", "env", "man",
	"rg.search",
	"folio.read", "folio.ls", "folio.glob", "folio.tree", "folio.file-type", "folio.mktemp",
	"folio.write", "folio.mkdir", "folio.mv", "folio.rm",
}

// DefaultDisabledMoxins are the moxins a confined worker never sees, with
// the reason it loses them (FDR 0020 "What a confined worker loses"): smith
// (writes to forges; a confined worker files through issue-filer), chrest
// (drives the operator's browser), the Google moxins (credentials under the
// hidden home), freud (operator transcripts), stats-me (a host-loopback
// service the unit cannot reach), grit (spinclass's grit is the worker's
// git), chix and jq (not in the ratified default).
var DefaultDisabledMoxins = []string{
	"smith", "chrest", "calendar", "car", "gmail", "gws", "piers", "prison", "freud", "stats-me",
	"grit", "chix", "jq",
}

// CeilingMoxyfile renders the read-only global moxyfile for an in-unit moxy:
// the allow-tools ceiling, the disabled moxins, and permission-mode enforce.
// The same inputs always render the same bytes (sorted, deduplicated), so
// the file's digest identifies the ceiling. A caller that wants the default
// passes DefaultCeiling and DefaultDisabledMoxins.
func CeilingMoxyfile(allow, disable []string) []byte {
	var b strings.Builder
	b.WriteString("# Written by clown for an agent unit (clown#244, FDR 0020; V3 ceiling, clown#339).\n")
	b.WriteString("# Read-only inside the unit. moxy enforces that nothing below this file widens it\n")
	b.WriteString("# (MOXY_PARENT_MOXYFILE, moxyfile(5) NARROWING); enforce refuses tools not listed.\n")
	b.WriteString("permission-mode = \"enforce\"\n")
	writeList(&b, "allow-tools", sortedUnique(allow))
	writeList(&b, "disable-moxins", sortedUnique(disable))
	return []byte(b.String())
}

func writeList(b *strings.Builder, key string, items []string) {
	fmt.Fprintf(b, "%s = [", key)
	for i, it := range items {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(b, "\n  %q", it)
	}
	if len(items) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("]\n")
}

func sortedUnique(items []string) []string {
	out := slices.Clone(items)
	slices.Sort(out)
	return slices.Compact(out)
}

// IntersectAllow narrows a ceiling: it returns the entries of requested that
// the default holds, in sorted order, and refuses any entry the default does
// not cover, naming them. An entry is covered by the same entry, or, for a
// dotted "moxin.tool" pair, by the default's bare moxin name (the parent
// holding the whole moxin); a bare name in requested is covered only by the
// same bare name (a whole moxin is never narrower than one of its tools).
// A nil requested means the default itself.
func IntersectAllow(def, requested []string) ([]string, error) {
	if requested == nil {
		return sortedUnique(def), nil
	}
	has := map[string]bool{}
	for _, d := range def {
		has[d] = true
	}
	var out, refused []string
	for _, r := range sortedUnique(requested) {
		moxin, _, dotted := strings.Cut(r, ".")
		switch {
		case has[r], dotted && has[moxin]:
			out = append(out, r)
		default:
			refused = append(refused, r)
		}
	}
	if len(refused) > 0 {
		return nil, fmt.Errorf("moxy ceiling: %s not in the default ceiling (a profile or spec may only narrow it)", strings.Join(refused, ", "))
	}
	return out, nil
}

// InUnitMoxyfile is the default read-only global moxyfile of an in-unit moxy,
// bound over $HOME/.config/moxy/moxyfile inside the unit: the ratified
// DefaultCeiling under enforce.
var InUnitMoxyfile = string(CeilingMoxyfile(DefaultCeiling, DefaultDisabledMoxins))
