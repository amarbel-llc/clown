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

// jobWakeupDisabled reports whether the job-wakeup facility is switched off via
// CLOWN_DISABLE_JOB_WAKEUP=1 (RFC-0009 §8). When set, the emit subcommands are
// no-ops that still exit 0 and job-watch exits 0 without binding a socket.
func jobWakeupDisabled() bool {
	return os.Getenv("CLOWN_DISABLE_JOB_WAKEUP") == "1"
}

// runJob dispatches `clown job <subcommand>` (RFC-0009 §8). When the facility
// is disabled the emit subcommands (start/progress/done/message) no-op with
// exit 0 so producers need no conditional logic; read still works since it is
// a pull, not an emit (RFC-0009 §8).
func runJob(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "clown job: missing subcommand (start|progress|done|message|read|spool-path|status)")
		return 2
	}
	switch args[0] {
	case "start":
		if jobWakeupDisabled() {
			return 0
		}
		return jobStart(args[1:])
	case "progress":
		if jobWakeupDisabled() {
			return 0
		}
		return jobProgress(args[1:])
	case "done":
		if jobWakeupDisabled() {
			return 0
		}
		return jobDone(args[1:])
	case "message":
		if jobWakeupDisabled() {
			return 0
		}
		return jobMessage(args[1:])
	case "read":
		return jobRead(args[1:])
	case "spool-path":
		if jobWakeupDisabled() {
			return 0 // RFC-0010 §2: empty stdout + exit 0 when disabled
		}
		return jobSpoolPath(args[1:])
	case "status":
		// A read-only pull, like `read`: works regardless of the disable
		// switch (RFC-0010 §3).
		return jobStatus(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "clown job: unknown subcommand %q\n", args[0])
		return 2
	}
}

func jobStart(args []string) int {
	fs := flag.NewFlagSet("job start", flag.ContinueOnError)
	target := fs.String("target", "", "target session key (default: resolved session)")
	label := fs.String("label", "", "job label, seeds the generated id")
	source := fs.String("source", "", "emitting plugin label")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	id, err := jobwake.Start(jobwake.StartOpts{Target: *target, Label: *label, Source: *source})
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown job start: %v\n", err)
		return 1
	}
	fmt.Println(id)
	return 0
}

func jobProgress(args []string) int {
	jobID, rest, ok := jobwake.LeadingArg(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "clown job progress: missing <job-id>")
		return 2
	}
	fs := flag.NewFlagSet("job progress", flag.ContinueOnError)
	target := fs.String("target", "", "target session key (default: resolved session)")
	message := fs.String("message", "", "human-readable progress detail")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if err := jobwake.Progress(*target, jobID, *message); err != nil {
		fmt.Fprintf(os.Stderr, "clown job progress: %v\n", err)
		return 1
	}
	return 0
}

func jobDone(args []string) int {
	jobID, rest, ok := jobwake.LeadingArg(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "clown job done: missing <job-id>")
		return 2
	}
	fs := flag.NewFlagSet("job done", flag.ContinueOnError)
	target := fs.String("target", "", "target session key (default: resolved session)")
	state := fs.String("state", "", "succeeded|failed|cancelled|interrupted")
	message := fs.String("message", "", "human-readable detail")
	resultRef := fs.String("result-ref", "", "opaque result pointer")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *state == "" {
		fmt.Fprintln(os.Stderr, "clown job done: --state is required")
		return 2
	}
	if err := jobwake.Done(*target, jobID, *state, *message, *resultRef); err != nil {
		fmt.Fprintf(os.Stderr, "clown job done: %v\n", err)
		return 1
	}
	return 0
}

// jobMessage emits a standalone single-record waking `message` job (RFC-0009
// §4, §8). --target is required and may be the reserved broadcast key '*';
// --message is required and must be non-empty (usage error exit 2 otherwise).
// --from is the optional sender session key rendered in the notification line.
// Prints the generated job id on success.
func jobMessage(args []string) int {
	fs := flag.NewFlagSet("job message", flag.ContinueOnError)
	target := fs.String("target", "", "target session key, or '*' for broadcast")
	from := fs.String("from", "", "sender session key")
	source := fs.String("source", "", "emitting plugin label")
	message := fs.String("message", "", "message body")
	resultRef := fs.String("result-ref", "", "opaque result pointer")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *target == "" {
		fmt.Fprintln(os.Stderr, "clown job message: --target is required (a session key or '*')")
		return 2
	}
	if *message == "" {
		fmt.Fprintln(os.Stderr, "clown job message: --message is required and must be non-empty")
		return 2
	}
	id, err := jobwake.Message(*target, *source, *from, *message, *resultRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown job message: %v\n", err)
		return 1
	}
	fmt.Println(id)
	return 0
}

// jobSpoolPath resolves and prints the absolute output-spool path for a job
// (RFC-0010 §2). It creates the channel directory but NOT the spool file (that
// is the producer's append). An invalid job id is a usage error (exit 2).
func jobSpoolPath(args []string) int {
	jobID, rest, ok := jobwake.LeadingArg(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "clown job spool-path: missing <job-id>")
		return 2
	}
	fs := flag.NewFlagSet("job spool-path", flag.ContinueOnError)
	target := fs.String("target", "", "target session key (default: resolved session)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	path, err := jobwake.SpoolPath(*target, jobID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown job spool-path: %v\n", err)
		if errors.Is(err, jobwake.ErrInvalidJobID) {
			return 2
		}
		return 1
	}
	fmt.Println(path)
	return 0
}

// jobStatus prints a job's journal+spool-derived status (RFC-0010 §3). It is a
// read-only pull, available regardless of CLOWN_DISABLE_JOB_WAKEUP. A missing
// journal exits 1; an invalid job id exits 2.
func jobStatus(args []string) int {
	jobID, rest, ok := jobwake.LeadingArg(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "clown job status: missing <job-id>")
		return 2
	}
	fs := flag.NewFlagSet("job status", flag.ContinueOnError)
	target := fs.String("target", "", "target session key (default: resolved session)")
	tail := fs.Int("tail", 20, "number of trailing spool lines to show")
	asJSON := fs.Bool("json", false, "emit the status as a single JSON object")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	st, err := jobwake.StatusOf(*target, jobID, *tail, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown job status: %v\n", err)
		if errors.Is(err, jobwake.ErrInvalidJobID) {
			return 2
		}
		return 1 // missing journal and any other failure
	}
	if *asJSON {
		b, err := json.Marshal(st)
		if err != nil {
			fmt.Fprintf(os.Stderr, "clown job status: %v\n", err)
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

// jobRead is the pull / observability surface (RFC-0009 §8). With --job it
// prints that job's full record stream (no cursor advance). Without --job it
// scans the channel for waking events, optionally filtered by --since (ts
// lower bound, exclusive) and --type. Each record is one line: NDJSON with
// --json, else the notification line (§9).
//
// TODO(RFC-0009 §8): the cursor-advancing channel read — return only waking
// events newer than the caller's persisted read cursor and (unless --peek)
// advance it — is not yet implemented. The current channel read is the
// cursorless --since/--type filter. --peek is accepted and is a no-op today
// because no cursor is advanced; it becomes meaningful once the persisted
// read cursor lands.
func jobRead(args []string) int {
	fs := flag.NewFlagSet("job read", flag.ContinueOnError)
	job := fs.String("job", "", "show one job's full record stream")
	since := fs.String("since", "", "channel mode: only events with ts > this RFC3339 value")
	asJSON := fs.Bool("json", false, "emit one JSON object per line instead of the notification line")
	var types stringList
	fs.Var(&types, "type", "channel mode: only events of this type (repeatable)")
	// --peek is accepted for forward compatibility; it is a no-op until the
	// persisted read cursor lands (see TODO above / RFC-0009 §8).
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
			fmt.Fprintf(os.Stderr, "clown job read: %v\n", err)
			return 1
		}
	} else {
		waking, err := jobwake.ScanWaking(cid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "clown job read: %v\n", err)
			return 1
		}
		// Apply the cursorless --since (exclusive ts lower bound) and
		// repeatable --type filters to the ts-sorted waking records.
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
				fmt.Fprintf(os.Stderr, "clown job read: %v\n", err)
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

// runJobWatch runs the channel monitor (RFC-0009 §8, §9). The long-running
// mode binds the channel socket, replays unacked waking events, then blocks,
// emitting one notification line per waking event until SIGINT or SIGTERM.
// With --once it replays unacked waking events and exits without binding —
// the conformance suite's deterministic mode and a pull-style replay surface.
// When the facility is disabled it exits 0 immediately without binding.
//
// The monitor deliberately ignores stdin: Claude Code spawns plugin monitors
// with an immediately-EOF stdin, so the earlier stdin-EOF shutdown path made
// the monitor exit right after replay at session start — the silent-no-wake
// failure this channel exists to prevent.
func runJobWatch(args []string) int {
	if jobWakeupDisabled() {
		return 0 // RFC-0009 §8
	}
	fs := flag.NewFlagSet("job-watch", flag.ContinueOnError)
	once := fs.Bool("once", false, "replay unacked waking events, then exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	emit := func(r jobwake.Record) error {
		_, werr := fmt.Println(notificationLine(r))
		return werr
	}

	if *once {
		if err := jobwake.ReplayOnce(jobwake.SessionKey(), emit); err != nil {
			fmt.Fprintf(os.Stderr, "clown job-watch: %v\n", err)
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
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown job-watch: %v\n", err)
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
	return line
}

// lineFlattener replaces newline characters with spaces so a record never
// breaks the one-line-per-event contract (RFC-0009 §9).
var lineFlattener = strings.NewReplacer("\n", " ", "\r", " ")

func flattenLine(s string) string {
	return lineFlattener.Replace(s)
}
