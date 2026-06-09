package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/amarbel-llc/clown/internal/jobwake"
)

// The ringmaster binary doubles as the operator-facing control plane for the
// clown job-wakeup channel (RFC-0009) and output spool (RFC-0010). These
// subcommands surface, at the terminal, the same job state an agent drives
// through the `clown job` MCP/CLI producer surface — list, inspect, follow, and
// cancel long-running background jobs out-of-band (clown#124).
//
// Cancellation is cooperative: jobs are NOT spawned by ringmaster and the
// journal carries no worker PID (a deliberate RFC-0010 §"alternatives" call —
// a PID is meaningless across the sessions/hosts a channel may span). `cancel`
// therefore writes the terminal `cancelled` record, which wakes the originating
// session's monitor and signals the owning producer to stop; it does not itself
// send a signal to an OS process.

// jobErrExit maps a jobwake error to the conventional CLI exit code: a malformed
// job id is a usage error (2); a missing journal or any other failure is 1.
func jobErrExit(err error) int {
	if errors.Is(err, jobwake.ErrInvalidJobID) {
		return 2
	}
	return 1
}

// ringmasterLs lists jobs in a channel (the current session by default, every
// channel on the host with --all). It is the operator's "what's running?" view.
func ringmasterLs(args []string) int {
	fs := flag.NewFlagSet("ringmaster ls", flag.ContinueOnError)
	target := fs.String("target", "", "session key to list (default: current session)")
	all := fs.Bool("all", false, "list jobs across every channel on this host")
	asJSON := fs.Bool("json", false, "emit the listing as a JSON array")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	now := time.Now().UTC()
	var (
		rows []jobwake.JobSummary
		err  error
	)
	if *all {
		rows, err = jobwake.ListAllJobs(0, now)
	} else {
		rows, err = jobwake.ListJobs(*target, 0, now)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ringmaster ls: %v\n", err)
		return 1
	}

	if *asJSON {
		if rows == nil {
			rows = []jobwake.JobSummary{}
		}
		b, err := json.Marshal(rows)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ringmaster ls: %v\n", err)
			return 1
		}
		fmt.Println(string(b))
		return 0
	}

	if len(rows) == 0 {
		fmt.Println("no jobs")
		return 0
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if *all {
		fmt.Fprintln(tw, "CHANNEL\tJOB\tSOURCE\tSTATE\tELAPSED\tLAST ACTIVITY")
	} else {
		fmt.Fprintln(tw, "JOB\tSOURCE\tSTATE\tELAPSED\tLAST ACTIVITY")
	}
	for _, r := range rows {
		elapsed := time.Duration(r.Status.ElapsedSec) * time.Second
		last := r.Status.LastActivity
		if last == "" {
			last = "-"
		}
		if *all {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				shortChannel(r.Channel), r.JobID, r.Status.Source, r.Status.State, elapsed, last)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				r.JobID, r.Status.Source, r.Status.State, elapsed, last)
		}
	}
	_ = tw.Flush()
	return 0
}

// shortChannel renders a channel id as its first 8 hex digits for the --all
// listing, where the full 32-digit hash is more noise than signal.
func shortChannel(cid string) string {
	if len(cid) > 8 {
		return cid[:8]
	}
	return cid
}

// ringmasterStatus prints one job's full journal+spool-derived status, mirroring
// `clown job status` so operators and agents read the same view (RFC-0010 §3).
func ringmasterStatus(args []string) int {
	jobID, rest, ok := jobwake.LeadingArg(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "ringmaster status: missing <job-id>")
		return 2
	}
	fs := flag.NewFlagSet("ringmaster status", flag.ContinueOnError)
	target := fs.String("target", "", "session key (default: current session)")
	tail := fs.Int("tail", 20, "number of trailing spool lines to show")
	asJSON := fs.Bool("json", false, "emit the status as a single JSON object")
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	st, err := jobwake.StatusOf(*target, jobID, *tail, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "ringmaster status: %v\n", err)
		return jobErrExit(err)
	}
	if *asJSON {
		b, err := json.Marshal(st)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ringmaster status: %v\n", err)
			return 1
		}
		fmt.Println(string(b))
		return 0
	}
	fmt.Println(st.Header(jobID))
	if len(st.Tail) > 0 {
		fmt.Println("---")
		for _, line := range st.Tail {
			fmt.Println(line)
		}
	}
	return 0
}

// ringmasterTail prints a job's spooled output, optionally following it (-f)
// until the job reaches a terminal state or the operator interrupts. Following
// polls the spool because the producer is a separate process that may not exist
// yet; there is no inotify dependency.
func ringmasterTail(args []string) int {
	jobID, rest, ok := jobwake.LeadingArg(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "ringmaster tail: missing <job-id>")
		return 2
	}
	fs := flag.NewFlagSet("ringmaster tail", flag.ContinueOnError)
	target := fs.String("target", "", "session key (default: current session)")
	follow := fs.Bool("f", false, "follow: stream new output until the job ends")
	n := fs.Int("n", 20, "trailing lines to print before following")
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	path, err := jobwake.ResolveSpool(*target, jobID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ringmaster tail: %v\n", err)
		return jobErrExit(err)
	}

	lines, offset := jobwake.SpoolTail(path, *n)
	for _, l := range lines {
		fmt.Println(l)
	}
	if !*follow {
		return 0
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
			offset = jobwake.StreamSpool(os.Stdout, path, offset)
			st, err := jobwake.StatusOf(*target, jobID, 0, time.Now().UTC())
			if err == nil && jobwake.IsTerminal(st.State) {
				jobwake.StreamSpool(os.Stdout, path, offset) // drain a final write that raced the terminal record
				return 0
			}
		}
	}
}

// ringmasterCancel writes the terminal `cancelled` record for a job (RFC-0009
// §5, §8). This is a cooperative cancel: it wakes the originating session's
// monitor and signals the owning producer to stop — it does not send an OS
// signal (jobs are not ringmaster-spawned; the journal carries no PID). A
// missing job exits 1; an already-terminal job exits 1 with its final state.
func ringmasterCancel(args []string) int {
	jobID, rest, ok := jobwake.LeadingArg(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "ringmaster cancel: missing <job-id>")
		return 2
	}
	fs := flag.NewFlagSet("ringmaster cancel", flag.ContinueOnError)
	target := fs.String("target", "", "session key (default: current session)")
	message := fs.String("message", "cancelled by operator via ringmaster", "human-readable cancel reason")
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	// Pre-check so the operator gets a clear "no such job" / "already <state>"
	// instead of silently materializing a one-record journal for a typo'd id.
	st, err := jobwake.StatusOf(*target, jobID, 0, time.Now().UTC())
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "ringmaster cancel: no such job %q\n", jobID)
			return 1
		}
		fmt.Fprintf(os.Stderr, "ringmaster cancel: %v\n", err)
		return jobErrExit(err)
	}
	if jobwake.IsTerminal(st.State) {
		fmt.Fprintf(os.Stderr, "ringmaster cancel: job %q already %s\n", jobID, st.State)
		return 1
	}

	if err := jobwake.Done(*target, jobID, jobwake.TypeCancelled, *message, ""); err != nil {
		fmt.Fprintf(os.Stderr, "ringmaster cancel: %v\n", err)
		return 1
	}
	fmt.Printf("cancelled %s\n", jobID)
	return 0
}
