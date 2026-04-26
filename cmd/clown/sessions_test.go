package main

import (
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/clown/internal/sessions"
)

func TestParseSessionsCompleteArgs_Empty(t *testing.T) {
	got, err := parseSessionsCompleteArgs(nil)
	if err != nil {
		t.Fatalf("parseSessionsCompleteArgs: %v", err)
	}
	if got.pwdOnly {
		t.Error("pwdOnly = true, want false")
	}
}

func TestParseSessionsCompleteArgs_PwdOnly(t *testing.T) {
	got, err := parseSessionsCompleteArgs([]string{"--pwd-only"})
	if err != nil {
		t.Fatalf("parseSessionsCompleteArgs: %v", err)
	}
	if !got.pwdOnly {
		t.Error("pwdOnly = false, want true")
	}
}

func TestParseSessionsCompleteArgs_RejectsUnknown(t *testing.T) {
	if _, err := parseSessionsCompleteArgs([]string{"--bogus"}); err == nil {
		t.Error("expected error for unknown flag, got nil")
	}
}

func TestParseSessionsCompleteArgs_RejectsPositional(t *testing.T) {
	if _, err := parseSessionsCompleteArgs([]string{"some-arg"}); err == nil {
		t.Error("expected error for positional arg, got nil")
	}
}

func TestFormatSessionCompletionDesc_PrefersTitle(t *testing.T) {
	s := sessions.Session{
		ID:      "abc-123",
		Title:   "fixing bug",
		ModTime: time.Now().Add(-5 * time.Minute),
	}
	got := formatSessionCompletionDesc(s)
	want := "5m ago  fixing bug"
	if got != want {
		t.Errorf("desc = %q, want %q", got, want)
	}
}

func TestFormatSessionCompletionDesc_FallsBackToID(t *testing.T) {
	s := sessions.Session{
		ID:      "abc-123",
		Title:   "",
		ModTime: time.Now().Add(-5 * time.Minute),
	}
	got := formatSessionCompletionDesc(s)
	want := "5m ago  abc-123"
	if got != want {
		t.Errorf("desc = %q, want %q", got, want)
	}
}

// Smoke test that the value/description split matches fish's expected
// format: literally one tab between the URI and the description.
func TestFormatSessionCompletionDesc_NoTabsInDescription(t *testing.T) {
	s := sessions.Session{
		ID:      "abc-123",
		Title:   "fixing\ttabbed bug",
		ModTime: time.Now().Add(-1 * time.Hour),
	}
	got := formatSessionCompletionDesc(s)
	if strings.Contains(got, "\t") {
		t.Errorf("description %q contains a tab; would confuse fish completions", got)
	}
}
