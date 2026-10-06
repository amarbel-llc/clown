package main

import (
	"context"
	"errors"
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
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&target, "target", "", "the channel holding the job, for a job juggler's state dir does not know")
	bins.register(fs, false)
	job, err := leadingJob(fs, args)
	if err != nil && !errors.Is(err, errExtraPositional) {
		return "", "", bins, false
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
	return withDeps(context.Background(), stderr, "job-ledger", bins, jobLookupTimeout, func(ctx context.Context, deps jr.LifecycleDeps) int {
		data, err := jr.JobLedger(ctx, deps, job, target)
		if err != nil {
			return fail(stderr, "job-ledger", err)
		}
		_, _ = stdout.Write(data)
		return jr.ExitSucceeded
	})
}
