package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/amarbel-llc/clown/internal/jobwake"
)

// The ringmaster binary is the job-control platform surface (RFC-0015). These
// producer/agent verbs — start/progress/done/read/spool-path/wait/whoami — were
// promoted verbatim from the former `clown job` subcommands; they emit and read
// the RFC-0009 wake channel and RFC-0010 spool. The operator verbs (ls, status,
// tail, cancel) live in jobs.go; the `status` verb is shared between the two
// surfaces (jobs.go's ringmasterStatus).

// jobWakeupDisabled reports whether the job-wakeup facility is switched off via
// CLOWN_DISABLE_JOB_WAKEUP=1 (RFC-0009 §8). When set, the emit verbs are no-ops
// that still exit 0 so producers need no conditional logic.
func jobWakeupDisabled() bool {
	return os.Getenv("CLOWN_DISABLE_JOB_WAKEUP") == "1"
}

// ringmasterWhoami prints the resolved per-instance session key, its derived
// channel id, the precedence source that supplied the key, and — when grouped —
// the group decoration (group-id, RFC-0014 §2) and its derived group channel
// (RFC-0012 §1, RFC-0013 §2.4). Pure read: unaffected by CLOWN_DISABLE_JOB_WAKEUP.
func ringmasterWhoami(args []string) int {
	fs := flag.NewFlagSet("ringmaster whoami", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit the identity as a single JSON object")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	key, source := jobwake.ResolveSessionKey()
	cid := jobwake.ChannelID(key)
	decoration := jobwake.GroupKey()
	groupChannel := jobwake.GroupChannel()
	if *asJSON {
		b, err := json.Marshal(struct {
			SessionKey     string `json:"sessionKey"`
			ChannelID      string `json:"channelId"`
			Source         string `json:"source"`
			Decoration     string `json:"decoration,omitempty"`
			GroupChannelID string `json:"groupChannelId,omitempty"`
		}{SessionKey: key, ChannelID: cid, Source: source, Decoration: decoration, GroupChannelID: groupChannel})
		if err != nil {
			fmt.Fprintf(os.Stderr, "ringmaster whoami: %v\n", err)
			return 1
		}
		fmt.Println(string(b))
		return 0
	}
	fmt.Printf("sessionKey:     %s\nchannelId:      %s\nsource:         %s\ndecoration:     %s\ngroupChannelId: %s\n", key, cid, source, decoration, groupChannel)
	return 0
}

func ringmasterStart(args []string) int {
	fs := flag.NewFlagSet("ringmaster start", flag.ContinueOnError)
	target := fs.String("target", "", "target session key (default: resolved session)")
	label := fs.String("label", "", "job label, seeds the generated id")
	source := fs.String("source", "", "emitting plugin label")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	id, err := jobwake.Start(jobwake.StartOpts{Target: *target, Label: *label, Source: *source})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ringmaster start: %v\n", err)
		return 1
	}
	fmt.Println(id)
	return 0
}

func ringmasterProgress(args []string) int {
	jobID, rest, ok := jobwake.LeadingArg(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "ringmaster progress: missing <job-id>")
		return 2
	}
	fs := flag.NewFlagSet("ringmaster progress", flag.ContinueOnError)
	target := fs.String("target", "", "target session key (default: resolved session)")
	message := fs.String("message", "", "human-readable progress detail")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if err := jobwake.Progress(*target, jobID, *message); err != nil {
		fmt.Fprintf(os.Stderr, "ringmaster progress: %v\n", err)
		return 1
	}
	return 0
}

func ringmasterDone(args []string) int {
	jobID, rest, ok := jobwake.LeadingArg(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "ringmaster done: missing <job-id>")
		return 2
	}
	fs := flag.NewFlagSet("ringmaster done", flag.ContinueOnError)
	target := fs.String("target", "", "target session key (default: resolved session)")
	state := fs.String("state", "", "succeeded|failed|cancelled|interrupted")
	message := fs.String("message", "", "human-readable detail")
	resultRef := fs.String("result-ref", "", "opaque result pointer")
	var resources stringList
	fs.Var(&resources, "resource", "attach a resource by URI, e.g. madder://blobs/<digest> (repeatable)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *state == "" {
		fmt.Fprintln(os.Stderr, "ringmaster done: --state is required")
		return 2
	}
	if err := jobwake.Done(*target, jobID, *state, *message, *resultRef, jobwake.ResourcesFromURIs(resources)...); err != nil {
		fmt.Fprintf(os.Stderr, "ringmaster done: %v\n", err)
		return 1
	}
	return 0
}

// ringmasterSpoolPath resolves and prints the absolute output-spool path for a
// job (RFC-0010 §2). It creates the channel directory but NOT the spool file
// (that is the producer's append). An invalid job id is a usage error (exit 2).
func ringmasterSpoolPath(args []string) int {
	jobID, rest, ok := jobwake.LeadingArg(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "ringmaster spool-path: missing <job-id>")
		return 2
	}
	fs := flag.NewFlagSet("ringmaster spool-path", flag.ContinueOnError)
	target := fs.String("target", "", "target session key (default: resolved session)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	path, err := jobwake.SpoolPath(*target, jobID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ringmaster spool-path: %v\n", err)
		if errors.Is(err, jobwake.ErrInvalidJobID) {
			return 2
		}
		return 1
	}
	fmt.Println(path)
	return 0
}

// ringmasterWait blocks until the job reaches a terminal state, then prints its
// status exactly as `ringmaster status` does — the synchronous join surface
// (clown#154). --timeout bounds the wait (0 = block until terminal or
// SIGINT/SIGTERM). A read-only journal pull, available regardless of
// CLOWN_DISABLE_JOB_WAKEUP. An invalid job id exits 2; an unknown job or a
// timeout exits 1; a signal is a clean exit 0.
func ringmasterWait(args []string) int {
	jobID, rest, ok := jobwake.LeadingArg(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "ringmaster wait: missing <job-id>")
		return 2
	}
	fs := flag.NewFlagSet("ringmaster wait", flag.ContinueOnError)
	target := fs.String("target", "", "target session key (default: resolved session)")
	timeout := fs.Duration("timeout", 0, "max time to block (0 = until terminal or signal)")
	tail := fs.Int("tail", 20, "number of trailing spool lines to show on completion")
	asJSON := fs.Bool("json", false, "emit the terminal status as a single JSON object")
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *timeout > 0 {
		var tcancel context.CancelFunc
		ctx, tcancel = context.WithTimeout(ctx, *timeout)
		defer tcancel()
	}

	if _, err := jobwake.WaitDone(ctx, *target, jobID); err != nil {
		switch {
		case errors.Is(err, jobwake.ErrInvalidJobID):
			fmt.Fprintf(os.Stderr, "ringmaster wait: %v\n", err)
			return 2
		case errors.Is(err, jobwake.ErrJobNotFound):
			fmt.Fprintf(os.Stderr, "ringmaster wait: %v\n", err)
			return 1
		case errors.Is(err, context.DeadlineExceeded):
			fmt.Fprintf(os.Stderr, "ringmaster wait: timed out after %s; job still running\n", *timeout)
			return 1
		case errors.Is(err, context.Canceled):
			return 0 // SIGINT/SIGTERM is a clean shutdown
		default:
			fmt.Fprintf(os.Stderr, "ringmaster wait: %v\n", err)
			return 1
		}
	}

	// Terminal reached: render the same payload as `ringmaster status` (#154).
	st, err := jobwake.StatusOf(*target, jobID, *tail, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "ringmaster wait: %v\n", err)
		return 1
	}
	if *asJSON {
		b, err := json.Marshal(st)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ringmaster wait: %v\n", err)
			return 1
		}
		fmt.Println(string(b))
		return 0
	}
	return printStatusHuman(jobID, st)
}

// printStatusHuman renders the one-line status header followed by the spool tail
// under a separator (RFC-0010 §3).
func printStatusHuman(jobID string, st jobwake.Status) int {
	fmt.Println(st.Header(jobID))
	if len(st.Tail) > 0 {
		fmt.Println("---")
		for _, line := range st.Tail {
			fmt.Println(line)
		}
	}
	return 0
}

// ringmasterRead is the pull / observability surface (RFC-0009 §8). With --job
// it prints that job's full record stream (no cursor advance). Without --job it
// scans the current session's channel for waking events, optionally filtered by
// --since (ts lower bound, exclusive) and --type. Each record is one line:
// NDJSON with --json, else the notification line (§9).
func ringmasterRead(args []string) int {
	fs := flag.NewFlagSet("ringmaster read", flag.ContinueOnError)
	job := fs.String("job", "", "show one job's full record stream")
	since := fs.String("since", "", "channel mode: only events with ts > this RFC3339 value")
	asJSON := fs.Bool("json", false, "emit one JSON object per line instead of the notification line")
	var types stringList
	fs.Var(&types, "type", "channel mode: only events of this type (repeatable)")
	// --peek is accepted for forward compatibility; it is a no-op until the
	// persisted read cursor lands (RFC-0009 §8).
	_ = fs.Bool("peek", false, "do not advance the read cursor (no-op until cursor persistence lands)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cid := jobwake.ChannelID(jobwake.SessionKey())

	var recs []jobwake.Record
	if *job != "" {
		var err error
		recs, err = jobwake.ReadJob(cid, *job)
		if err != nil {
			if os.IsNotExist(err) {
				return 0 // no such job => empty stream, not an error
			}
			fmt.Fprintf(os.Stderr, "ringmaster read: %v\n", err)
			return 1
		}
	} else {
		waking, err := jobwake.ScanWaking(cid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ringmaster read: %v\n", err)
			return 1
		}
		typeSet := map[string]struct{}{}
		for _, t := range types {
			typeSet[t] = struct{}{}
		}
		for _, r := range waking {
			if *since != "" && r.TS <= *since {
				continue
			}
			if len(typeSet) > 0 {
				if _, ok := typeSet[r.Type]; !ok {
					continue
				}
			}
			recs = append(recs, r)
		}
	}

	return printRecords(recs, *asJSON)
}

func printRecords(recs []jobwake.Record, asJSON bool) int {
	for _, r := range recs {
		if asJSON {
			b, err := json.Marshal(r)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ringmaster read: %v\n", err)
				return 1
			}
			fmt.Println(string(b))
			continue
		}
		fmt.Println(notificationLine(r))
	}
	return 0
}

// stringList is a repeatable string flag (e.g. --type succeeded --type failed).
type stringList []string

func (s *stringList) String() string { return fmt.Sprint([]string(*s)) }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// ringmasterMonitor runs the channel wake monitor (RFC-0009 §8, §9; RFC-0015 §6,
// formerly `clown job-watch`). The long-running mode binds the channel socket,
// replays unacked waking events, then blocks, emitting one notification line per
// waking event until SIGINT or SIGTERM. With --once it replays unacked waking
// events and exits without binding. When the facility is disabled it exits 0
// immediately without binding.
//
// The monitor deliberately ignores stdin: Claude Code spawns plugin monitors
// with an immediately-EOF stdin, so an stdin-EOF shutdown path would make the
// monitor exit right after replay at session start — the silent-no-wake failure
// this channel exists to prevent.
func ringmasterMonitor(args []string) int {
	if jobWakeupDisabled() {
		return 0 // RFC-0009 §8
	}
	fs := flag.NewFlagSet("ringmaster monitor", flag.ContinueOnError)
	once := fs.Bool("once", false, "replay unacked waking events, then exit")
	session := fs.String("session", "", "per-instance channel key to watch (RFC-0009 §2); overrides CLOWN_SESSION_ID")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// --session seeds this monitor's OWN process env (clown#136). The monitor is
	// a leaf — it spawns no claude subtree — so localizing the key here is
	// harmless, and it makes every jobwake.SessionKey() call inside Watch resolve
	// to the explicit key without clown having to stamp CLOWN_SESSION_ID on the
	// env claude inherits.
	if *session != "" {
		_ = os.Setenv("CLOWN_SESSION_ID", *session)
	}

	emit := func(r jobwake.Record) error {
		_, werr := fmt.Println(notificationLine(r))
		return werr
	}

	if *once {
		if err := jobwake.ReplayOnce(jobwake.SessionKey(), emit); err != nil {
			fmt.Fprintf(os.Stderr, "ringmaster monitor: %v\n", err)
			return 1
		}
		return 0
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	err := jobwake.Watch(ctx, jobwake.SessionKey(), emit)
	if ctx.Err() != nil {
		return 0 // SIGINT/SIGTERM is a normal monitor shutdown
	}
	if errors.Is(err, jobwake.ErrAlreadyWatching) {
		// Singleton (clown#132): another live monitor already owns this channel,
		// so this one is a no-op, not a failure.
		fmt.Fprintln(os.Stderr, "ringmaster monitor: another monitor is already watching this channel; exiting")
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ringmaster monitor: %v\n", err)
		return 1
	}
	return 0
}

// notificationLine renders a waking record as the agent notification line
// (RFC-0009 §9): "[clown-job] <source> <job> <type>: <message> · <result_ref>".
// When the record carries a `from` (sender session key), " from <from>" is
// inserted before the colon. The ": " is omitted when message is empty;
// " · <result_ref>" is appended only when result_ref is present. Embedded
// newlines in message are flattened to spaces so the line never breaks the
// one-line-per-event contract.
func notificationLine(r jobwake.Record) string {
	line := fmt.Sprintf("[clown-job] %s %s %s", r.Source, r.Job, r.Type)
	if from := flattenLine(r.From); from != "" {
		line += " from " + from
	}
	if msg := flattenLine(r.Message); msg != "" {
		line += ": " + msg
	}
	if r.ResultRef != "" {
		line += " · " + flattenLine(r.ResultRef)
	}
	if n := len(r.Resources); n > 0 {
		// One-line contract (RFC-0009 §9): the wake carries only a count hint;
		// the URIs come from the pull side (ringmaster read / troupe read, #112).
		line += fmt.Sprintf(" · %d resource(s)", n)
	}
	return line
}

// lineFlattener replaces newline characters with spaces so a record never breaks
// the one-line-per-event contract (RFC-0009 §9).
var lineFlattener = strings.NewReplacer("\n", " ", "\r", " ")

func flattenLine(s string) string {
	return lineFlattener.Replace(s)
}
