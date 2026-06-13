package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    string
		want     string // expected permissionDecision
	}{
		{
			name:     "Read /nix/store path → allow",
			toolName: "Read",
			input:    `{"file_path":"/nix/store/abc-pkg/share/file.txt"}`,
			want:     "allow",
		},
		{
			name:     "Read outside /nix/store → defer",
			toolName: "Read",
			input:    `{"file_path":"/etc/passwd"}`,
			want:     "defer",
		},
		{
			name:     "Read with empty path → defer",
			toolName: "Read",
			input:    `{"file_path":""}`,
			want:     "defer",
		},
		{
			name:     "Read with /nix/store-prefix lookalike → defer",
			toolName: "Read",
			input:    `{"file_path":"/nix/storefoo/bar"}`,
			want:     "defer",
		},
		{
			name:     "Glob with /nix/store path → allow",
			toolName: "Glob",
			input:    `{"path":"/nix/store/abc-pkg","pattern":"**/*.txt"}`,
			want:     "allow",
		},
		{
			name:     "Glob with /nix/store pattern, no path → allow",
			toolName: "Glob",
			input:    `{"pattern":"/nix/store/*-clown/bin/*"}`,
			want:     "allow",
		},
		{
			name:     "Glob with neither rooted at /nix/store → defer",
			toolName: "Glob",
			input:    `{"path":"/home/user","pattern":"**/*.go"}`,
			want:     "defer",
		},
		{
			name:     "Grep with /nix/store path → allow",
			toolName: "Grep",
			input:    `{"pattern":"foo","path":"/nix/store/abc-pkg"}`,
			want:     "allow",
		},
		{
			name:     "Grep without path → defer",
			toolName: "Grep",
			input:    `{"pattern":"foo"}`,
			want:     "defer",
		},
		{
			name:     "Bash always defers",
			toolName: "Bash",
			input:    `{"command":"cat /nix/store/x"}`,
			want:     "defer",
		},
		{
			name:     "Write always defers (even under /nix/store)",
			toolName: "Write",
			input:    `{"file_path":"/nix/store/abc/x"}`,
			want:     "defer",
		},
		{
			name:     "Edit always defers",
			toolName: "Edit",
			input:    `{"file_path":"/nix/store/abc/x","old_string":"a","new_string":"b"}`,
			want:     "defer",
		},
		{
			name:     "Unknown tool defers",
			toolName: "SomeOtherTool",
			input:    `{}`,
			want:     "defer",
		},
		{
			name:     "clown-builtin-jobs job_read → allow",
			toolName: "mcp__plugin_clown-builtin-jobs_jobs__job_read",
			input:    `{}`,
			want:     "allow",
		},
		{
			name:     "clown-builtin-jobs broadcast job_message → allow",
			toolName: "mcp__plugin_clown-builtin-jobs_jobs__job_message",
			input:    `{"target":"*","message":"hi"}`,
			want:     "allow",
		},
		{
			name:     "another plugin's MCP tool defers",
			toolName: "mcp__plugin_moxy_moxy__rg_search",
			input:    `{"pattern":"x"}`,
			want:     "defer",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := evaluate(hookInput{
				ToolName:  tc.toolName,
				ToolInput: json.RawMessage(tc.input),
			})
			switch tc.want {
			case "allow":
				if out == nil {
					t.Fatal("got defer (nil), want allow")
				}
				if out.HookSpecificOutput.PermissionDecision != "allow" {
					t.Errorf("permissionDecision = %q, want allow", out.HookSpecificOutput.PermissionDecision)
				}
				// The nested form MUST carry hookEventName, or claude-code
				// 2.1.177 ignores the allow for MCP tools (clown#130).
				if out.HookSpecificOutput.HookEventName != "PreToolUse" {
					t.Errorf("hookEventName = %q, want PreToolUse", out.HookSpecificOutput.HookEventName)
				}
			case "defer":
				if out != nil {
					t.Errorf("got %+v, want defer (nil)", out)
				}
			default:
				t.Fatalf("bad want %q", tc.want)
			}
		})
	}
}

func TestRun_EndToEnd(t *testing.T) {
	in := strings.NewReader(`{"tool_name":"Read","tool_input":{"file_path":"/nix/store/abc-pkg/x"}}`)
	var out bytes.Buffer
	if err := run(in, &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	// MUST emit the nested hookSpecificOutput form — the bare form is ignored
	// for MCP tools by claude-code 2.1.177 (clown#130). Guard against a regress
	// to the bare shape.
	if !bytes.Contains(out.Bytes(), []byte(`"hookSpecificOutput"`)) {
		t.Fatalf("output must use the nested hookSpecificOutput form; got %s", out.String())
	}
	var got hookOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parsing stdout: %v", err)
	}
	if got.HookSpecificOutput.PermissionDecision != "allow" {
		t.Errorf("permissionDecision = %q, want allow", got.HookSpecificOutput.PermissionDecision)
	}
	if got.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("hookEventName = %q, want PreToolUse", got.HookSpecificOutput.HookEventName)
	}
}

// Defer MUST emit nothing (no opinion) so the next hook / default logic decides
// — not a bare {"permissionDecision":"defer"} (not a real claude-code value).
func TestRun_DeferEmitsNothing(t *testing.T) {
	in := strings.NewReader(`{"tool_name":"Read","tool_input":{"file_path":"/etc/passwd"}}`)
	var out bytes.Buffer
	if err := run(in, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("defer must emit nothing; got %q", out.String())
	}
}

func TestRun_MalformedInput(t *testing.T) {
	in := strings.NewReader(`not json`)
	var out bytes.Buffer
	err := run(in, &out)
	if err == nil {
		t.Fatal("expected error for malformed input, got nil")
	}
	if out.Len() != 0 {
		t.Errorf("expected empty stdout on parse failure, got %q", out.String())
	}
}
