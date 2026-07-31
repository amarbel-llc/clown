package main

import (
	"errors"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/mcpcollapse"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    []mcpcollapse.Upstream
		wantErr string
	}{
		{
			name: "single upstream",
			in:   []string{"--upstream", "grit=http://127.0.0.1:8080/mcp"},
			want: []mcpcollapse.Upstream{
				{Name: "grit", URL: "http://127.0.0.1:8080/mcp"},
			},
		},
		{
			name: "multiple upstreams preserve order",
			in: []string{
				"--upstream", "grit=http://127.0.0.1:8080/mcp",
				"--upstream", "docs=http://127.0.0.1:8081/mcp",
			},
			want: []mcpcollapse.Upstream{
				{Name: "grit", URL: "http://127.0.0.1:8080/mcp"},
				{Name: "docs", URL: "http://127.0.0.1:8081/mcp"},
			},
		},
		{
			name: "url may itself contain equals signs",
			in:   []string{"--upstream", "q=http://127.0.0.1:8080/mcp?a=b&c=d"},
			want: []mcpcollapse.Upstream{
				{Name: "q", URL: "http://127.0.0.1:8080/mcp?a=b&c=d"},
			},
		},
		{
			name:    "no upstreams is an error",
			in:      []string{},
			wantErr: "at least one --upstream",
		},
		{
			name:    "missing equals is rejected",
			in:      []string{"--upstream", "grit"},
			wantErr: "expected name=url",
		},
		{
			name:    "empty name is rejected",
			in:      []string{"--upstream", "=http://127.0.0.1:8080/mcp"},
			wantErr: "empty name",
		},
		{
			name:    "empty url is rejected",
			in:      []string{"--upstream", "grit="},
			wantErr: "empty url",
		},
		{
			name:    "flag with no value",
			in:      []string{"--upstream"},
			wantErr: "--upstream requires an argument",
		},
		{
			name:    "unknown flag",
			in:      []string{"--frobnicate"},
			wantErr: "unknown flag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(tt.in)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil; upstreams = %#v", tt.wantErr, got.upstreams)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.upstreams) != len(tt.want) {
				t.Fatalf("upstreams = %#v, want %#v", got.upstreams, tt.want)
			}
			for i := range tt.want {
				if got.upstreams[i] != tt.want[i] {
					t.Errorf("upstream[%d] = %#v, want %#v", i, got.upstreams[i], tt.want[i])
				}
			}
		})
	}
}

func TestBuildSystemPromptFragment(t *testing.T) {
	t.Run("always emits the steering text", func(t *testing.T) {
		frag := buildSystemPromptFragment(nil)
		for _, verb := range []string{"mcp_list", "mcp_describe", "mcp_call"} {
			if !strings.Contains(frag, verb) {
				t.Errorf("fragment missing steering mention of %q:\n%s", verb, frag)
			}
		}
		if strings.Contains(frag, "degraded") {
			t.Errorf("fragment should not mention degraded when none given:\n%s", frag)
		}
	})

	t.Run("appends degraded server names when non-empty", func(t *testing.T) {
		degraded := []mcpcollapse.DegradedUpstream{
			{Name: "grit", URL: "http://127.0.0.1:8080/mcp", Err: errors.New("initialize: connection refused")},
			{Name: "docs", URL: "http://127.0.0.1:8081/mcp", Err: errors.New("tools/list: upstream error 500")},
		}
		frag := buildSystemPromptFragment(degraded)
		// Steering text still present.
		if !strings.Contains(frag, "mcp_list") {
			t.Errorf("fragment missing steering text when degraded present:\n%s", frag)
		}
		// Both failed server names named.
		if !strings.Contains(frag, "grit") || !strings.Contains(frag, "docs") {
			t.Errorf("fragment does not name both degraded servers:\n%s", frag)
		}
		// The reason is surfaced so the agent knows why.
		if !strings.Contains(frag, "connection refused") {
			t.Errorf("fragment does not surface degraded reason:\n%s", frag)
		}
		if !strings.Contains(frag, "unavailable") {
			t.Errorf("fragment does not say degraded tools are unavailable:\n%s", frag)
		}
	})
}
