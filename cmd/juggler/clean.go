package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	jr "code.linenisgreat.com/clown/internal/jugglerrun"
)

const cleanUsage = `usage: juggler clean [--older-than <duration>] [--resolved [--resolved-older-than <duration>]] [--act]`

// cleanTimeout bounds the sweep: generous since it may touch many runs, each
// with its own troupe mint-revoke call (pending) or a handful of file
// removals and ringmaster Records calls (resolved).
const cleanTimeout = 5 * time.Minute

// cmdClean is `juggler clean` (clown#250), the retention sweep for a pending
// run's leaked root account and password file, and, with --resolved, a
// resolved run's leftover staged/traffic files (FDR 0019 Limitations).
// Defaults to a dry-run listing of what would be reaped; --act performs it.
// Needs the minter credential exactly when resolve does, for the same
// reason (teardownRoot calls troupe mint-revoke) — checked upfront, like
// resolve, so a sweep never reaps some runs before discovering the
// credential is missing for the rest. Exits non-zero under --act if any
// listed run could not be fully reaped (an error, or reaped:false) — a
// dry-run always exits 0, since nothing there was ever expected to be
// "reaped" yet.
func cmdClean(args []string, stdout, stderr io.Writer) int {
	var (
		bins                         platformBins
		olderThan, resolvedOlderThan time.Duration
		resolved, act                bool
	)
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.DurationVar(&olderThan, "older-than", jr.DefaultCleanOlderThan, "reap a pending run only once it has been pending at least this long (in-flight-retry guard)")
	fs.BoolVar(&resolved, "resolved", false, "also sweep resolved runs' leftover staged and traffic.jsonl files (a retention policy, not a retry guard; off by default)")
	fs.DurationVar(&resolvedOlderThan, "resolved-older-than", jr.DefaultResolvedOlderThan, "with --resolved, sweep a resolved run's files only once its resolution is at least this old")
	fs.BoolVar(&act, "act", false, "perform the reap; the default is a dry-run listing of what would be done")
	bins.register(fs, false)
	if err := fs.Parse(args); err != nil {
		return jr.ExitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "juggler: clean: unexpected argument %q\n%s\n", fs.Arg(0), cleanUsage)
		return jr.ExitUsage
	}
	if olderThan < 0 || resolvedOlderThan < 0 {
		fmt.Fprintln(stderr, "juggler: clean: --older-than and --resolved-older-than must not be negative\n"+cleanUsage)
		return jr.ExitUsage
	}
	if act && os.Getenv(minterPasswordFileEnv) == "" {
		fmt.Fprintf(stderr, "juggler: clean: $%s is unset: reaping a pending run's root needs the minter credential (troupe mint-revoke's self-removal path); set it (and $%s)\n", minterPasswordFileEnv, minterUserEnv)
		return jr.ExitUsage
	}
	return withDeps(context.Background(), stderr, "clean", bins, cleanTimeout, true, func(ctx context.Context, deps jr.LifecycleDeps) int {
		out, err := jr.Clean(ctx, deps, jr.CleanRequest{OlderThan: olderThan, Resolved: resolved, ResolvedOlderThan: resolvedOlderThan, Act: act})
		if err != nil {
			return fail(stderr, "clean", err)
		}
		printJSON(stdout, out)
		if act {
			for _, r := range out.Runs {
				if r.Error != "" || !r.Reaped {
					return jr.ExitFailed
				}
			}
		}
		return jr.ExitSucceeded
	})
}
