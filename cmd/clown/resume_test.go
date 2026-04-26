package main

import (
	"reflect"
	"testing"
	"time"
)

func TestParseResumeArgs_DefaultsToClaude(t *testing.T) {
	provider, forwarded, err := parseResumeArgs(nil)
	if err != nil {
		t.Fatalf("parseResumeArgs(nil): %v", err)
	}
	if provider != "claude" {
		t.Errorf("provider = %q, want claude", provider)
	}
	if forwarded != nil {
		t.Errorf("forwarded = %v, want nil", forwarded)
	}
}

func TestParseResumeArgs_ProviderFlag(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"long form", []string{"--provider", "codex"}, "codex"},
		{"equals form", []string{"--provider=codex"}, "codex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, _, err := parseResumeArgs(tc.args)
			if err != nil {
				t.Fatalf("parseResumeArgs: %v", err)
			}
			if provider != tc.want {
				t.Errorf("provider = %q, want %q", provider, tc.want)
			}
		})
	}
}

func TestParseResumeArgs_ForwardedAfterDash(t *testing.T) {
	_, forwarded, err := parseResumeArgs([]string{"--", "--model", "sonnet"})
	if err != nil {
		t.Fatalf("parseResumeArgs: %v", err)
	}
	want := []string{"--model", "sonnet"}
	if !reflect.DeepEqual(forwarded, want) {
		t.Errorf("forwarded = %v, want %v", forwarded, want)
	}
}

func TestParseResumeArgs_ProviderThenForwarded(t *testing.T) {
	provider, forwarded, err := parseResumeArgs([]string{"--provider", "claude", "--", "--model", "sonnet"})
	if err != nil {
		t.Fatalf("parseResumeArgs: %v", err)
	}
	if provider != "claude" {
		t.Errorf("provider = %q, want claude", provider)
	}
	if !reflect.DeepEqual(forwarded, []string{"--model", "sonnet"}) {
		t.Errorf("forwarded = %v, want [--model sonnet]", forwarded)
	}
}

func TestParseResumeArgs_RejectsUnknownFlag(t *testing.T) {
	if _, _, err := parseResumeArgs([]string{"--bogus"}); err == nil {
		t.Error("expected error for unknown flag, got nil")
	}
}

func TestParseResumeArgs_ProviderMissingValue(t *testing.T) {
	if _, _, err := parseResumeArgs([]string{"--provider"}); err == nil {
		t.Error("expected error when --provider has no argument, got nil")
	}
}

func TestFormatRelDate_OldDateRendersISO(t *testing.T) {
	old, err := time.Parse(time.RFC3339, "2020-01-15T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if got := formatRelDate(old); got != "2020-01-15" {
		t.Errorf("formatRelDate(2020-01-15) = %q, want 2020-01-15", got)
	}
}

func TestFormatRelDate_RecentRendersRelative(t *testing.T) {
	cases := []struct {
		name string
		when time.Time
		want string
	}{
		{"30s ago", time.Now().Add(-30 * time.Second), "just now"},
		{"5m ago", time.Now().Add(-5 * time.Minute), "5m ago"},
		{"3h ago", time.Now().Add(-3 * time.Hour), "3h ago"},
		{"2d ago", time.Now().Add(-2 * 24 * time.Hour), "2d ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatRelDate(tc.when); got != tc.want {
				t.Errorf("formatRelDate = %q, want %q", got, tc.want)
			}
		})
	}
}
