package jugglerloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

const (
	DefaultMaxSteps  = 12
	DefaultWallClock = 2 * time.Minute
	DefaultMaxTokens = 4096

	// CannotCompleteTool is the tool the loop always offers the model.
	// Calling it ends the run and records its reason in the ledger.
	CannotCompleteTool = "cannot_complete"
)

// CannotCompleteSpec is the injected cannot_complete tool definition.
var CannotCompleteSpec = ToolSpec{
	Name:        CannotCompleteTool,
	Description: "Call this when you cannot complete the task. It ends the run; explain why in reason.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"reason":{"type":"string","description":"Why the task cannot be completed."}},"required":["reason"]}`),
}

// ToolExecutor runs one tool call. A returned err is a transport failure
// (an unrecovered tool error that ends the run); isError=true is a normal
// tool-level error whose content the model sees as a tool_result.
type ToolExecutor interface {
	Call(ctx context.Context, name string, args json.RawMessage) (content json.RawMessage, isError bool, err error)
}

// Config is one run's inputs. Codec, HTTPClient, MaxSteps, WallClock,
// MaxTokens, URIExtractor, NewTurnID and Now default when zero; Codec
// defaults to CodecFor(Resolved). Principal and Tools' executor are
// required.
type Config struct {
	Codec      Codec
	HTTPClient *http.Client
	Resolved   rm.ResolveModelResult
	Model      string

	SystemPrompt string
	// Task is sent to the model as the opening user message. It is the
	// brief's content, so it is not re-recorded as a transcript turn; the
	// first transcript turn's Parent is BriefTurnID.
	Task  string
	Tools []ToolSpec
	Exec  ToolExecutor

	// MaxSteps caps model round trips: one step is one request/response
	// with the provider, regardless of how many tool calls it carries. The
	// cap is checked before each request, so the tool calls of the final
	// permitted step still execute and the run then ends with step_cap.
	MaxSteps int
	// WallClock bounds the whole run, model requests and tool calls alike;
	// on expiry the in-flight call is cancelled and the run ends with
	// timeout. Cancelling the parent ctx ends the run the same way, but Run
	// then also returns the ctx's error so the caller can tell a holder's
	// cancel from the brief's own limit.
	WallClock time.Duration
	MaxTokens int

	// Principal is the sender of every turn the run appends.
	Principal   string
	BriefTurnID string
	// OnTurn, when set, is called synchronously with each turn as it is
	// appended, terminal turn included.
	OnTurn func(Turn)

	URIExtractor URIExtractor
	// StopWhen, when set, is called with the in-progress ledger (the final
	// ledger's shape, end reason empty) after each successful tool result; true
	// ends the run with evaluator_pass. An error is recorded as a ledger note
	// and the run goes on. The loop does not know what decides it (the brief's
	// evaluator, for `juggler run`).
	StopWhen  func(ledger json.RawMessage) (bool, error)
	NewTurnID func() string
	Now       func() time.Time
}

// Result is what a run recorded. The loop never decides success.
type Result struct {
	Transcript Transcript
	Ledger     Ledger
	End        EndReason
}

type run struct {
	cfg        Config
	transcript Transcript
	ledger     Ledger
	kinds      map[string]string
	lastTurnID string
	start      time.Time
}

// Run drives the model's tool-use protocol until the model ends its turn
// (a reply with no tool calls), calls cannot_complete, StopWhen passes after
// a successful tool result, exhausts MaxSteps,
// the wall clock or ctx expires, or the executor reports a transport
// failure. The returned error is non-nil only for an invalid Config, a
// model endpoint failure (End = model_error), or a cancelled parent ctx;
// the Result is populated in every case but the first.
func Run(ctx context.Context, cfg Config) (Result, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return Result{}, err
	}
	start := cfg.Now()
	runCtx, cancel := context.WithTimeout(ctx, cfg.WallClock)
	defer cancel()

	r := &run{
		cfg:        cfg,
		transcript: Transcript{Schema: TranscriptSchema, Turns: []Turn{}},
		ledger:     newLedger(),
		kinds:      map[string]string{},
		lastTurnID: cfg.BriefTurnID,
		start:      start,
	}
	tools := toolsWithCannotComplete(cfg.Tools)
	for _, t := range tools {
		if t.Kind != "" {
			r.kinds[t.Name] = t.Kind
		}
	}

	end, runErr := r.loop(runCtx, tools)
	if end == EndTimeout && ctx.Err() != nil {
		runErr = ctx.Err()
	}
	r.append(TerminalBody(end), r.lastTurnID)
	r.ledger.End = LedgerEnd{Reason: end}
	r.ledger.ElapsedMS = cfg.Now().Sub(start).Milliseconds()
	return Result{Transcript: r.transcript, Ledger: r.ledger, End: end}, runErr
}

func (c Config) withDefaults() (Config, error) {
	if c.Principal == "" {
		return c, errors.New("jugglerloop: Principal is required")
	}
	if c.Exec == nil {
		return c, errors.New("jugglerloop: Exec is required")
	}
	if c.Codec == nil {
		codec, err := CodecFor(c.Resolved)
		if err != nil {
			return c, fmt.Errorf("jugglerloop: %w", err)
		}
		c.Codec = codec
	}
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	if c.MaxSteps <= 0 {
		c.MaxSteps = DefaultMaxSteps
	}
	if c.WallClock <= 0 {
		c.WallClock = DefaultWallClock
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = DefaultMaxTokens
	}
	if c.URIExtractor == nil {
		c.URIExtractor = ExtractURIsFromTopLevelFields
	}
	if c.NewTurnID == nil {
		c.NewTurnID = NewRandomTurnID
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c, nil
}

func toolsWithCannotComplete(tools []ToolSpec) []ToolSpec {
	out := make([]ToolSpec, 0, len(tools)+1)
	for _, t := range tools {
		if t.Name != CannotCompleteTool {
			out = append(out, t)
		}
	}
	return append(out, CannotCompleteSpec)
}

func (r *run) append(body Body, parent string) Turn {
	t := Turn{
		ID:     r.cfg.NewTurnID(),
		Sender: r.cfg.Principal,
		Parent: parent,
		At:     r.cfg.Now(),
		Body:   body,
	}
	r.transcript.Turns = append(r.transcript.Turns, t)
	r.lastTurnID = t.ID
	if r.cfg.OnTurn != nil {
		r.cfg.OnTurn(t)
	}
	return t
}

// wireTurns is the conversation sent to the model: the brief's task as the
// opening user turn (sender "" never equals Principal) followed by the
// transcript.
func (r *run) wireTurns() []Turn {
	task := Turn{ID: r.cfg.BriefTurnID, Body: TextBody(r.cfg.Task)}
	return append([]Turn{task}, r.transcript.Turns...)
}

func (r *run) loop(ctx context.Context, tools []ToolSpec) (EndReason, error) {
	for {
		if ctx.Err() != nil {
			return EndTimeout, nil
		}
		if r.ledger.Steps >= r.cfg.MaxSteps {
			return EndStepCap, nil
		}

		r.ledger.Steps++
		reply, err := RoundTrip(ctx, r.cfg.HTTPClient, r.cfg.Codec, r.cfg.Resolved, r.cfg.Model, ModelRequest{
			System:    r.cfg.SystemPrompt,
			Agent:     r.cfg.Principal,
			Turns:     r.wireTurns(),
			Tools:     tools,
			MaxTokens: r.cfg.MaxTokens,
		})
		if err != nil {
			if ctx.Err() != nil {
				return EndTimeout, nil
			}
			return EndModelError, err
		}

		var calls []Turn
		for _, t := range reply.Turns {
			appended := r.append(t.Body, r.lastTurnID)
			if appended.Body.Type == BodyToolCall {
				calls = append(calls, appended)
			}
		}
		if len(calls) == 0 {
			if reply.Stop == StopMaxTokens {
				// Cut off by the provider's token limit: not a finished answer.
				return EndMaxTokens, nil
			}
			return EndTurn, nil
		}

		for _, callTurn := range calls {
			if end, done := r.execute(ctx, callTurn); done {
				return end, nil
			}
		}
	}
}

// validateToolArgs reports why args cannot be a tool call's arguments: they
// must be a JSON object. The openai-compat codec keeps unparseable argument
// text as a JSON string, so a string is re-parsed to surface the real parse
// error.
func validateToolArgs(args json.RawMessage) error {
	var obj map[string]json.RawMessage
	err := json.Unmarshal(args, &obj)
	if err == nil {
		return nil
	}
	var text string
	if json.Unmarshal(args, &text) == nil {
		if innerErr := json.Unmarshal([]byte(text), &obj); innerErr != nil {
			return innerErr
		}
	}
	return errors.New("arguments must be a JSON object")
}

// execute runs one tool_call turn and appends its tool_result. done reports
// that the run ends here, with end as the reason.
func (r *run) execute(ctx context.Context, callTurn Turn) (end EndReason, done bool) {
	call := callTurn.Body.ToolCall
	if call.Name == CannotCompleteTool {
		var args struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(call.Args, &args)
		r.ledger.CannotComplete = &CannotComplete{Reason: args.Reason}
		return EndCannotComplete, true
	}

	entry := LedgerCall{Tool: call.Name, URIs: []string{}}
	if kind, ok := r.kinds[call.Name]; ok {
		entry.Kind = &kind
	}

	if argsErr := validateToolArgs(call.Args); argsErr != nil {
		// The model can recover from unusable arguments (e.g. JSON cut off by
		// a token limit), so the executor is not called and the run goes on.
		content, _ := json.Marshal(map[string]string{"error": "invalid tool arguments: " + argsErr.Error()})
		result := ToolResult{CallID: call.CallID, OK: false, Content: content}
		entry.Error = toolResultText(result)
		r.ledger.Calls = append(r.ledger.Calls, entry)
		r.append(ToolResultBody(result), callTurn.ID)
		return "", false
	}

	content, isError, err := r.cfg.Exec.Call(ctx, call.Name, call.Args)
	if err != nil {
		entry.Error = err.Error()
		r.ledger.Calls = append(r.ledger.Calls, entry)
		r.append(ToolResultBody(ToolResult{CallID: call.CallID, OK: false, Error: err.Error()}), callTurn.ID)
		if ctx.Err() != nil {
			return EndTimeout, true
		}
		return EndToolError, true
	}

	result := ToolResult{CallID: call.CallID, OK: !isError, Content: content}
	if isError {
		entry.Error = toolResultText(result)
	} else {
		entry.OK = true
		if uris := r.cfg.URIExtractor(call.Name, content); uris != nil {
			entry.URIs = uris
		}
	}
	r.ledger.Calls = append(r.ledger.Calls, entry)
	r.append(ToolResultBody(result), callTurn.ID)
	if entry.OK && r.stopNow() {
		return EndEvaluatorPass, true
	}
	return "", false
}

// stopNow runs Config.StopWhen over the in-progress ledger; an error is a
// note, never a stop.
func (r *run) stopNow() bool {
	if r.cfg.StopWhen == nil {
		return false
	}
	snapshot := r.ledger
	snapshot.ElapsedMS = r.cfg.Now().Sub(r.start).Milliseconds()
	doc, err := json.Marshal(snapshot)
	if err == nil {
		var stop bool
		if stop, err = r.cfg.StopWhen(doc); err == nil {
			return stop
		}
	}
	r.ledger.Notes = append(r.ledger.Notes, fmt.Sprintf("stop_on_pass check after call %d errored: %v", len(r.ledger.Calls), err))
	return false
}
