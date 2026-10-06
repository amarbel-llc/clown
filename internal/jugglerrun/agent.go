package jugglerrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
	"code.linenisgreat.com/clown/internal/jugglerbrief"
	"code.linenisgreat.com/clown/internal/jugglereval"
	"code.linenisgreat.com/clown/internal/jugglerloop"
)

// Labels and sources juggler stamps on ringmaster jobs and troupe posts.
const (
	AgentJobLabel = "juggler-agent"
	RunJobLabel   = "juggler-run"

	RunSource      = "juggler-run"
	SpawnSource    = "juggler-spawn"
	ExitWakeSource = "juggler-exit-wake"
	ResolveSource  = "juggler-resolve"
)

// finishTimeout bounds the terminal bookkeeping after the loop (ledger
// write, job_done) — run on a fresh ctx, since a holder's cancel has already
// cancelled the run's.
const finishTimeout = 30 * time.Second

// turnPostTimeout bounds one transcript stanza post.
const turnPostTimeout = 20 * time.Second

// AgentDeps are `juggler run`'s collaborators. Zero optional fields default.
type AgentDeps struct {
	Ringmaster Ringmaster
	Troupe     Troupe
	// ResolveModel resolves the brief's registry name (daemon-free first for
	// remote entries, FDR 0019 §10; the cmd wires the daemon fallback).
	ResolveModel func(ctx context.Context, name string) (rm.ResolveModelResult, error)
	// ConnectTools defaults to ConnectMoxy.
	ConnectTools func(ctx context.Context, url string) ([]jugglerloop.ToolSpec, jugglerloop.ToolExecutor, error)
	HTTPClient   *http.Client
	// Evaluate defaults to jugglereval.Evaluate.
	Evaluate func(ctx context.Context, kind, program string, ledger json.RawMessage) (bool, error)
	Stderr   io.Writer
	Now      func() time.Time
	// Moxy launches the agent's moxy from the brief's moxyfile when no
	// upstream URL is handed in.
	Moxy ToolPlaneLauncher
	// AgentStateDir is the agent's own state directory (the moxy CWD).
	AgentStateDir string
}

func (d AgentDeps) withDefaults() AgentDeps {
	if d.ConnectTools == nil {
		d.ConnectTools = ConnectMoxy
	}
	if d.Evaluate == nil {
		d.Evaluate = jugglereval.Evaluate
	}
	if d.Stderr == nil {
		d.Stderr = io.Discard
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return d
}

// AgentRequest is one `juggler run` invocation.
type AgentRequest struct {
	Brief *jugglerbrief.Brief
	// Job is the ringmaster job `juggler spawn` pre-started (its id is baked
	// into the unit's ExecStopPost). Empty: RunAgent starts the job itself on
	// the parent's channel and records the parent as its first holder.
	Job string
	// BriefStanza is the brief's stanza id in the room: the provenance parent
	// of the agent's first turn.
	BriefStanza string
	// MoxyURL overrides the tool plane with an already-running MCP upstream
	// (JUGGLER_MOXY_URL; tests). Empty: RunAgent launches moxy itself.
	MoxyURL string
	// EnvPrincipal is CLOWN_SESSION_ID as the process saw it; when set it
	// must equal brief.principal.
	EnvPrincipal string
	// WatchCancel observes a holder's job_cancel (a cancel-requested record)
	// and cancels the run, which then terminalizes aborted.
	WatchCancel bool
}

// AgentOutcome is what `juggler run` wrote.
type AgentOutcome struct {
	Job        string
	State      string
	Message    string
	LedgerPath string
	Ledger     jugglerloop.Ledger
	// ConfigError: the brief does not fit the tool plane (an allowlisted
	// tool moxy does not advertise). The job is still terminalized failed,
	// but the process exits 1.
	ConfigError bool
}

// ExitCode mirrors the terminal state (0/2/3/4), or 1 for a config error.
func (o AgentOutcome) ExitCode() int {
	if o.ConfigError {
		return ExitUsage
	}
	return ExitCodeForState(o.State)
}

// RunAgent is `juggler run` (FDR 0019 §1 bullets 1–5, §6): it drives the
// brief through the loop, posts every turn to the run's room and a progress
// record per turn, evaluates the ledger, writes the ledger as the job's
// result spool and writes the terminal record. It never emits the exit wake;
// the unit's ExecStopPost hook does (§6).
//
// A returned error means no job could be addressed (bad identity, a failed
// job_start) and nothing was written. Once the job exists, every failure is
// a `failed` terminal record and a nil error.
func RunAgent(ctx context.Context, deps AgentDeps, req AgentRequest) (AgentOutcome, error) {
	deps = deps.withDefaults()
	b := req.Brief
	if req.EnvPrincipal != "" && req.EnvPrincipal != b.Principal {
		return AgentOutcome{}, fmt.Errorf("CLOWN_SESSION_ID %q does not match brief.principal %q", req.EnvPrincipal, b.Principal)
	}
	a := &agentRun{deps: deps, brief: b, target: b.Parent, job: req.Job}
	// PLATFORM GAP (recorded, not fixed): this is where `juggler run` would
	// take the ringmaster producer's advisory lock (RFC-0016 §2) on a.job, if
	// a CLI verb for it existed. Without the lock, status liveness reads
	// `unknown` and the RFC-0018 §2.3 reaper never reaps juggler jobs, so if
	// the post-stop hook cannot run, nothing writes `interrupted`.
	//
	// PLATFORM REQUIREMENT: ringmaster protocol >= 2 (RFC-0018): the
	// `aborted` state, `cancel-requested` records and `wait --on-cancel`.
	// Brief 6 pins the binary.
	if a.job == "" {
		job, err := deps.Ringmaster.Start(ctx, a.target, AgentJobLabel, RunSource)
		if err != nil {
			return AgentOutcome{}, fmt.Errorf("ringmaster start on %s: %w", a.target, err)
		}
		a.job = job
		if err := mirrorHolder(ctx, deps.Ringmaster, a.target, a.job, FirstHolder(a.target, deps.Now())); err != nil {
			a.logf("%v", err)
		}
	}

	if deps.ResolveModel == nil {
		return a.finishWithoutLoop(jugglerloop.EndModelError, "model_error: no model resolver configured"), nil
	}
	resolved, err := deps.ResolveModel(ctx, b.Model)
	if err != nil {
		return a.finishWithoutLoop(jugglerloop.EndModelError, fmt.Sprintf("model_error: resolving %q: %v", b.Model, err)), nil
	}

	moxyURL := req.MoxyURL
	if moxyURL == "" {
		if deps.Moxy == nil || deps.AgentStateDir == "" {
			return a.finishWithoutLoop(jugglerloop.EndToolError, "tool_error: no moxy launcher or agent state dir configured"), nil
		}
		url, stop, err := deps.Moxy.Launch(ctx, deps.AgentStateDir, b.Moxyfile)
		if err != nil {
			return a.finishWithoutLoop(jugglerloop.EndToolError, "tool_error: launching moxy: "+err.Error()), nil
		}
		defer stop()
		moxyURL = url
	}
	advertised, exec, err := deps.ConnectTools(ctx, moxyURL)
	if err != nil {
		return a.finishWithoutLoop(jugglerloop.EndToolError, "tool_error: "+err.Error()), nil
	}
	tools, missing := FilterToolsToAllowlist(advertised, b.Tools)
	if len(missing) > 0 {
		out := a.finishWithoutLoop(jugglerloop.EndToolError, "config: brief.tools names tools moxy does not advertise: "+strings.Join(missing, ", "))
		out.ConfigError = true
		return out, nil
	}

	wallClock, _ := b.Limits.WallClockDuration()
	deadline := deps.Now().Add(wallClock)
	// The time the parent ctx was cancelled (SIGTERM): a cancel at or past
	// the brief's own deadline is systemd's RuntimeMaxSec backstop, not a
	// holder, and stays `failed` per §6's table.
	var parentCancelledAt atomic.Pointer[time.Time]
	runDone := make(chan struct{})
	defer close(runDone)
	go func() {
		select {
		case <-ctx.Done():
			at := deps.Now()
			parentCancelledAt.Store(&at)
		case <-runDone:
		}
	}()
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	var cancelObserved atomic.Bool
	stopWatch := func() {}
	if req.WatchCancel {
		var watchCtx context.Context
		watchCtx, stopWatch = context.WithCancel(runCtx)
		go func() {
			if deps.Ringmaster.WaitCancelRequested(watchCtx, a.target, a.job) == nil && watchCtx.Err() == nil {
				cancelObserved.Store(true)
				cancelRun()
			}
		}()
	}

	res, runErr := jugglerloop.Run(runCtx, jugglerloop.Config{
		HTTPClient:   deps.HTTPClient,
		Resolved:     resolved,
		Model:        upstreamModel(resolved, b.Model),
		SystemPrompt: b.System,
		Task:         b.Task,
		Tools:        tools,
		Exec:         exec,
		MaxSteps:     b.Limits.Steps,
		WallClock:    wallClock,
		Principal:    b.Principal,
		BriefTurnID:  req.BriefStanza,
		OnTurn:       a.postTurn,
		URIExtractor: ExtractURIsFromMCPContent,
		Now:          deps.Now,
	})
	stopWatch()
	if runErr != nil && res.End == "" {
		// An invalid loop config (e.g. a style with no codec): nothing ran.
		return a.finishWithoutLoop(jugglerloop.EndModelError, "model_error: "+runErr.Error()), nil
	}

	holderCancelled := res.End == jugglerloop.EndTimeout && runErr != nil
	if cancelObserved.Load() {
		holderCancelled = true
	}
	passed, evalErr := false, error(nil)
	if res.End == jugglerloop.EndTurn && !holderCancelled {
		passed, evalErr = a.evaluate(res.Ledger)
	}
	state, message := AgentVerdict(res, runErr, holderCancelled, passed, evalErr)
	at := parentCancelledAt.Load()
	if at == nil && ctx.Err() != nil {
		now := deps.Now()
		at = &now
	}
	if holderCancelled && !cancelObserved.Load() && at != nil && !at.Before(deadline) {
		state, message = StateFailed, "wall clock expired"
	}
	return a.finish(state, message, res.Ledger), nil
}

// upstreamModel is the model id sent to the provider: the registry entry's
// upstream id when it aliases one, else the brief's registry name.
func upstreamModel(resolved rm.ResolveModelResult, registryName string) string {
	if resolved.ModelID != "" {
		return resolved.ModelID
	}
	return registryName
}

type agentRun struct {
	deps   AgentDeps
	brief  *jugglerbrief.Brief
	target string
	job    string
}

func (a *agentRun) logf(format string, args ...any) {
	fmt.Fprintf(a.deps.Stderr, "juggler: run: "+format+"\n", args...)
}

// postTurn appends one turn to the run's room as a stanza and records a
// progress record. A failed post is logged, not fatal: the ledger, not the
// transcript, decides success.
func (a *agentRun) postTurn(t jugglerloop.Turn) {
	ctx, cancel := context.WithTimeout(context.Background(), turnPostTimeout)
	defer cancel()
	stanza, err := json.Marshal(t)
	if err != nil {
		a.logf("marshal turn %s: %v", t.ID, err)
		return
	}
	if _, err := a.deps.Troupe.PostStanza(ctx, Identity{}, a.brief.Room, RunSource, stanza); err != nil {
		a.logf("posting turn %s to %s: %v", t.ID, a.brief.Room, err)
	}
	progress := fmt.Sprintf("turn %s %s", t.ID, t.Body.Type)
	if t.Body.Type == jugglerloop.BodyTerminal && t.Body.Terminal != nil {
		progress += " " + string(t.Body.Terminal.Reason)
	}
	if err := a.deps.Ringmaster.Progress(ctx, a.target, a.job, progress); err != nil {
		a.logf("progress for turn %s: %v", t.ID, err)
	}
}

func (a *agentRun) evaluate(l jugglerloop.Ledger) (bool, error) {
	doc, err := json.Marshal(l)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), jugglereval.DefaultTimeout)
	defer cancel()
	return a.deps.Evaluate(ctx, a.brief.Evaluator.Kind, a.brief.Evaluator.Program, doc)
}

func (a *agentRun) finishWithoutLoop(end jugglerloop.EndReason, message string) AgentOutcome {
	l := jugglerloop.Ledger{Schema: jugglerloop.LedgerSchema, Calls: []jugglerloop.LedgerCall{}, End: jugglerloop.LedgerEnd{Reason: end}}
	return a.finish(StateFailed, message, l)
}

// finish writes the ledger to the job's spool and the terminal record.
func (a *agentRun) finish(state, message string, l jugglerloop.Ledger) AgentOutcome {
	ctx, cancel := context.WithTimeout(context.Background(), finishTimeout)
	defer cancel()
	out := AgentOutcome{Job: a.job, State: state, Message: message, Ledger: l}
	if doc, err := marshalDocument(l); err != nil {
		a.logf("marshal ledger: %v", err)
	} else if path, err := writeSpool(ctx, a.deps.Ringmaster, a.target, a.job, doc); err != nil {
		a.logf("%v", err)
	} else {
		out.LedgerPath = path
	}
	if err := a.deps.Ringmaster.Done(ctx, a.target, a.job, DoneRecord{State: state, Message: message, ResultRef: out.LedgerPath}); err != nil {
		a.logf("ringmaster done %s %s: %v", a.job, state, err)
	}
	return out
}
