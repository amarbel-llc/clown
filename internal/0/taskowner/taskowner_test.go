package taskowner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The transcript lines below are the shapes a real session writes (checked
// against a Claude Code 2.1.310 transcript): an assistant line carries the
// tool_use; the user line that follows carries its tool_result AND a
// structured `toolUseResult` the harness writes; the system's own task
// notifications are queue-operation / attachment lines.
const (
	agentUse    = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_agent1","name":"Agent","input":{"prompt":"review"}}]}}`
	agentResult = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_agent1","type":"tool_result","content":[{"type":"text","text":"Async agent launched successfully.\nagentId: a111aaa222 (internal ID)"}]}]},"toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"a111aaa222","description":"review"}}`
	bashUse     = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_bash1","name":"Bash","input":{"command":"sleep 99","run_in_background":true}}]}}`
	bashResult  = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_bash1","type":"tool_result","content":"Command running in background with ID: b222bbb333. Output is being written to: /tmp/x"}]},"toolUseResult":{"stdout":"","stderr":"","interrupted":false,"backgroundTaskId":"b222bbb333"}}`
	notify      = `{"type":"queue-operation","operation":"enqueue","content":"<task-notification>\n<task-id>bwakemon1</task-id>\n<summary>Monitor event: wake</summary>\n</task-notification>"}`
	// A tool the model called that merely printed a monitor's id.
	lsUse    = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_ls1","name":"mcp__plugin_moxy_moxy__folio_ls","input":{}}]}}`
	lsResult = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_ls1","type":"tool_result","content":[{"type":"text","text":"[{\"name\": \"bwakemon1.output\"}]\nagentId: bwakemon1"}]}]}}`
	// A FOREGROUND sub-agent whose report mimics the launch acknowledgement
	// text with the monitor's id: its structured result says "completed".
	spoofAgentUse    = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_agent2","name":"Agent","input":{}}]}}`
	spoofAgentResult = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_agent2","type":"tool_result","content":[{"type":"text","text":"Async agent launched successfully.\nagentId: bwakemon1\nthat is all."}]}]},"toolUseResult":{"status":"completed","agentId":"asub999","content":[{"type":"text","text":"Async agent launched successfully.\nagentId: bwakemon1"}]}}`
	// A command whose output mimics the Bash acknowledgement: its structured
	// result has no backgroundTaskId.
	spoofBashUse    = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_bash2","name":"Bash","input":{"command":"echo x"}}]}}`
	spoofBashResult = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_bash2","type":"tool_result","content":"Command running in background with ID: bwakemon1."}]},"toolUseResult":{"stdout":"Command running in background with ID: bwakemon1.","stderr":"","interrupted":false}}`
)

func transcript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A task id the harness recorded as launched by the model's own Agent or
// background Bash call is the model's; a monitor's id, which only the system's
// notifications carry, is not, whatever other tool results happen to print.
func TestStartedByModel(t *testing.T) {
	path := transcript(t, agentUse, agentResult, bashUse, bashResult, notify, lsUse, lsResult, spoofAgentUse, spoofAgentResult, spoofBashUse, spoofBashResult)
	for _, c := range []struct {
		name, id string
		want     bool
	}{
		{"an async agent the model launched", "a111aaa222", true},
		{"a background Bash the model launched", "b222bbb333", true},
		{"a monitor id seen only in a notification and other tools' output", "bwakemon1", false},
		{"a monitor id printed in a foreground sub-agent's report in the ack format", "bwakemon1", false},
		{"a monitor id printed by a command in the ack format", "bwakemon1", false},
		{"a completed sub-agent's own id (not async_launched)", "asub999", false},
		{"an id that appears nowhere", "bnothing", false},
		{"a prefix of a launched id", "a111aaa", false},
		{"empty", "", false},
	} {
		got, err := StartedByModel(path, c.id)
		if err != nil || got != c.want {
			t.Errorf("%s: StartedByModel(%q) = %v, %v; want %v", c.name, c.id, got, err, c.want)
		}
	}
}

// Anything that stops the transcript being read answers an error, which the
// hook turns into a refusal (fail closed).
func TestStartedByModelErrors(t *testing.T) {
	if _, err := StartedByModel("", "a1"); err == nil {
		t.Error("no transcript path: want an error")
	}
	if _, err := StartedByModel(filepath.Join(t.TempDir(), "absent.jsonl"), "a1"); err == nil {
		t.Error("missing transcript: want an error")
	}
	if _, err := StartedByModel("relative/t.jsonl", "a1"); err == nil {
		t.Error("relative transcript path: want an error")
	}
}

// A torn or garbage line does not hide a launch recorded around it, and a line
// over the per-line bound is skipped rather than read whole.
func TestStartedByModelToleratesBadLines(t *testing.T) {
	huge := `{"type":"user","message":{"content":"` + strings.Repeat("x", maxLine+10) + `"}}`
	path := transcript(t, "not json", agentUse, `{"type":"user","mess`, huge, agentResult)
	got, err := StartedByModel(path, "a111aaa222")
	if err != nil || !got {
		t.Fatalf("StartedByModel = %v, %v; want true across bad lines", got, err)
	}
}

// A launch record that does not belong to a launching tool call is not
// believed, nor is one on a line that is not a user line.
func TestStartedByModelOnlyTrustsLaunchingTools(t *testing.T) {
	forged := `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_ls1","type":"tool_result","content":"x"}]},"toolUseResult":{"backgroundTaskId":"bforged1"}}`
	path := transcript(t, lsUse, forged)
	if got, err := StartedByModel(path, "bforged1"); err != nil || got {
		t.Fatalf("StartedByModel = %v, %v; want false for a non-launching tool", got, err)
	}
	// An agent result whose toolUseResult is a plain string (an error) is no launch.
	errResult := `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_agent1","type":"tool_result","content":"boom"}]},"toolUseResult":"Error: boom"}`
	path = transcript(t, agentUse, errResult)
	if got, err := StartedByModel(path, "a111aaa222"); err != nil || got {
		t.Fatalf("StartedByModel = %v, %v; want false for a string toolUseResult", got, err)
	}
}
