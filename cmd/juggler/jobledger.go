package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	jr "code.linenisgreat.com/clown/internal/jugglerrun"
)

const jobLedgerUsage = "usage: juggler job-ledger <job> [--target <channel principal>] [--ringmaster <path>]"

// jobLookupTimeout bounds the read-only job verbs.
const jobLookupTimeout = 30 * time.Second

// parseJobLookup parses `<job> [--target <principal>]` plus the binary flags.
func parseJobLookup(name, usage string, args []string, stderr io.Writer) (job, target string, bins platformBins, ok bool) {
	job, rest := leadingArg(args)
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&target, "target", "", "the channel holding the job, for a job juggler's state dir does not know")
	bins.register(fs, false)
	if err := fs.Parse(rest); err != nil {
		return "", "", bins, false
	}
	if job == "" && fs.NArg() == 1 {
		job = fs.Arg(0)
	} else if fs.NArg() != 0 {
		job = ""
	}
	if job == "" {
		fmt.Fprintf(stderr, "juggler: %s: exactly one <job> is required\n%s\n", name, usage)
		return "", "", bins, false
	}
	return job, target, bins, true
}

// cmdJobLedger is `juggler job-ledger <job>`: a job's ledger (its result
// spool, or a run job's run ledger) for a caller that did not --wait.
func cmdJobLedger(args []string, stdout, stderr io.Writer) int {
	job, target, bins, ok := parseJobLookup("job-ledger", jobLedgerUsage, args, stderr)
	if !ok {
		return jr.ExitUsage
	}
	deps, err := bins.lifecycleDeps(false)
	if err != nil {
		fmt.Fprintf(stderr, "juggler: job-ledger: %v\n", err)
		return jr.ExitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobLookupTimeout)
	defer cancel()
	data, err := jr.JobLedger(ctx, deps, job, target)
	if err != nil {
		fmt.Fprintf(stderr, "juggler: job-ledger: %v\n", err)
		return jr.ExitUsage
	}
	_, _ = stdout.Write(data)
	return 0
}
