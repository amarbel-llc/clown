package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/amarbel-llc/ringmaster/jobwake"
)

// runPresence dispatches the `clown presence` subcommands. The presence index
// (github.com/amarbel-llc/ringmaster/jobwake presence.go) is the clown→spinclass
// liveness seam (RFC-0014): each live clown writes a record under
// $XDG_STATE_HOME/ringmaster/presence/, its job-watch monitor refreshes the
// lastSeen on a ticker, and a clean shutdown
// removes the file (ListPresence also prunes records past the freshness window).
// This CLI is the stable query surface a consumer — notably spinclass's
// liveness probe (spinclass#201) — uses instead of reading the on-disk JSON
// directly, so the schema and the freshness window stay clown's concern. It is a
// pure read, available regardless of CLOWN_DISABLE_JOB_WAKEUP (when disabled no
// records are written, so the listing is simply empty).
func runPresence(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "clown presence: expected a subcommand (list)")
		return 2
	}
	switch args[0] {
	case "list":
		return presenceList(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "clown presence: unknown subcommand %q\n", args[0])
		return 2
	}
}

// presenceList prints the live presence records. --group filters to records
// whose decoration (the group-id, CLOWN_GROUP_ID — e.g. a spinclass session's
// repo/branch) equals the given value; passing --group with an empty value
// selects the ungrouped records. --quiet suppresses output and turns the command
// into a liveness predicate: exit 0 if at least one matching live record exists,
// exit 1 if none (grep -q style), so a probe is
// `clown presence list --group "$ID" --quiet`. --json emits one JSON record per
// line (the jobwake.Presence schema). The default is a human listing grouped by
// decoration, mirroring `troupe list`.
func presenceList(args []string) int {
	fs := flag.NewFlagSet("clown presence list", flag.ContinueOnError)
	group := fs.String("group", "", "only records whose decoration (group-id) equals this value")
	asJSON := fs.Bool("json", false, "emit one JSON object per presence record")
	quiet := fs.Bool("quiet", false, "print nothing; exit 0 if any matching record is live, else 1")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// Distinguish "--group not given" (list every group) from "--group ''"
	// (select the ungrouped records); flag's zero value alone cannot.
	groupSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "group" {
			groupSet = true
		}
	})

	ps, err := jobwake.ListPresence(time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown presence list: %v\n", err)
		return 1
	}
	if groupSet {
		filtered := ps[:0:0]
		for _, p := range ps {
			if p.Decoration == *group {
				filtered = append(filtered, p)
			}
		}
		ps = filtered
	}

	if *quiet {
		if len(ps) > 0 {
			return 0
		}
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, p := range ps {
			if err := enc.Encode(p); err != nil {
				fmt.Fprintf(os.Stderr, "clown presence list: %v\n", err)
				return 1
			}
		}
		return 0
	}

	groups := map[string][]jobwake.Presence{}
	var order []string
	for _, p := range ps {
		if _, ok := groups[p.Decoration]; !ok {
			order = append(order, p.Decoration)
		}
		groups[p.Decoration] = append(groups[p.Decoration], p)
	}
	sort.Strings(order)
	for _, g := range order {
		label := g
		if label == "" {
			label = "(no session)"
		}
		fmt.Println(label)
		for _, p := range groups[g] {
			desc := p.Description
			if desc == "" {
				desc = "-"
			}
			fmt.Printf("  %s  %s\n", p.SessionKey, desc)
		}
	}
	return 0
}
