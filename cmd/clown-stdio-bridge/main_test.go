package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    parsedArgs
		wantErr string
	}{
		{
			name: "command and wrapped args",
			in:   []string{"--command", "kagi-mcp", "--", "--api-key-env", "KAGI_KEY"},
			want: parsedArgs{
				command: "kagi-mcp",
				args:    []string{"--api-key-env", "KAGI_KEY"},
			},
		},
		{
			name: "command and empty wrapped args",
			in:   []string{"--command", "echo", "--"},
			want: parsedArgs{command: "echo", args: []string{}},
		},
		{
			name: "command without separator",
			in:   []string{"--command", "echo"},
			want: parsedArgs{command: "echo"},
		},
		{
			name: "wrapped args may include double-dash",
			in:   []string{"--command", "wrapper", "--", "inner-cmd", "--"},
			want: parsedArgs{
				command: "wrapper",
				args:    []string{"inner-cmd", "--"},
			},
		},
		{
			name:    "missing command flag",
			in:      []string{"--", "echo", "hi"},
			wantErr: "--command is required",
		},
		{
			name:    "command flag with no value",
			in:      []string{"--command"},
			wantErr: "--command requires an argument",
		},
		{
			name:    "unknown flag",
			in:      []string{"--frobnicate", "--command", "echo"},
			wantErr: "unknown flag",
		},
		{
			name:    "no args at all",
			in:      []string{},
			wantErr: "--command is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(tt.in)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil; args = %#v", tt.wantErr, got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.command != tt.want.command {
				t.Errorf("command = %q, want %q", got.command, tt.want.command)
			}
			if !slicesEqual(got.args, tt.want.args) {
				t.Errorf("args = %#v, want %#v", got.args, tt.want.args)
			}
		})
	}
}

func slicesEqual(a, b []string) bool {
	// reflect.DeepEqual treats nil and []string{} as different. A
	// caller passing args = nil and a parser returning args = []string{}
	// should be considered equivalent for this test's purposes.
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
