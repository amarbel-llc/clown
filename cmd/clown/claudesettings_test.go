package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// clown#163: with no user-supplied CLAUDE_AFK_TIMEOUT_MS, clown injects the
// AFK-disable override as a valid settings "env" block.
func TestClaudeSafetySettingsJSON_InjectsWhenUnset(t *testing.T) {
	t.Setenv("CLAUDE_AFK_TIMEOUT_MS", "") // empty == unset for os.Getenv

	got := claudeSafetySettingsJSON()
	if !strings.Contains(got, "CLAUDE_AFK_TIMEOUT_MS") {
		t.Fatalf("expected AFK override JSON, got %q", got)
	}
	var v struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("not valid JSON: %v (%q)", err, got)
	}
	if v.Env["CLAUDE_AFK_TIMEOUT_MS"] != afkTimeoutDisableMS {
		t.Errorf("env value = %q, want %q", v.Env["CLAUDE_AFK_TIMEOUT_MS"], afkTimeoutDisableMS)
	}
	// Never 0 — that would auto-submit instantly.
	if afkTimeoutDisableMS == "0" {
		t.Fatal("afkTimeoutDisableMS must never be 0 (instant auto-submit footgun)")
	}
}

// A user who has set CLAUDE_AFK_TIMEOUT_MS explicitly wins: clown supplies only
// the default and does not force the setting against a user who has spoken.
func TestClaudeSafetySettingsJSON_RespectsUserOverride(t *testing.T) {
	t.Setenv("CLAUDE_AFK_TIMEOUT_MS", "60000")
	if got := claudeSafetySettingsJSON(); got != "" {
		t.Errorf("with user override set, want empty (no injection), got %q", got)
	}
}
