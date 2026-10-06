package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	jr "code.linenisgreat.com/clown/internal/jugglerrun"
)

const resolveUsage = `usage: juggler resolve <run-job> --state succeeded|failed --reason <text> [--fallback-artifacts '[{"tool","kind","uris"}]'] [--stop-grace <dur>]`

// resolveTimeout bounds the run's last call.
const resolveTimeout = 60 * time.Second

// cmdResolve is `juggler resolve`, the run's last call (FDR 0019 §1).
// Resolving an already-resolved run is a no-op that reports the stored
// verdict and exits 0. Still-running subagents are cancelled first.
func cmdResolve(args []string, stdout, stderr io.Writer) int {
	var (
		bins                 platformBins
		state, reason, fbArt string
		stopGrace            time.Duration
	)
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&state, "state", "", "succeeded, or failed when the fallback ran")
	fs.StringVar(&reason, "reason", "", "why the run ended")
	fs.StringVar(&fbArt, "fallback-artifacts", "", "the fallback's outcome as a JSON array of {tool, kind, uris}")
	fs.DurationVar(&stopGrace, "stop-grace", jr.DefaultStopGrace, "how long to wait for each still-running subagent to terminalize after it is asked to cancel")
	bins.register(fs, false)
	job, err := leadingJob(fs, args)
	if errors.Is(err, errExtraPositional) {
		fmt.Fprintf(stderr, "juggler: resolve: %v\n%s\n", err, resolveUsage)
		return jr.ExitUsage
	} else if err != nil {
		return jr.ExitUsage
	}
	if job == "" || state == "" || reason == "" {
		fmt.Fprintln(stderr, "juggler: resolve: <run-job>, --state and --reason are required\n"+resolveUsage)
		return jr.ExitUsage
	}
	var artifacts []jr.Artifact
	if fbArt != "" {
		if err := json.Unmarshal([]byte(fbArt), &artifacts); err != nil {
			return fail(stderr, "resolve", fmt.Errorf("--fallback-artifacts: %w", err))
		}
	}
	return withDeps(context.Background(), stderr, "resolve", bins, resolveTimeout, func(ctx context.Context, deps jr.LifecycleDeps) int {
		out, err := jr.Resolve(ctx, deps, jr.ResolveRequest{RunJob: job, State: state, Reason: reason, FallbackArtifacts: artifacts, StopGrace: stopGrace})
		if err != nil {
			return fail(stderr, "resolve", err)
		}
		if out.AlreadyResolved {
			fmt.Fprintf(stderr, "juggler: resolve: run %s was already resolved %s; nothing written\n", out.RunKey, out.State)
		}
		printJSON(stdout, out)
		return jr.ExitSucceeded
	})
}
