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

const cleanUsage = `usage: juggler clean [--older-than <duration>] [--act]`

// cleanTimeout bounds the sweep: generous since it may touch many runs, each
// with its own troupe mint-revoke call (pending) or a handful of file
// removals (resolved).
const cleanTimeout = 5 * time.Minute

// cmdClean is `juggler clean` (clown#250), the retention sweep for a pending
// run's leaked root account, password file and pending record, and a
// resolved run's leftover staged/traffic files (FDR 0019 Limitations).
// Defaults to a dry-run listing of what would be reaped; --act performs it.
// Needs the minter credential exactly when resolve does, for the same
// reason (teardownRoot calls troupe mint-revoke) — checked upfront, like
// resolve, so a sweep never reaps some runs before discovering the
// credential is missing for the rest.
func cmdClean(args []string, stdout, stderr io.Writer) int {
	var (
		bins      platformBins
		olderThan time.Duration
		act       bool
	)
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.DurationVar(&olderThan, "older-than", jr.DefaultCleanOlderThan, "reap only a pending or resolved run at least this old (in-flight-retry guard)")
	fs.BoolVar(&act, "act", false, "perform the reap; the default is a dry-run listing of what would be done")
	bins.register(fs, false)
	if err := fs.Parse(args); err != nil {
		return jr.ExitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "juggler: clean: unexpected argument %q\n%s\n", fs.Arg(0), cleanUsage)
		return jr.ExitUsage
	}
	if act && os.Getenv(minterPasswordFileEnv) == "" {
		fmt.Fprintf(stderr, "juggler: clean: $%s is unset: reaping a pending run's root needs the minter credential (troupe mint-revoke's self-removal path); set it (and $%s)\n", minterPasswordFileEnv, minterUserEnv)
		return jr.ExitUsage
	}
	return withDeps(context.Background(), stderr, "clean", bins, cleanTimeout, true, func(ctx context.Context, deps jr.LifecycleDeps) int {
		out, err := jr.Clean(ctx, deps, jr.CleanRequest{OlderThan: olderThan, Act: act})
		if err != nil {
			return fail(stderr, "clean", err)
		}
		printJSON(stdout, out)
		return jr.ExitSucceeded
	})
}
