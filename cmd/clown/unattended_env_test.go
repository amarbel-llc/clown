package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/provider"
)

// The marker travels in the environment only (clown#323, FDR 0023). A
// restart keeps the launch environment, so a restarted unattended session
// stays unattended; an unwrapped relaunch's argv carries nothing of it (the
// [attach] inner's restart carries unattendedCarrierFlag; see
// TestAttachCarriesTheUnattendedPosture).
func TestRestartKeepsTheUnattendedMarker(t *testing.T) {
	if slices.Contains(restartEnvDropped, unattendedEnv) {
		t.Fatalf("restartEnvDropped drops %s; a restart would come back attended", unattendedEnv)
	}
	env := restartEnv([]string{"PATH=/bin", unattendedEnv + "=1"}, &relaunchPlan{principal: "p", reason: "r"})
	if !slices.Contains(env, unattendedEnv+"=1") {
		t.Fatalf("restartEnv = %q; want %s=1 kept", env, unattendedEnv)
	}
	t.Setenv(unattendedEnv, "1")
	f := parsedFlags{provider: "claude", identity: sessionIdentity{Key: "p"}, forwarded: []string{"--model", "opus"}}
	argv := strings.Join(restartArgv(f, ""), " ")
	if strings.Contains(argv, "dontAsk") || strings.Contains(argv, "permission-mode") || strings.Contains(argv, unattendedEnv) || strings.Contains(argv, unattendedCarrierFlag) {
		t.Fatalf("restartArgv = %q; the posture belongs to the environment, not the argv", argv)
	}
}

// Nothing of the posture is recorded: the session record and the resume
// argv carry no mode, and the settings follow the environment of the launch
// that builds them, so without the marker they carry no permissions key.
// (What claude then does is live-checked, FDR 0023 B5.)
func TestThePostureIsNotRecordedAndFollowsTheEnvironment(t *testing.T) {
	t.Setenv(unattendedEnv, "1")
	raw, err := json.Marshal(sessionRecordBase(otherID, parsedFlags{provider: "claude"}))
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.ToLower(string(raw)); strings.Contains(s, "dontask") || strings.Contains(s, "unattended") {
		t.Fatalf("session record %s carries the posture; it must stay env-only", raw)
	}
	if _, present := permissionsDefaultMode(t, claudeSafetySettingsJSON("", "")); !present {
		t.Fatal("with the marker: no permissions key")
	}
	t.Setenv(unattendedEnv, "")
	if mode, present := permissionsDefaultMode(t, claudeSafetySettingsJSON("", "")); present {
		t.Fatalf("marker dropped: defaultMode %q still set; want claude's default mode", mode)
	}
	// clown resume rebuilds its flags from the session's records, never from
	// a posture: its argv carries none.
	argv := strings.Join(resumeLaunchFlags(otherID, resumeArgs{}, "", "", "").reexecArgv(), " ")
	if strings.Contains(argv, "dontAsk") || strings.Contains(argv, "permission-mode") {
		t.Fatalf("resume argv %q carries a permission mode", argv)
	}
}

// Under an agent scope the frontend builds claude's argv, --settings
// included, and runs that argv in the unit: the posture reaches claude
// without the marker crossing into the unit's environment.
func TestUnattendedSettingsRideTheArgv(t *testing.T) {
	t.Setenv(unattendedEnv, "1")
	args, _, err := provider.BuildClaudeArgs(provider.ClaudeArgs{SettingsJSON: claudeSafetySettingsJSON("", "")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(args, "--settings")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("args %q carry no --settings", args)
	}
	if mode, _ := permissionsDefaultMode(t, args[i+1]); mode != "dontAsk" {
		t.Fatalf("--settings %s: defaultMode %q; want dontAsk", args[i+1], mode)
	}
}
