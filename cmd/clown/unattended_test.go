package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// permissionsDefaultMode returns the permissions.defaultMode the settings JSON
// carries, and whether a permissions key is present at all.
func permissionsDefaultMode(t *testing.T, settings string) (string, bool) {
	t.Helper()
	var parsed struct {
		Permissions *struct {
			DefaultMode string `json:"defaultMode"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(settings), &parsed); err != nil {
		t.Fatalf("settings %q: %v", settings, err)
	}
	if parsed.Permissions == nil {
		return "", false
	}
	return parsed.Permissions.DefaultMode, true
}

// CLOWN_UNATTENDED=1 puts claude in dontAsk through --settings (clown#323);
// without it clown sets no permissions key at all.
func TestClaudeSafetySettingsJSON_Unattended(t *testing.T) {
	t.Setenv(unattendedEnv, "")
	if mode, present := permissionsDefaultMode(t, claudeSafetySettingsJSON("", "")); present {
		t.Fatalf("no marker: permissions present (defaultMode %q); want none", mode)
	}
	t.Setenv(unattendedEnv, "1")
	if mode, present := permissionsDefaultMode(t, claudeSafetySettingsJSON("", "")); !present || mode != "dontAsk" {
		t.Fatalf("CLOWN_UNATTENDED=1: defaultMode %q (present %v); want dontAsk", mode, present)
	}
}

// The marker's value is exactly "1"; unset or empty is off, and anything
// else refuses rather than guessing.
func TestUnattendedRequested(t *testing.T) {
	for value, want := range map[string]bool{"": false, "1": true} {
		if got, err := unattendedRequested(value); err != nil || got != want {
			t.Errorf("unattendedRequested(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	for _, value := range []string{"0", "true", "yes", " 1", "1 ", "01", "on"} {
		if _, err := unattendedRequested(value); err == nil || !strings.Contains(err.Error(), unattendedEnv) {
			t.Errorf("unattendedRequested(%q): err %v; want a refusal naming %s", value, err, unattendedEnv)
		}
	}
}

// What the marker refuses: another provider, --naked, a different
// permission mode, the bypass flags, and the flags that bring their own
// settings. --permission-mode dontAsk (spinclass's splice) agrees with it.
func TestUnattendedLaunchError(t *testing.T) {
	claude := func(fwd ...string) parsedFlags { return parsedFlags{provider: "claude", forwarded: fwd} }
	allowed := map[string]parsedFlags{
		"plain claude":               claude(),
		"spinclass's splice":         claude("--permission-mode", "dontAsk"),
		"spinclass's splice, = form": claude("--permission-mode=dontAsk"),
		"inherited dontAsk":          claude("--inherit-permission-mode", "dontAsk"),
		"a resume":                   claude("--resume", "11111111-2222-3333-4444-555555555555"),
	}
	for name, f := range allowed {
		if err := unattendedLaunchError(f, "1"); err != nil {
			t.Errorf("%s: %v; want it allowed", name, err)
		}
	}
	refused := map[string]parsedFlags{
		"codex":                                 {provider: "codex"},
		"juggler":                               {provider: "juggler"},
		"opencode":                              {provider: "opencode"},
		"crush":                                 {provider: "crush"},
		"--naked":                               {provider: "claude", naked: true},
		"--permission-mode default":             claude("--permission-mode", "default"),
		"--permission-mode=bypassPermissions":   claude("--permission-mode=bypassPermissions"),
		"--permission-mode with no value":       claude("--permission-mode"),
		"dontAsk then another mode":             claude("--permission-mode", "dontAsk", "--permission-mode", "auto"),
		"--inherit-permission-mode acceptEdits": claude("--inherit-permission-mode", "acceptEdits"),
		"--dangerously-skip-permissions":        claude("--dangerously-skip-permissions"),
		"--allow-dangerously-skip-permissions":  claude("--allow-dangerously-skip-permissions"),
		"a forwarded --settings":                claude("--settings", `{"permissions":{"defaultMode":"default"}}`),
		"a forwarded --settings=":               claude("--settings=/tmp/s.json"),
		"--setting-sources":                     claude("--setting-sources", "user"),
	}
	for name, f := range refused {
		if err := unattendedLaunchError(f, "1"); err == nil {
			t.Errorf("%s: allowed; want a refusal", name)
		}
	}
	// Without the marker none of this is clown's business.
	for name, f := range refused {
		if err := unattendedLaunchError(f, ""); err != nil {
			t.Errorf("%s without the marker: %v; want it allowed", name, err)
		}
	}
	if err := unattendedLaunchError(claude(), "yes"); err == nil {
		t.Error("CLOWN_UNATTENDED=yes: allowed; want a refusal")
	}
}

// runWithFlags runs the unattended gate before anything starts: a bad
// marker refuses a --print-launch-plan launch that would otherwise succeed.
func TestRunWithFlagsRunsTheUnattendedGate(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv(unattendedEnv, "yes")
	called := false
	orig := unattendedGate
	unattendedGate = func(f *parsedFlags) error {
		called = true
		return orig(f)
	}
	t.Cleanup(func() { unattendedGate = orig })
	flags := parsedFlags{provider: "claude", providerExplicit: true, printLaunchPlan: true, noAttach: true}
	if code := runWithFlags(flags); code != 1 || !called {
		t.Fatalf("runWithFlags with CLOWN_UNATTENDED=yes = %d, gate called %v; want the gate to refuse (1)", code, called)
	}
}
