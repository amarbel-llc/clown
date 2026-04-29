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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := evaluate(hookInput{
				ToolName:  tc.toolName,
				ToolInput: json.RawMessage(tc.input),
			})
			if d.PermissionDecision != tc.want {
				t.Errorf("got decision %q, want %q (reason=%q)",
					d.PermissionDecision, tc.want, d.Reason)
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

	var got decision
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parsing stdout: %v", err)
	}
	if got.PermissionDecision != "allow" {
		t.Errorf("permissionDecision = %q, want allow", got.PermissionDecision)
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
