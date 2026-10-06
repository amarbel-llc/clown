package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	jr "code.linenisgreat.com/clown/internal/jugglerrun"
)

const resolveUsage = `usage: juggler resolve <run-job> --state succeeded|failed --reason <text> [--fallback-artifacts '[{"tool","kind","uris"}]'] [--stop-grace <dur>] [--result-line <text> --canary-room <jid>] [--keep-accounts]`

// resolveTimeout bounds the run's last call.
const resolveTimeout = 60 * time.Second

// cmdResolve is `juggler resolve`, the run's last call (FDR 0019 §1).
// Resolving an already-resolved run is a no-op that reports the stored
// verdict, runs only a teardown still pending, and exits 0. Still-running
// subagents are cancelled first; the run's accounts are torn down last.
func cmdResolve(args []string, stdout, stderr io.Writer) int {
	var (
		bins                 platformBins
		state, reason, fbArt string
		resultLine, canary   string
		keepAccounts         bool
		stopGrace            time.Duration
	)
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&state, "state", "", "succeeded, or failed when the fallback ran")
	fs.StringVar(&reason, "reason", "", "why the run ended")
	fs.StringVar(&fbArt, "fallback-artifacts", "", "the fallback's outcome as a JSON array of {tool, kind, uris}")
	fs.DurationVar(&stopGrace, "stop-grace", jr.DefaultStopGrace, "how long to wait for each still-running subagent to terminalize after it is asked to cancel")
	fs.StringVar(&resultLine, "result-line", "", "the run's one-line result, posted into --canary-room as the run root before anything else")
	fs.StringVar(&canary, "canary-room", "", "the MUC JID --result-line is posted to")
	fs.BoolVar(&keepAccounts, "keep-accounts", false, "skip the account teardown: keep every account and password file of the run (debugging)")
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
	if (resultLine == "") != (canary == "") {
		fmt.Fprintln(stderr, "juggler: resolve: --result-line and --canary-room go together\n"+resolveUsage)
		return jr.ExitUsage
	}
	if strings.ContainsAny(resultLine, "\r\n") {
		fmt.Fprintln(stderr, "juggler: resolve: --result-line must be one line")
		return jr.ExitUsage
	}
	var artifacts []jr.Artifact
	if fbArt != "" {
		if err := json.Unmarshal([]byte(fbArt), &artifacts); err != nil {
			return fail(stderr, "resolve", fmt.Errorf("--fallback-artifacts: %w", err))
		}
	}
	// The stop grace is spent inside the budget, so it extends it.
	budget := resolveTimeout
	if stopGrace > 0 {
		budget += stopGrace
	}
	return withDeps(context.Background(), stderr, "resolve", bins, budget, func(ctx context.Context, deps jr.LifecycleDeps) int {
		out, err := jr.Resolve(ctx, deps, jr.ResolveRequest{
			RunJob: job, State: state, Reason: reason, FallbackArtifacts: artifacts, StopGrace: stopGrace,
			ResultLine: resultLine, CanaryRoom: canary, KeepAccounts: keepAccounts,
		})
		if err != nil {
			return fail(stderr, "resolve", err)
		}
		if out.AlreadyResolved {
			fmt.Fprintf(stderr, "juggler: resolve: run %s was already resolved %s; only a pending teardown was done\n", out.RunKey, out.State)
		}
		printJSON(stdout, out)
		return jr.ExitSucceeded
	})
}
