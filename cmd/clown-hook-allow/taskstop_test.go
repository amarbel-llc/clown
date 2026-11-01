package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Operator ruling 2026-10-27 (stage 2, option A): in an unattended session the
// worker may stop only the tasks it started itself; the wake monitor and the
// troupe monitor, started by Claude Code from a plugin manifest, are refused.
// Fail closed: a transcript the hook cannot read, or an id it cannot place,
// is a refusal. Attended sessions are untouched.

const (
	tsAgentUse    = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_a1","name":"Agent","input":{}}]}}`
	tsAgentResult = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_a1","type":"tool_result","content":[{"type":"text","text":"Async agent launched successfully.\nagentId: amine123 (internal)"}]}]},"toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"amine123"}}`
	tsNotify      = `{"type":"queue-operation","operation":"enqueue","content":"<task-notification>\n<task-id>bwake99</task-id>\n</task-notification>"}`
)

func taskStopTranscript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join([]string{tsAgentUse, tsAgentResult, tsNotify}, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func taskStop(id, transcript string) hookInput {
	in := hookInput{ToolName: "TaskStop", ToolInput: json.RawMessage(`{"task_id":"` + id + `"}`), TranscriptPath: transcript}
	return in
}

func decision(out *hookOutput) string {
	if out == nil {
		return "defer"
	}
	return out.HookSpecificOutput.PermissionDecision
}

func TestTaskStopUnattended(t *testing.T) {
	t.Setenv("CLOWN_UNATTENDED", "1")
	tr := taskStopTranscript(t)

	// The worker's own task is stopped without a prompt of its own: the hook has
	// no objection, so it defers to the permission mode.
	if got := decision(evaluate(taskStop("amine123", tr))); got != "defer" {
		t.Errorf("TaskStop of the model's own task = %q, want defer", got)
	}

	for name, in := range map[string]hookInput{
		"the wake or troupe monitor":              taskStop("bwake99", tr),
		"an unknown id":                           taskStop("bnothing", tr),
		"no transcript path":                      taskStop("amine123", ""),
		"an unreadable transcript":                taskStop("amine123", filepath.Join(t.TempDir(), "absent.jsonl")),
		"no task id":                              {ToolName: "TaskStop", ToolInput: json.RawMessage(`{}`), TranscriptPath: tr},
		"malformed input":                         {ToolName: "TaskStop", ToolInput: json.RawMessage(`not json`), TranscriptPath: tr},
		"the legacy shell_id form of the monitor": {ToolName: "TaskStop", ToolInput: json.RawMessage(`{"shell_id":"bwake99"}`), TranscriptPath: tr},
		"a lookalike prefix of an own id":         taskStop("amine", tr),
	} {
		out := evaluate(in)
		if decision(out) != "deny" {
			t.Errorf("%s: decision = %q, want deny", name, decision(out))
			continue
		}
		if out.HookSpecificOutput.HookEventName != "PreToolUse" || out.HookSpecificOutput.PermissionDecisionReason == "" {
			t.Errorf("%s: deny must be the nested PreToolUse form with a reason: %+v", name, out.HookSpecificOutput)
		}
	}
}

// The older names of the same tool, and the legacy shell_id field, follow the
// same rule (reviewer finding): the monitor is refused, the worker's own task is
// not.
func TestTaskStopAliasesFollowTheSameRule(t *testing.T) {
	t.Setenv("CLOWN_UNATTENDED", "1")
	tr := taskStopTranscript(t)
	for _, tool := range []string{"KillShell", "KillBash"} {
		mon := hookInput{ToolName: tool, ToolInput: json.RawMessage(`{"shell_id":"bwake99"}`), TranscriptPath: tr}
		if got := decision(evaluate(mon)); got != "deny" {
			t.Errorf("%s of the monitor = %q, want deny", tool, got)
		}
		own := hookInput{ToolName: tool, ToolInput: json.RawMessage(`{"shell_id":"amine123"}`), TranscriptPath: tr}
		if got := decision(evaluate(own)); got != "defer" {
			t.Errorf("%s of the worker's own task = %q, want defer", tool, got)
		}
	}
}

// The transcript may lag the turn: an id the harness records between the first
// look and the retry is the worker's own, not refused.
func TestTaskStopRetriesForALaggingTranscript(t *testing.T) {
	t.Setenv("CLOWN_UNATTENDED", "1")
	oldN, oldW := transcriptRetries, transcriptRetryWait
	transcriptRetries, transcriptRetryWait = 3, 50*time.Millisecond
	t.Cleanup(func() { transcriptRetries, transcriptRetryWait = oldN, oldW })

	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte(tsAgentUse+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(70 * time.Millisecond)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(tsAgentResult + "\n")
			f.Close()
		}
	}()
	if got := decision(evaluate(taskStop("amine123", path))); got != "defer" {
		t.Errorf("TaskStop while the transcript catches up = %q, want defer", got)
	}
}

// An attended session, or any value of the marker other than 1, is unchanged.
func TestTaskStopAttendedDefers(t *testing.T) {
	tr := taskStopTranscript(t)
	for _, v := range []string{"", "0", "yes"} {
		t.Setenv("CLOWN_UNATTENDED", v)
		if got := decision(evaluate(taskStop("bwake99", tr))); got != "defer" {
			t.Errorf("CLOWN_UNATTENDED=%q: TaskStop = %q, want defer", v, got)
		}
	}
}

// Other tools are not the hook's business here.
func TestTaskStopRuleDoesNotTouchOtherTools(t *testing.T) {
	t.Setenv("CLOWN_UNATTENDED", "1")
	tr := taskStopTranscript(t)
	in := hookInput{ToolName: "TaskOutput", ToolInput: json.RawMessage(`{"task_id":"bwake99"}`), TranscriptPath: tr}
	if got := decision(evaluate(in)); got != "defer" {
		t.Errorf("TaskOutput = %q, want defer", got)
	}
}

// The whole hook, stdin to stdout, as claude runs it: a refusal is one JSON line.
func TestTaskStopThroughRun(t *testing.T) {
	t.Setenv("CLOWN_UNATTENDED", "1")
	tr := taskStopTranscript(t)
	payload := `{"hook_event_name":"PreToolUse","tool_name":"TaskStop","tool_input":{"task_id":"bwake99"},"transcript_path":` + jsonString(tr) + `}`
	var out bytes.Buffer
	if err := run(strings.NewReader(payload), &out); err != nil {
		t.Fatal(err)
	}
	var got hookOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("output %q (%v), want a deny", out.String(), err)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
