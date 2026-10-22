package main

import (
	"encoding/json"
	"strings"
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

func TestProfileDisallowedTools(t *testing.T) {
	t.Setenv("CLOWN_DISALLOWED_TOOLS", "")
	if got := profileDisallowedTools(); len(got) != 0 {
		t.Errorf("unset: %v", got)
	}
	t.Setenv("CLOWN_DISALLOWED_TOOLS", " Artifact, ArtifactData ,,Workflow,")
	got := profileDisallowedTools()
	if want := "Artifact,ArtifactData,Workflow"; strings.Join(got, ",") != want {
		t.Errorf("got %v, want %s", got, want)
	}
}

func parseConnectorSetting(t *testing.T, got string) (present, value bool) {
	t.Helper()
	var v struct {
		Disable *bool `json:"disableClaudeAiConnectors"`
	}
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("not valid JSON: %v (%q)", err, got)
	}
	if v.Disable == nil {
		return false, false
	}
	return true, *v.Disable
}

// Context floor: a profile that sets CLOWN_DISABLE_CLAUDEAI_CONNECTORS=1 turns
// the claude.ai connectors off through both knobs claude honors (the
// disableClaudeAiConnectors setting and ENABLE_CLAUDEAI_MCP_SERVERS=false),
// so the connectors' tool definitions never enter the context. Off by default.
func TestClaudeSafetySettingsJSON_ConnectorsOffOnlyWhenAsked(t *testing.T) {
	t.Setenv("ENABLE_CLAUDEAI_MCP_SERVERS", "")

	t.Setenv("CLOWN_DISABLE_CLAUDEAI_CONNECTORS", "")
	got := claudeSafetySettingsJSON("", "")
	if present, _ := parseConnectorSetting(t, got); present {
		t.Errorf("connector setting must be absent by default: %s", got)
	}
	if _, ok := parseSafetyEnv(t, got)["ENABLE_CLAUDEAI_MCP_SERVERS"]; ok {
		t.Errorf("connector env must be absent by default: %s", got)
	}

	t.Setenv("CLOWN_DISABLE_CLAUDEAI_CONNECTORS", "1")
	got = claudeSafetySettingsJSON("", "")
	if present, value := parseConnectorSetting(t, got); !present || !value {
		t.Errorf("disableClaudeAiConnectors must be true: %s", got)
	}
	if v := parseSafetyEnv(t, got)["ENABLE_CLAUDEAI_MCP_SERVERS"]; v != "false" {
		t.Errorf("ENABLE_CLAUDEAI_MCP_SERVERS = %q, want false: %s", v, got)
	}

	// An operator's explicit ENABLE_CLAUDEAI_MCP_SERVERS is not overridden.
	t.Setenv("ENABLE_CLAUDEAI_MCP_SERVERS", "true")
	if _, ok := parseSafetyEnv(t, claudeSafetySettingsJSON("", ""))["ENABLE_CLAUDEAI_MCP_SERVERS"]; ok {
		t.Error("an explicit ENABLE_CLAUDEAI_MCP_SERVERS must win over the profile default")
	}
}

// clown#289: hook processes are children of claude and inherit only claude's
// env (clown deliberately never exports CLOWN_SESSION_ID on its own env,
// clown#136), so the per-instance key rides claude's settings env block. It is
// the principal verbatim, never gated on a user override, and omitted when the
// identity is empty rather than injected as an empty override.
func TestClaudeSafetySettingsJSON_InjectsPrincipalForHooks(t *testing.T) {
	t.Setenv("CLAUDE_AFK_TIMEOUT_MS", "60000")
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "0")

	env := parseSafetyEnv(t, claudeSafetySettingsJSON("9eff073f-a924-48c2-b076-8b02d38f1938", ""))
	if got := env["CLOWN_SESSION_ID"]; got != "9eff073f-a924-48c2-b076-8b02d38f1938" {
		t.Errorf("CLOWN_SESSION_ID = %q, want the principal", got)
	}

	if env := parseSafetyEnv(t, claudeSafetySettingsJSON("", "")); len(env) != 0 {
		t.Errorf("empty principal must not inject an env block, got %v", env)
	}
}

// The context-floor guard's window: a `[1m]` model selects claude's 1M window,
// so hooks get CLOWN_CONTEXT_WINDOW=1000000; a bare model id implies no window
// (it could be 200K or 1M), so nothing is injected and the guard stays quiet;
// an operator's own CLOWN_CONTEXT_WINDOW (env, profile env, clownfile env) wins.
func TestClaudeSafetySettingsJSON_ContextWindowFromTheModelSuffix(t *testing.T) {
	t.Setenv("CLOWN_CONTEXT_WINDOW", "")

	for model, want := range map[string]string{
		"fable[1m]":              "1000000",
		"claude-sonnet-5-5[1M]":  "1000000",
		" opus[1m] ":             "1000000",
		"claude-sonnet-5-5":      "",
		"fable":                  "",
		"":                       "",
		"claude-sonnet-4-6[1mx]": "",
	} {
		got := parseSafetyEnv(t, claudeSafetySettingsJSON("", model))["CLOWN_CONTEXT_WINDOW"]
		if got != want {
			t.Errorf("model %q: CLOWN_CONTEXT_WINDOW = %q, want %q", model, got, want)
		}
	}

	t.Setenv("CLOWN_CONTEXT_WINDOW", "400000")
	if v, ok := parseSafetyEnv(t, claudeSafetySettingsJSON("", "fable[1m]"))["CLOWN_CONTEXT_WINDOW"]; ok {
		t.Errorf("an explicit CLOWN_CONTEXT_WINDOW must win, but settings injected %q", v)
	}
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

			present, val := parseRemoteControl(t, claudeSafetySettingsJSON("", ""))
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

	env := parseSafetyEnv(t, claudeSafetySettingsJSON("", ""))
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

	env := parseSafetyEnv(t, claudeSafetySettingsJSON("", ""))
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

	got := claudeSafetySettingsJSON("", "")
	if got == "" {
		t.Fatal("settings must never be empty (remoteControlAtStartup is always injected)")
	}
	if env := parseSafetyEnv(t, got); len(env) != 0 {
		t.Errorf("env block should be omitted when every env default is overridden, got %v", env)
	}
}
