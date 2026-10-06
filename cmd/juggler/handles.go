package main

import (
	"context"
	"fmt"
	"io"

	jr "code.linenisgreat.com/clown/internal/jugglerrun"
)

const handlesUsage = "usage: juggler handles <job> [--target <channel principal>] [--ringmaster <path>]"

// cmdHandles is `juggler handles <job>`: the job's handle table, read-only
// (FDR 0019 §11's record shape). v1 has no grant, accept or release verb.
func cmdHandles(args []string, stdout, stderr io.Writer) int {
	job, target, bins, ok := parseJobLookup("handles", handlesUsage, args, stderr)
	if !ok {
		return jr.ExitUsage
	}
	deps, err := bins.lifecycleDeps(false)
	if err != nil {
		fmt.Fprintf(stderr, "juggler: handles: %v\n", err)
		return jr.ExitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobLookupTimeout)
	defer cancel()
	holders, err := jr.JobHandles(ctx, deps, job, target)
	if err != nil {
		fmt.Fprintf(stderr, "juggler: handles: %v\n", err)
		return jr.ExitUsage
	}
	if holders == nil {
		holders = []jr.Holder{}
	}
	printJSON(stdout, holders)
	return 0
}
