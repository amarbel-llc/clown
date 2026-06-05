package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/amarbel-llc/clown/internal/jobwake"
)

// jobWakeupDisabled reports whether the job-wakeup facility is switched off via
// CLOWN_DISABLE_JOB_WAKEUP=1 (RFC-0009 §8). When set, the emit subcommands are
// no-ops that still exit 0 and job-watch exits 0 without binding a socket.
func jobWakeupDisabled() bool {
	return os.Getenv("CLOWN_DISABLE_JOB_WAKEUP") == "1"
}

// runJob dispatches `clown job <subcommand>` (RFC-0009 §8). When the facility is
// disabled the emit subcommands (start/progress/done) no-op with exit 0 so
// producers need no conditional logic; read still works since it is a pull, not
// an emit (RFC-0009 §8).
func runJob(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "clown job: missing subcommand (start|progress|done|read)")
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
	case "read":
		return jobRead(args[1:])
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
	jobID, rest, ok := leadingJobID(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "clown job progress: missing <job-id>")
		return 2
	}
	fs := flag.NewFlagSet("job progress", flag.ContinueOnError)
	message := fs.String("message", "", "human-readable progress detail")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if err := jobwake.Progress(jobID, *message); err != nil {
		fmt.Fprintf(os.Stderr, "clown job progress: %v\n", err)
		return 1
	}
	return 0
}

func jobDone(args []string) int {
	jobID, rest, ok := leadingJobID(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "clown job done: missing <job-id>")
		return 2
	}
	fs := flag.NewFlagSet("job done", flag.ContinueOnError)
	state := fs.String("state", "", "succeeded|failed|cancelled|interrupted")
	message := fs.String("message", "", "human-readable detail")
	resultRef := fs.String("result-ref", "", "opaque result pointer")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if err := jobwake.Done(jobID, *state, *message, *resultRef); err != nil {
		fmt.Fprintf(os.Stderr, "clown job done: %v\n", err)
		return 1
	}
	return 0
}

// leadingJobID splits a positional job id (the RFC-0009 §8 `<job-id>` argument
// that precedes the flags) from the remaining flag args. Go's flag package
// stops at the first non-flag token, so progress/done — which take the job id
// first — must peel it off before parsing. Returns ok=false when the first
// token is missing or looks like a flag.
func leadingJobID(args []string) (jobID string, rest []string, ok bool) {
	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		return "", args, false
	}
	return args[0], args[1:], true
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
		var err error
		recs, err = readChannelWaking(cid, *since, types)
		if err != nil {
			fmt.Fprintf(os.Stderr, "clown job read: %v\n", err)
			return 1
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

// readChannelWaking enumerates the channel's waking events (cursorless),
// applying the --since (exclusive ts lower bound) and --type filters. It reads
// the journal directory and each job file via the jobwake public API rather
// than reimplementing the monitor's scan logic.
func readChannelWaking(channelID, since string, types stringList) ([]jobwake.Record, error) {
	dir := jobwake.JournalDir(channelID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	typeSet := map[string]struct{}{}
	for _, t := range types {
		typeSet[t] = struct{}{}
	}
	var out []jobwake.Record
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name[0] == '.' || len(name) < 6 || name[len(name)-6:] != ".jsonl" {
			continue
		}
		jobID := name[:len(name)-6]
		recs, err := jobwake.ReadJob(channelID, jobID)
		if err != nil {
			continue // skip an unreadable job file (RFC-0009 §10)
		}
		for _, r := range recs {
			if !jobwake.IsWaking(r.Type) {
				continue
			}
			if since != "" && r.TS <= since {
				continue
			}
			if len(typeSet) > 0 {
				if _, ok := typeSet[r.Type]; !ok {
					continue
				}
			}
			out = append(out, r)
		}
	}
	// Sort oldest-first by ts for a stable, chronological stream.
	sortRecordsByTS(out)
	return out, nil
}

func sortRecordsByTS(recs []jobwake.Record) {
	for i := 1; i < len(recs); i++ {
		for j := i; j > 0 && recs[j-1].TS > recs[j].TS; j-- {
			recs[j-1], recs[j] = recs[j], recs[j-1]
		}
	}
}

// stringList is a repeatable string flag (e.g. --type succeeded --type failed).
type stringList []string

func (s *stringList) String() string { return fmt.Sprint([]string(*s)) }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// runJobWatch runs the channel monitor (RFC-0009 §8, §9): it binds the channel
// socket, replays unacked waking events, then blocks, emitting one
// notification line per waking event. A clean interrupt (SIGINT or stdin EOF)
// exits 0. When the facility is disabled it exits 0 immediately without binding.
func runJobWatch(_ []string) int {
	if jobWakeupDisabled() {
		return 0 // RFC-0009 §8
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	go watchStdinEOF(cancel)

	err := jobwake.Watch(ctx, jobwake.SessionKey(), func(r jobwake.Record) error {
		_, werr := fmt.Println(notificationLine(r))
		return werr
	})
	if ctx.Err() != nil {
		return 0 // clean interrupt / stdin EOF is a normal monitor shutdown
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown job-watch: %v\n", err)
		return 1
	}
	return 0
}

// watchStdinEOF cancels the watch when stdin closes, so a parent that wires the
// monitor's stdin to a pipe can stop it by closing that pipe (RFC-0009 §8).
func watchStdinEOF(cancel context.CancelFunc) {
	buf := make([]byte, 256)
	for {
		if _, err := os.Stdin.Read(buf); err != nil {
			cancel()
			return
		}
	}
}

// notificationLine renders a waking record as the agent notification line
// (RFC-0009 §9): "[clown-job] <source> <job> <type>: <message> · <result_ref>".
// The ": " is omitted when message is empty; " · <result_ref>" is appended only
// when result_ref is present. Embedded newlines in message are flattened to
// spaces so the line never breaks the one-line-per-event contract.
func notificationLine(r jobwake.Record) string {
	line := fmt.Sprintf("[clown-job] %s %s %s", r.Source, r.Job, r.Type)
	if msg := flattenLine(r.Message); msg != "" {
		line += ": " + msg
	}
	if r.ResultRef != "" {
		line += " · " + flattenLine(r.ResultRef)
	}
	return line
}

func flattenLine(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' {
			out = append(out, ' ')
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
