package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	jr "code.linenisgreat.com/clown/internal/jugglerrun"
)

const spawnUsage = `usage: juggler spawn --new-run [--run-key <key>] --input <file|-> [--issuer <principal>] (--room <jid> | --room-domain <domain>)
       juggler spawn --brief <template|-> [--task <file|->] [--run-key <key>] [--wait [--timeout <dur>]] [--moxy-url <url>] [--juggler <path>] [--stop-grace <dur>] [--user]
  common: [--ringmaster <path>] [--troupe <path>] [--systemd-run <path>]`

// spawnTimeout bounds the launch (mints, posts, systemd-run), not --wait.
const spawnTimeout = 2 * time.Minute

type spawnOpts struct {
	bins       platformBins
	newRun     bool
	runKey     string
	input      string
	issuer     string
	room       string
	roomDomain string
	brief      string
	task       string
	wait       bool
	timeout    time.Duration
	moxyURL    string
	juggler    string
	stopGrace  time.Duration
}

func parseSpawnFlags(args []string, stderr io.Writer) (spawnOpts, error) {
	var o spawnOpts
	fs := flag.NewFlagSet("spawn", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.newRun, "new-run", false, "mint a new run root (the issuer's entry for a new tree)")
	fs.StringVar(&o.runKey, "run-key", "", "run idempotency key (--new-run: minted when empty; --brief: the run, else found by brief.room)")
	fs.StringVar(&o.input, "input", "", "--new-run: the run's input (the recording), file or -")
	fs.StringVar(&o.issuer, "issuer", "", "--new-run: the issuer principal (default $CLOWN_SESSION_ID)")
	fs.StringVar(&o.room, "room", "", "--new-run: an existing, configured run MUC JID")
	fs.StringVar(&o.roomDomain, "room-domain", "", "--new-run: create the run MUC on this component (troupe's lane; unavailable)")
	fs.StringVar(&o.brief, "brief", "", "the subagent brief template (TOML), file or -")
	fs.StringVar(&o.task, "task", "", "the brief's task text, file or - (default: the template's own task)")
	fs.BoolVar(&o.wait, "wait", false, "block until the subagent's job terminalizes and print its verdict")
	fs.DurationVar(&o.timeout, "timeout", 0, "--wait bound (default: the brief's wall clock plus twice the stop grace)")
	fs.StringVar(&o.moxyURL, "moxy-url", "", "the agent's moxy upstream (default $"+jr.MoxyURLEnv+")")
	fs.StringVar(&o.juggler, "juggler", "", "juggler binary the unit runs (default: this executable)")
	fs.DurationVar(&o.stopGrace, "stop-grace", jr.DefaultStopGrace, "added to the wall clock for RuntimeMaxSec")
	fs.BoolVar(&o.bins.userManager, "user", false, "start the unit under the user service manager (systemd-run --user)")
	o.bins.register(fs, true)
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	switch {
	case fs.NArg() != 0:
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case o.newRun && o.brief != "":
		return o, errors.New("--new-run and --brief are exclusive")
	case !o.newRun && o.brief == "":
		return o, errors.New("one of --new-run or --brief is required")
	case o.newRun && o.input == "":
		return o, errors.New("--new-run requires --input")
	case o.newRun && o.task != "":
		return o, errors.New("--task applies to --brief only")
	case o.brief == "-" && o.task == "-":
		return o, errors.New("--brief and --task cannot both read stdin")
	case o.newRun && o.wait:
		return o, errors.New("--wait applies to --brief only")
	case !o.wait && o.timeout != 0:
		return o, errors.New("--timeout requires --wait")
	}
	return o, nil
}

// cmdSpawn is `juggler spawn` (FDR 0019 §1's launcher glue and glue-facing
// contract). Exit codes: --new-run and plain --brief 0 or 1; --brief --wait
// mirrors the job state (0/2/3/4), 5 when the job is still running at the
// timeout, 1 on a usage or launch failure.
func cmdSpawn(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	o, err := parseSpawnFlags(args, stderr)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(stderr, "juggler: spawn: %v\n%s\n", err, spawnUsage)
		}
		return jr.ExitUsage
	}
	return withDeps(context.Background(), stderr, "spawn", o.bins, spawnTimeout, func(launchCtx context.Context, deps jr.LifecycleDeps) int {
		if o.newRun {
			return spawnNewRun(launchCtx, deps, o, stdin, stdout, stderr)
		}
		return spawnBrief(launchCtx, deps, o, stdin, stdout, stderr)
	})
}

func spawnNewRun(ctx context.Context, deps jr.LifecycleDeps, o spawnOpts, stdin io.Reader, stdout, stderr io.Writer) int {
	input, err := readInput(o.input, stdin)
	if err != nil {
		return fail(stderr, "spawn", fmt.Errorf("reading input: %w", err))
	}
	issuer := o.issuer
	if issuer == "" {
		issuer = os.Getenv(jr.SessionIDEnv)
	}
	res, err := jr.NewRun(ctx, deps, jr.NewRunRequest{RunKey: o.runKey, Issuer: issuer, Input: input, Room: o.room, RoomDomain: o.roomDomain})
	if err != nil {
		return fail(stderr, "spawn", err)
	}
	printJSON(stdout, res)
	return jr.ExitSucceeded
}

func spawnBrief(ctx context.Context, deps jr.LifecycleDeps, o spawnOpts, stdin io.Reader, stdout, stderr io.Writer) int {
	brief, err := readInput(o.brief, stdin)
	if err != nil {
		return fail(stderr, "spawn", fmt.Errorf("reading brief: %w", err))
	}
	var task []byte
	if o.task != "" {
		if task, err = readInput(o.task, stdin); err != nil {
			return fail(stderr, "spawn", fmt.Errorf("reading task: %w", err))
		}
	}
	jugglerBin := o.juggler
	if jugglerBin == "" {
		if jugglerBin, err = os.Executable(); err != nil {
			return fail(stderr, "spawn", fmt.Errorf("resolving the juggler binary: %w", err))
		}
	}
	moxyURL := o.moxyURL
	if moxyURL == "" {
		moxyURL = os.Getenv(jr.MoxyURLEnv)
	}
	rec, _, err := jr.SpawnChild(ctx, deps, jr.SpawnRequest{
		Brief:        brief,
		Task:         task,
		RunKey:       o.runKey,
		EnvPrincipal: os.Getenv(jr.SessionIDEnv),
		MoxyURL:      moxyURL,
		JugglerBin:   jugglerBin,
		StopGrace:    o.stopGrace,
		UnitEnv:      jr.PassthroughUnitEnv(os.Environ()),
	})
	if err != nil {
		return fail(stderr, "spawn", err)
	}
	if !o.wait {
		printJSON(stdout, rec.Launch())
		return jr.ExitSucceeded
	}
	timeout := o.timeout
	if timeout <= 0 {
		timeout = jr.DefaultWaitTimeout(rec, o.stopGrace)
	}
	// The wait outlives the launch bound (spawnTimeout).
	w, err := jr.WaitChild(context.Background(), deps, rec, timeout)
	if err != nil {
		return fail(stderr, "spawn", fmt.Errorf("waiting on %s: %w", rec.Job, err))
	}
	printJSON(stdout, w)
	return w.ExitCode()
}
