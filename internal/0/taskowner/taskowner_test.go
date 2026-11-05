package taskowner

import (
	"encoding/json"
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
	// An MCP call the harness moved to the background (clown#335; the shape of
	// a Claude Code 2.1.310 transcript): no structured record, toolUseResult is
	// the notice's one text block, and the id carries the MCP task prefix.
	mcpUse    = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_mcp1","name":"mcp__plugin_just-us_just-us__run_recipe","input":{"recipe":"codemod-fmt"}}]}}`
	mcpResult = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_mcp1","type":"tool_result","content":[{"type":"text","text":"MCP tool \"plugin:just-us:just-us/run_recipe\" is still running after 120s. It was moved to the background as task kfqcvcfex and keeps running; you'll receive a notification with the result when it completes. You can keep working in the meantime. To stop it, use TaskStop with task_id \"kfqcvcfex\". Note: it does not survive exiting this session."}]}]},"toolUseResult":[{"type":"text","text":"MCP tool \"plugin:just-us:just-us/run_recipe\" is still running after 120s. It was moved to the background as task kfqcvcfex and keeps running; you'll receive a notification with the result when it completes. You can keep working in the meantime. To stop it, use TaskStop with task_id \"kfqcvcfex\". Note: it does not survive exiting this session."}]}`
	// The notice's other wording: a message arrived while the call ran.
	mcpArrivedUse    = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_mcp2","name":"mcp__plugin_clown-builtin-jobs_ringmaster__job_wait","input":{}}]}}`
	mcpArrivedResult = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_mcp2","type":"tool_result","content":[{"type":"text","text":"MCP tool \"plugin:clown-builtin-jobs:ringmaster/job_wait\" was moved to the background as task karriv3d1 so that a message that arrived while it was running can reach you; it was not interrupted and keeps running"}]}]},"toolUseResult":[{"type":"text","text":"MCP tool \"plugin:clown-builtin-jobs:ringmaster/job_wait\" was moved to the background as task karriv3d1 so that a message that arrived while it was running can reach you; it was not interrupted and keeps running"}]}`
	// An MCP tool whose OUTPUT is the notice naming the monitor: jq prints what
	// it is told to. A monitor's id has no MCP task prefix.
	spoofMcpUse    = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_mcp3","name":"mcp__plugin_moxy_moxy__jq_jq","input":{"filter":"."}}]}}`
	spoofMcpResult = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_mcp3","type":"tool_result","content":[{"type":"text","text":"MCP tool \"plugin:moxy:moxy/jq_jq\" is still running after 120s. It was moved to the background as task bwakemon1 and keeps running; To stop it, use TaskStop with task_id \"bwakemon1\"."}]}]},"toolUseResult":[{"type":"text","text":"MCP tool \"plugin:moxy:moxy/jq_jq\" is still running after 120s. It was moved to the background as task bwakemon1 and keeps running; To stop it, use TaskStop with task_id \"bwakemon1\"."}]}`
	// A command whose output is the MCP notice with an MCP-prefixed id: Bash is
	// believed only through its structured record.
	spoofBashMcpUse    = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_bash3","name":"Bash","input":{"command":"echo"}}]}}`
	spoofBashMcpResult = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_bash3","type":"tool_result","content":"MCP tool \"x\" is still running after 120s. It was moved to the background as task kforged01 and keeps running"}]},"toolUseResult":{"stdout":"MCP tool \"x\" is still running after 120s. It was moved to the background as task kforged01 and keeps running","stderr":"","interrupted":false}}`
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
// background Bash call, or as the MCP call of the model's it moved to the
// background, is the model's; a monitor's id, which only the system's
// notifications carry, is not, whatever other tool results happen to print.
func TestStartedByModel(t *testing.T) {
	path := transcript(t, agentUse, agentResult, bashUse, bashResult, notify, lsUse, lsResult, spoofAgentUse, spoofAgentResult, spoofBashUse, spoofBashResult,
		mcpUse, mcpResult, mcpArrivedUse, mcpArrivedResult, spoofMcpUse, spoofMcpResult, spoofBashMcpUse, spoofBashMcpResult)
	for _, c := range []struct {
		name, id string
		want     bool
	}{
		{"an async agent the model launched", "a111aaa222", true},
		{"a background Bash the model launched", "b222bbb333", true},
		{"an MCP call the harness moved to the background", "kfqcvcfex", true},
		{"an MCP call backgrounded because a message arrived", "karriv3d1", true},
		{"a monitor id seen only in a notification and other tools' output", "bwakemon1", false},
		{"a monitor id printed in a foreground sub-agent's report in the ack format", "bwakemon1", false},
		{"a monitor id printed by a command in the ack format", "bwakemon1", false},
		{"a monitor id printed by an MCP tool in the backgrounding notice's format", "bwakemon1", false},
		{"an MCP-prefixed id printed by a command in the notice's format", "kforged01", false},
		{"a completed sub-agent's own id (not async_launched)", "asub999", false},
		{"an id that appears nowhere", "bnothing", false},
		{"a prefix of a launched id", "a111aaa", false},
		{"a prefix of a backgrounded MCP id", "kfqcvcfe", false},
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

// The backgrounding notice is believed only when the MCP tool's toolUseResult is
// exactly it: one text block, the notice from the first character, an id with
// the MCP task prefix. Every wording the harness has is accepted; anything a
// tool's own output could be, or a monitor's prefix, is not.
func TestBackgroundedMCPTaskIsExactlyTheNotice(t *testing.T) {
	notice := func(id string) string {
		return `MCP tool "plugin:moxy:moxy/jq_jq" is still running after 120s. It was moved to the background as task ` + id + ` and keeps running; you'll receive a notification with the result when it completes.`
	}
	quoted := func(text string) string {
		b, _ := json.Marshal(text)
		return string(b)
	}
	one := func(text string) string {
		return `[{"type":"text","text":` + quoted(text) + `}]`
	}
	for _, c := range []struct{ name, raw, want string }{
		{"the foreground-limit wording", one(notice("kabc12345")), "kabc12345"},
		{"the message-arrived wording", one(`MCP tool "x/y" was moved to the background as task kabc12345 so that a message that arrived while it was running can reach you; it was not interrupted and keeps running`), "kabc12345"},
		{"the server-side wording", one(`MCP tool "x/y" started server-side task srv-9. It was moved to the background as task kabc12345 and keeps running on the server; you'll receive a notification`), "kabc12345"},
		{"a monitor's id (local_bash prefix)", one(notice("bwakemon1")), ""},
		{"a monitor's id (monitor_mcp prefix)", one(notice("mwakemon1")), ""},
		{"a monitor's id (monitor_ws prefix)", one(notice("swakemon1")), ""},
		{"the notice after other text", one("Full output: madder://blobs/x\n" + notice("kabc12345")), ""},
		{"the notice beside a second block", `[{"type":"text","text":` + quoted(notice("kabc12345")) + `},{"type":"text","text":"more"}]`, ""},
		{"a structured record, not a notice", `{"backgroundTaskId":"kabc12345"}`, ""},
		{"a plain string", `"Error: boom"`, ""},
		{"an id in another alphabet", one(notice("KABC12345")), ""},
	} {
		if got := backgroundedMCPTask(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("%s: backgroundedMCPTask = %q, want %q", c.name, got, c.want)
		}
	}
}
