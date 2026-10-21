// clown-hook-toolcost is a Claude Code Stop / SubagentStop / SessionEnd hook
// handler that turns the session transcript into per-tool context-cost
// metrics in stats-me (statsd over UDP). See package internal/toolcost for the
// metric definitions, the grouping/attribution rules and their
// approximations.
//
// Each invocation processes only the transcript bytes appended since the last
// run (a per-transcript State under $XDG_STATE_HOME/clown/hook-toolcost/),
// committing at API-response boundaries so a half-flushed trailing response
// is never counted twice and re-running on an unchanged transcript emits
// nothing. Stop and SubagentStop leave the trailing response open (its
// records may still be flushing); SessionEnd and SubagentStop mark the
// transcript final and close it.
//
// Like every clown hook it is best-effort and silent: every failure exits 0
// (a Stop-hook exit 2 would BLOCK the agent from stopping), and the run is
// bounded by runBudget. Its one stdout output is a {"systemMessage": ...} line
// when the session's context floor (the first response's total input) exceeds
// CLOWN_CONTEXT_FLOOR_PCT (default 60) percent of CLOWN_CONTEXT_WINDOW (default
// 200000 tokens): past that, auto-compact loops. CLOWN_HOOK_DEBUG_LOG, when
// set, receives the failures. CLOWN_DISABLE_STATSD=1 turns it off entirely.
//
// Wire-up: clown's synthesized clown-builtin-jobs plugin registers this
// binary for Stop, SubagentStop and SessionEnd (hooks/hooks.json) — see
// cmd/clown/jobmonitor.go.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"code.linenisgreat.com/clown/internal/sessions"
	"code.linenisgreat.com/clown/internal/toolcost"
)

// runBudget bounds one invocation well inside the hooks' timeout; hitting it
// commits the progress made and the next run resumes from there.
const runBudget = 8 * time.Second

// sessionEndBudget is tighter: SessionEnd hooks share a short harness budget
// (1.5s unless CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS is raised), and a
// SessionEnd run normally has only the tail left to process.
const sessionEndBudget = 1 * time.Second

// hookInput is the slice of the hook payload this handler consumes.
// agent_transcript_path is set only for SubagentStop (whose transcript_path
// points at the PARENT file).
type hookInput struct {
	HookEventName       string `json:"hook_event_name"`
	TranscriptPath      string `json:"transcript_path"`
	AgentTranscriptPath string `json:"agent_transcript_path"`
	CWD                 string `json:"cwd"`
}

func main() {
	dryRun := flag.String("dry-run", "", "dev loop: process this `transcript` from the start with no saved state, print the statsd lines to stdout, send nothing")
	flag.Parse()
	if *dryRun != "" {
		if err := dryRunTranscript(*dryRun, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "clown-hook-toolcost:", err)
			os.Exit(1)
		}
		return
	}
	// A panic would exit 2, which BLOCKS the agent from stopping: make
	// "always exit 0" structural rather than dependent on there being no bug.
	defer func() {
		if p := recover(); p != nil {
			debugLog("panic", fmt.Sprint(p))
		}
		os.Exit(0)
	}()
	if err := run(os.Stdin); err != nil {
		debugLog("error", err.Error())
	}
}

// dryRunTranscript is the dev-loop entry (just explore debug-toolcost-transcript):
// the whole transcript, as if finished, with the cwd-derived repo, printed
// instead of sent. It is the only mode allowed to fail loudly.
func dryRunTranscript(path string, out io.Writer) error {
	host, _ := os.Hostname()
	_, metrics, err := toolcost.Run(path, toolcost.State{}, toolcost.Config{
		Host:  toolcost.Segment(host),
		Repo:  toolcost.Segment(sessions.RepoFromCWD(transcriptCWD(path))),
		Final: true,
	})
	if err != nil {
		return err
	}
	for _, line := range toolcost.Encode(metrics) {
		fmt.Fprintln(out, line)
	}
	return nil
}

func run(stdin io.Reader) error {
	if toolcost.StatsdDisabled() {
		return nil
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}
	var in hookInput
	if err := json.Unmarshal(data, &in); err != nil {
		return fmt.Errorf("parsing hook input: %w", err)
	}
	path, final := in.TranscriptPath, in.HookEventName == "SessionEnd"
	if in.HookEventName == "SubagentStop" {
		path, final = in.AgentTranscriptPath, true
	}
	if path == "" {
		return errors.New("hook input carries no transcript path for this event")
	}

	statePath, err := toolcost.StatePath(path)
	if err != nil {
		return err
	}
	budget := runBudget
	if in.HookEventName == "SessionEnd" {
		budget = sessionEndBudget
	}
	deadline := time.Now().Add(budget)

	// One run per transcript at a time: a concurrent Stop + SessionEnd would
	// otherwise both read the same State and double-emit. A non-final loser
	// just yields (the winner covers the same bytes); a final run must not —
	// the winner may be a non-final Stop that leaves the trailing response
	// open — so it waits for the lock until its budget runs out.
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(statePath+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !final || time.Now().After(deadline) {
			return fmt.Errorf("another run holds the lock: %w", err)
		}
		time.Sleep(25 * time.Millisecond)
	}

	host, _ := os.Hostname()
	cwd := in.CWD
	if cwd == "" {
		cwd = transcriptCWD(path)
	}
	cfg := toolcost.Config{
		Host:     toolcost.Segment(host),
		Repo:     toolcost.Segment(sessions.RepoFromCWD(cwd)),
		Final:    final,
		Deadline: deadline,
		Window:   envInt64("CLOWN_CONTEXT_WINDOW"),
		FloorPct: int(envInt64("CLOWN_CONTEXT_FLOOR_PCT")),
	}
	st, metrics, err := toolcost.Run(path, toolcost.LoadState(statePath), cfg)
	if err != nil {
		return err
	}
	// State first, then emit: a lost UDP datagram is a gap, whereas
	// emit-then-fail-to-save would double-count on the next run.
	if err := toolcost.SaveState(statePath, st); err != nil {
		return err
	}
	// The one thing this hook says aloud: a context floor so large that
	// auto-compact can never get below it. systemMessage is the hook output
	// field claude shows the operator; it is emitted at most once per
	// transcript, when the first response is judged.
	if msg, over := toolcost.FloorWarning(metrics, cfg); over {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"systemMessage": msg})
	}
	return toolcost.Send(toolcost.Encode(metrics))
}

// envInt64 reads a positive integer from the environment; unset or malformed
// is 0 (the toolcost defaults).
func envInt64(name string) int64 {
	n, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// transcriptCWD finds the working directory recorded in the first transcript
// records, for payloads that carry no cwd.
func transcriptCWD(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	for i := 0; i < 200; i++ {
		line, err := r.ReadBytes('\n')
		var rec struct {
			CWD string `json:"cwd"`
		}
		if json.Unmarshal(line, &rec) == nil && rec.CWD != "" {
			return rec.CWD
		}
		if err != nil {
			break
		}
	}
	return ""
}

// debugLog appends a tagged line to $CLOWN_HOOK_DEBUG_LOG when set. The hook
// is silent by contract, so this is its only failure surface.
func debugLog(tag, payload string) {
	path := os.Getenv("CLOWN_HOOK_DEBUG_LOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[clown-hook-toolcost %s] %s\n", tag, payload)
}
