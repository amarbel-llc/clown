package main

import (
	"encoding/json"
	"testing"
)

func parseSafetyEnv(t *testing.T, got string) map[string]string {
	t.Helper()
	if got == "" {
		return map[string]string{}
	}
	var v struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("not valid JSON: %v (%q)", err, got)
	}
	return v.Env
}

// parseRemoteControl reports whether the settings JSON carries a
// remoteControlAtStartup key and, if so, its value.
func parseRemoteControl(t *testing.T, got string) (present bool, value bool) {
	t.Helper()
	var v struct {
		RemoteControlAtStartup *bool `json:"remoteControlAtStartup"`
	}
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("not valid JSON: %v (%q)", err, got)
	}
	if v.RemoteControlAtStartup == nil {
		return false, false
	}
	return true, *v.RemoteControlAtStartup
}

// remoteControlAtStartup is injected unconditionally to disable Remote Control
// auto-connect for clown sessions, independent of the env-gated defaults.
func TestClaudeSafetySettingsJSON_RemoteControlAlwaysDisabled(t *testing.T) {
	// Env defaults present, then fully overridden — remoteControlAtStartup
	// must be false in both cases.
	for _, tc := range []struct {
		name         string
		afk, autoMem string
	}{
		{"defaults unset", "", ""},
		{"defaults overridden", "60000", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_AFK_TIMEOUT_MS", tc.afk)
			t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", tc.autoMem)

			present, val := parseRemoteControl(t, claudeSafetySettingsJSON())
			if !present {
				t.Fatal("remoteControlAtStartup missing; must always be injected")
			}
			if val {
				t.Error("remoteControlAtStartup = true, want false (Remote Control disabled)")
			}
		})
	}
}

// clown#163 + clown#164: with no user overrides, clown injects both safety
// defaults (AFK auto-continue off, auto memory off) as a valid settings env block.
func TestClaudeSafetySettingsJSON_InjectsBothWhenUnset(t *testing.T) {
	t.Setenv("CLAUDE_AFK_TIMEOUT_MS", "") // empty == unset for os.Getenv
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "")

	env := parseSafetyEnv(t, claudeSafetySettingsJSON())
	if env["CLAUDE_AFK_TIMEOUT_MS"] != afkTimeoutDisableMS {
		t.Errorf("AFK = %q, want %q", env["CLAUDE_AFK_TIMEOUT_MS"], afkTimeoutDisableMS)
	}
	if env["CLAUDE_CODE_DISABLE_AUTO_MEMORY"] != "1" {
		t.Errorf("auto-memory disable = %q, want 1", env["CLAUDE_CODE_DISABLE_AUTO_MEMORY"])
	}
	// Never 0 — that would auto-submit instantly.
	if afkTimeoutDisableMS == "0" {
		t.Fatal("afkTimeoutDisableMS must never be 0 (instant auto-submit footgun)")
	}
}

// The defaults are gated independently: overriding one still injects the other.
func TestClaudeSafetySettingsJSON_IndependentOverride(t *testing.T) {
	t.Setenv("CLAUDE_AFK_TIMEOUT_MS", "60000") // user keeps a normal AFK timeout
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "")

	env := parseSafetyEnv(t, claudeSafetySettingsJSON())
	if _, ok := env["CLAUDE_AFK_TIMEOUT_MS"]; ok {
		t.Errorf("AFK override present; should not be injected: %v", env)
	}
	if env["CLAUDE_CODE_DISABLE_AUTO_MEMORY"] != "1" {
		t.Errorf("auto-memory should still be injected when only AFK is overridden: %v", env)
	}
}

// When the user has overridden every env default, the env block is omitted —
// but the settings JSON is still non-empty because remoteControlAtStartup is
// injected unconditionally.
func TestClaudeSafetySettingsJSON_AllEnvOverridden(t *testing.T) {
	t.Setenv("CLAUDE_AFK_TIMEOUT_MS", "60000")
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "0") // user opts back into auto memory

	got := claudeSafetySettingsJSON()
	if got == "" {
		t.Fatal("settings must never be empty (remoteControlAtStartup is always injected)")
	}
	if env := parseSafetyEnv(t, got); len(env) != 0 {
		t.Errorf("env block should be omitted when every env default is overridden, got %v", env)
	}
}
