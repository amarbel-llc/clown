package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The unit is launched by the frontend, which reads the unattended marker
// (unattendedValue: the environment, or the [attach] carrier) and gives the
// launch its wall clock; the marker itself never enters the unit (clown#320).
func TestPrepareAgentScopeReadsTheUnattendedMarker(t *testing.T) {
	dir := t.TempDir()
	fields := specFields(dir)
	if err := os.MkdirAll(filepath.Join(dir, "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := filepath.Join(dir, "spec.json")
	writeSpec(t, spec, fields)
	systemctl := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(systemctl, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOWN_SYSTEMCTL", systemctl)
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_LOG_HOME", filepath.Join(dir, "log"))
	t.Setenv("TROUPE_XMPP_PASSWORD_FILE", "")
	t.Chdir(filepath.Join(dir, "wt"))
	withAttach(t, "", false)
	for _, marker := range []string{"", "1"} {
		t.Setenv(unattendedEnv, marker)
		launch, err := prepareAgentScope(spec, "", "u-1.service", []string{"claude"}, nil)
		if err != nil {
			t.Fatalf("marker %q: %v", marker, err)
		}
		if launch.Unattended != (marker == "1") {
			t.Errorf("marker %q: Unattended %v", marker, launch.Unattended)
		}
	}
	// Carried across the [attach] wrap with the environment stripped.
	t.Setenv(unattendedEnv, "")
	withAttach(t, "k", true)
	if launch, err := prepareAgentScope(spec, "", "u-1.service", []string{"claude"}, nil); err != nil || !launch.Unattended {
		t.Fatalf("carried: Unattended %v, %v; want true", launch.Unattended, err)
	}
}
