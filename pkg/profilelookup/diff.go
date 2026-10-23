package profilelookup

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxDiffLineRunes is the longest line Diff returns, in runes.
const MaxDiffLineRunes = 80

// RedactedURL is what Diff prints in place of a URL that may carry a
// credential.
const RedactedURL = "<redacted: may contain credentials>"

// Diff describes, one change per line, how new differs from old, for an
// approver who must see what a profile transition changes rather than two
// opaque digests (FDR 0022, decision 4). It reads the canonical fields
// (Resolved.Canonical) and never changes them.
//
// Contract:
//
//   - Order is deterministic: fields in the fixed order name, provider,
//     backend, model, url, token_set, env, context_servers,
//     context_excluded, base; within env, context_servers and
//     context_excluded, keys (and tools) sorted bytewise. display is not
//     compared.
//   - Each line is printable (every rune passes unicode.IsPrint: values are
//     quoted with strconv.QuoteToGraphic, so a newline, tab, escape or
//     bidi-control in, say, a model name appears escaped) and at most
//     MaxDiffLineRunes runes; a longer line is cut and ends in "…".
//   - Secrets are never printed. The token appears only as set/unset. env
//     appears only as `env "KEY": added|removed|changed`, decided by
//     comparing the per-value digests; neither the values nor the digests
//     are printed.
//   - url is printed verbatim unless it may carry a credential, then
//     RedactedURL is printed instead. The rule is conservative: a URL is
//     redacted when it does not parse, or has userinfo (user or user:pass@),
//     or any query string, or any fragment, since API keys commonly ride in
//     a query parameter. Only scheme, host and path are ever shown.
//   - When the changes do not fit in maxLines, the last returned line is
//     "+N more changes" (it counts toward maxLines) and more is N. When they
//     fit, more is 0. maxLines <= 0 returns no lines and more is the total
//     number of changes. Identical profiles return no lines and 0.
//   - nil: old == nil reports every set field of new as added
//     (`field: value (added)`); new == nil reports old's as removed; both
//     nil return nothing.
//   - A Resolved whose Canonical is missing or unreadable (one not made by
//     Resolve) is compared on its exported fields only: url, token_set and
//     base then read as unset.
//
// Adding or changing Diff never changes the canonical form or the digest,
// so it does not touch ProtocolVersion.
func Diff(old, new *Resolved, maxLines int) (lines []string, more int) {
	changes := diffChanges(old, new)
	total := len(changes)
	switch {
	case maxLines <= 0:
		return nil, total
	case total <= maxLines:
		if total == 0 {
			return nil, 0
		}
		return changes, 0
	}
	keep := maxLines - 1
	more = total - keep
	return append(changes[:keep:keep], fmt.Sprintf("+%d more changes", more)), more
}

// diffView is the comparable shape of one side.
type diffView struct {
	present  bool
	fields   canonical
	selected bool // context_servers is a selection (not "all kept")
}

func viewOf(r *Resolved) diffView {
	if r == nil {
		return diffView{}
	}
	var c canonical
	if len(r.Canonical) == 0 || json.Unmarshal(r.Canonical, &c) != nil {
		c = canonical{Name: r.Name, Provider: r.Provider, Backend: r.Backend, Model: r.Model, Env: r.Env}
		if r.Selection.Selective {
			c.ContextServers = r.Selection.Kept
		}
		c.ContextExcluded = r.Selection.Excluded
	}
	return diffView{present: true, fields: c, selected: c.ContextServers != nil}
}

func diffChanges(old, new *Resolved) []string {
	o, n := viewOf(old), viewOf(new)
	if !o.present && !n.present {
		return nil
	}
	var out []string
	add := func(line string) { out = append(out, fitLine(line)) }

	scalar := func(field, ov, nv string, shown func(string) string) {
		switch {
		case !o.present:
			if nv != "" {
				add(fmt.Sprintf("%s: %s (added)", field, shown(nv)))
			}
		case !n.present:
			if ov != "" {
				add(fmt.Sprintf("%s: %s (removed)", field, shown(ov)))
			}
		case ov != nv:
			add(fmt.Sprintf("%s: %s -> %s", field, shown(ov), shown(nv)))
		}
	}
	scalar("name", o.fields.Name, n.fields.Name, quote)
	scalar("provider", o.fields.Provider, n.fields.Provider, quote)
	scalar("backend", o.fields.Backend, n.fields.Backend, quote)
	scalar("model", o.fields.Model, n.fields.Model, quote)

	ou, nu := o.fields.URL, n.fields.URL
	if o.present && n.present && ou != nu && urlMayCarryCredential(ou) && urlMayCarryCredential(nu) {
		add("url: " + RedactedURL + " (changed)")
	} else {
		scalar("url", ou, nu, shownURL)
	}

	switch ot, nt := o.fields.TokenSet, n.fields.TokenSet; {
	case !o.present:
		if nt {
			add("token: set (added)")
		}
	case !n.present:
		if ot {
			add("token: set (removed)")
		}
	case ot != nt:
		add(fmt.Sprintf("token: %s -> %s", setWord(ot), setWord(nt)))
	}

	for _, k := range unionKeys(o.fields.Env, n.fields.Env) {
		ov, inOld := o.fields.Env[k]
		nv, inNew := n.fields.Env[k]
		switch {
		case !inOld:
			add(fmt.Sprintf("env %s: added", quote(k)))
		case !inNew:
			add(fmt.Sprintf("env %s: removed", quote(k)))
		case ov != nv:
			add(fmt.Sprintf("env %s: changed", quote(k)))
		}
	}

	if o.selected != n.selected {
		switch {
		case !o.present:
			add("context_servers: selection (added)")
		case !n.present:
			add("context_servers: selection (removed)")
		default:
			add(fmt.Sprintf("context_servers: %s -> %s", selectionWord(o.selected), selectionWord(n.selected)))
		}
	}
	for _, s := range listDiff(o.fields.ContextServers, n.fields.ContextServers) {
		add(fmt.Sprintf("context_servers %s: %s", quote(s.item), s.change))
	}

	for _, srv := range unionKeys(o.fields.ContextExcluded, n.fields.ContextExcluded) {
		for _, tool := range listDiff(o.fields.ContextExcluded[srv], n.fields.ContextExcluded[srv]) {
			add(fmt.Sprintf("context_excluded %s %s: %s", quote(srv), quote(tool.item), tool.change))
		}
	}

	scalar("base", o.fields.Base, n.fields.Base, quote)
	return out
}

type listChange struct{ item, change string }

// listDiff returns the items only in a (removed) or only in b (added),
// sorted.
func listDiff(a, b []string) []listChange {
	inA, inB := map[string]bool{}, map[string]bool{}
	for _, s := range a {
		inA[s] = true
	}
	for _, s := range b {
		inB[s] = true
	}
	var out []listChange
	for _, s := range unionKeys(inA, inB) {
		switch {
		case !inA[s]:
			out = append(out, listChange{s, "added"})
		case !inB[s]:
			out = append(out, listChange{s, "removed"})
		}
	}
	return out
}

func unionKeys[V any](a, b map[string]V) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return slices.Compact(keys)
}

func setWord(set bool) string {
	if set {
		return "set"
	}
	return "unset"
}

func selectionWord(selected bool) string {
	if selected {
		return "selection"
	}
	return "all kept"
}

// urlMayCarryCredential is the conservative redaction rule documented on
// Diff.
func urlMayCarryCredential(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return true
	}
	return u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != ""
}

func shownURL(raw string) string {
	if urlMayCarryCredential(raw) {
		return RedactedURL
	}
	return quote(raw)
}

// quote renders s printable: strconv.QuoteToGraphic escapes control and
// format characters, and any graphic-but-not-printable rune left (a
// non-ASCII space) becomes '?'.
func quote(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return '?'
	}, strconv.QuoteToGraphic(s))
}

// fitLine cuts a line to MaxDiffLineRunes runes, ending it in "…".
func fitLine(s string) string {
	if utf8.RuneCountInString(s) <= MaxDiffLineRunes {
		return s
	}
	r := []rune(s)
	return string(r[:MaxDiffLineRunes-1]) + "…"
}
