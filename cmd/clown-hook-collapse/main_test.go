// Added for mcp-collapse permission-mux POC (throwaway; stage 1 mechanics).
package main

import (
	"bytes"
	"encoding/json"
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
		{"moxy/moxy.folio_read", "allow"},
		{"moxy/moxy.folio_write", "ask"},
		{"moxy/moxy.grit_push", "deny"},
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
	out := runHook(t, mcpCallInput("moxy/moxy.grit_clone"))
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
		in := `{"tool_name":"` + name + `","tool_input":{"tool_id":"moxy/moxy.grit_push"}}`
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
