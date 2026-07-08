package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"code.linenisgreat.com/ringmaster/jobwake"
)

// runList dispatches `clown list`: a flat table of every live clown session,
// in the shape of `spinclass list`/`sc list` (one row per session, columns
// including a spinclass session key when present) rather than `clown
// presence list`'s grouped-by-decoration view — the two commands answer
// different questions ("what is running, spinclass-shaped" vs. "what's in
// THIS group"), so `list` is a distinct rendering over the SAME presence
// data (jobwake.ListPresence), not an alias or a duplicate query path.
// Works identically inside and outside a spinclass session: a session with
// no decoration (bare clown, no group-id resolved) still gets a row, with
// an empty SPINCLASS column — see clown#169.
func runList(args []string) int {
	fs := flag.NewFlagSet("clown list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit one JSON object per presence record")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ps, err := jobwake.ListPresence(time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown list: %v\n", err)
		return 1
	}

	// Stable order: by spinclass session (decoration) first, then by name/key
	// within it — mirrors clown presence list's grouping order, so the two
	// commands agree on "what comes first" even though list's rows are flat.
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Decoration != ps[j].Decoration {
			return ps[i].Decoration < ps[j].Decoration
		}
		return listSortKey(ps[i]) < listSortKey(ps[j])
	})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, p := range ps {
			if err := enc.Encode(p); err != nil {
				fmt.Fprintf(os.Stderr, "clown list: %v\n", err)
				return 1
			}
		}
		return 0
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSESSION\tSPINCLASS\tDESCRIPTION")
	for _, p := range ps {
		name := p.ClownName
		if name == "" {
			name = "-"
		}
		spinclass := p.Decoration
		if spinclass == "" {
			spinclass = "-"
		}
		desc := p.Description
		if desc == "" {
			desc = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, p.SessionKey, spinclass, desc)
	}
	_ = w.Flush()
	return 0
}

// listSortKey orders rows within one spinclass group (or the ungrouped
// bucket): by clown-name when present (human-ergonomic, clown#169), falling
// back to the raw session key so pre-allocator/older-ringmaster records
// still sort deterministically.
func listSortKey(p jobwake.Presence) string {
	if p.ClownName != "" {
		return p.ClownName
	}
	return p.SessionKey
}
