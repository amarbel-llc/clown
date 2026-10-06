package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	jr "code.linenisgreat.com/clown/internal/jugglerrun"
)

const exitWakeUsage = "usage: juggler exit-wake --job <id> --target <principal> [--run-ledger <path>] [--from <principal>] [--ringmaster <path>] [--troupe <path>]  ($SERVICE_RESULT, $EXIT_CODE, $EXIT_STATUS from systemd)"

// exitWakeTimeout bounds the whole hook; systemd's own stop timeout is the
// outer bound.
const exitWakeTimeout = 60 * time.Second

// cmdExitWake is `juggler exit-wake`, the agent unit's ExecStopPost and the
// sole exit-wake emitter (FDR 0019 §6). getenv supplies $SERVICE_RESULT,
// $EXIT_CODE and $EXIT_STATUS, so the hook is testable without systemd.
func cmdExitWake(getenv func(string) string, args []string, stdout, stderr io.Writer) int {
	var (
		bins                         platformBins
		job, target, ledgerRef, from string
	)
	fs := flag.NewFlagSet("exit-wake", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&job, "job", "", "the agent's ringmaster job")
	fs.StringVar(&target, "target", "", "the channel holding the job: the agent's parent principal")
	fs.StringVar(&ledgerRef, "run-ledger", "", "result_ref to attach when the hook writes the terminal record itself")
	fs.StringVar(&from, "from", "", "the exiting principal (default $CLOWN_SESSION_ID)")
	bins.register(fs, false)
	if err := fs.Parse(args); err != nil {
		return jr.ExitUsage
	}
	if job == "" || target == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "juggler: exit-wake: --job and --target are required\n"+exitWakeUsage)
		return jr.ExitUsage
	}
	if from == "" {
		from = getenv(jr.SessionIDEnv)
	}
	return withDeps(context.Background(), stderr, "exit-wake", bins, exitWakeTimeout, func(ctx context.Context, deps jr.LifecycleDeps) int {
		out, err := jr.ExitWake(ctx, deps, jr.ExitWakeRequest{
			Job:       job,
			Target:    target,
			From:      from,
			LedgerRef: ledgerRef,
			Service:   jr.ServiceResultFromEnv(getenv),
		})
		printJSON(stdout, out)
		if err != nil {
			return fail(stderr, "exit-wake", err)
		}
		return jr.ExitSucceeded
	})
}
