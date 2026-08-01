// Added for mcp-collapse permission-mux POC (throwaway; stage 1 mechanics).
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runHook feeds hookInputJSON to run() and returns trimmed stdout.
func runHook(t *testing.T, hookInputJSON string) string {
	t.Helper()
	var out bytes.Buffer
	if err := run(strings.NewReader(hookInputJSON), &out); err != nil {
		t.Fatalf("run() returned error: %v", err)
	}
	return strings.TrimSpace(out.String())
}

// decodeDecision parses the hook's stdout into the nested decision, failing the
// test if it is not the expected shape.
func decodeDecision(t *testing.T, stdout string) hookSpecificOutput {
	t.Helper()
	var got hookOutput
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decoding decision %q: %v", stdout, err)
	}
	if got.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("hookEventName = %q, want PreToolUse", got.HookSpecificOutput.HookEventName)
	}
	return got.HookSpecificOutput
}

const collapsedMcpCallTool = "mcp__plugin_clown-mcp-collapse_mcp-collapse__mcp_call"

func mcpCallInput(toolID string) string {
	return `{"tool_name":"` + collapsedMcpCallTool +
		`","tool_input":{"tool_id":"` + toolID + `","args":{}}}`
}

func TestDemuxHonorsPolicyDecisions(t *testing.T) {
	cases := []struct {
		toolID string
		want   string
	}{
		// Real dotted multi-level tool_ids from the user's live session. A
		// BLOCK on smith.issue-list is the load-bearing proof that `deny` is
		// honored on a collapsed mcp__* tool.
		{"moxy/moxy.get-hubbed.api", "allow"},
		{"moxy/moxy.smith.whoami", "ask"},
		{"moxy/moxy.smith.issue-list", "deny"},
	}
	for _, tc := range cases {
		t.Run(tc.toolID, func(t *testing.T) {
			out := runHook(t, mcpCallInput(tc.toolID))
			if out == "" {
				t.Fatalf("expected a %q decision, got empty (deferred)", tc.want)
			}
			dec := decodeDecision(t, out)
			if dec.PermissionDecision != tc.want {
				t.Errorf("permissionDecision = %q, want %q", dec.PermissionDecision, tc.want)
			}
			if !strings.Contains(dec.PermissionDecisionReason, tc.toolID) {
				t.Errorf("reason %q does not mention tool_id %q", dec.PermissionDecisionReason, tc.toolID)
			}
		})
	}
}

// An unknown tool_id must fail open (defer): emit nothing.
func TestUnknownToolIDDefers(t *testing.T) {
	out := runHook(t, mcpCallInput("moxy/moxy.grit.clone"))
	if out != "" {
		t.Errorf("expected defer (empty output) for unknown tool_id, got %q", out)
	}
}

// A non-mcp_call tool must defer: this hook only speaks for the collapsed mcp_call.
func TestNonMcpCallToolDefers(t *testing.T) {
	// A plain Read, and a different aggregator verb, both defer.
	for _, name := range []string{
		"Read",
		"mcp__plugin_clown-mcp-collapse_mcp-collapse__mcp_list",
		"mcp__plugin_moxy_moxy__folio_read",
	} {
		in := `{"tool_name":"` + name + `","tool_input":{"tool_id":"moxy/moxy.smith.issue-list"}}`
		if out := runHook(t, in); out != "" {
			t.Errorf("tool_name=%q: expected defer, got %q", name, out)
		}
	}
}

// A collapsed mcp_call missing tool_id must fail open (defer).
func TestMcpCallWithoutToolIDDefers(t *testing.T) {
	in := `{"tool_name":"` + collapsedMcpCallTool + `","tool_input":{"args":{}}}`
	if out := runHook(t, in); out != "" {
		t.Errorf("expected defer for missing tool_id, got %q", out)
	}
}

// Malformed hook input must fail open (defer, no error).
func TestMalformedInputFailsOpen(t *testing.T) {
	if out := runHook(t, `{not json`); out != "" {
		t.Errorf("expected defer for malformed input, got %q", out)
	}
}

// A hit must not panic writing the decision logfile, and the decision line must
// land in it. Point XDG_LOG_HOME at a temp dir so the write is real but isolated.
func TestLogfileWriteOnHit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_LOG_HOME", dir)

	out := runHook(t, mcpCallInput("moxy/moxy.smith.issue-list"))
	if out == "" {
		t.Fatal("expected a deny decision, got empty (deferred)")
	}

	data, err := os.ReadFile(filepath.Join(dir, "clown", "hook-collapse.log"))
	if err != nil {
		t.Fatalf("reading logfile: %v", err)
	}
	line := string(data)
	if !strings.Contains(line, "moxy/moxy.smith.issue-list") || !strings.Contains(line, "decision=deny") {
		t.Errorf("logfile missing expected decision, got %q", line)
	}
}
