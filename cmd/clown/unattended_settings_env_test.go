package main

import (
	"encoding/json"
	"testing"
)

// The TaskStop rule in clown-hook-allow applies only to unattended sessions, and
// the hook learns that from its environment, which is claude's, which clown
// builds through --settings. An unattended launch therefore carries the marker
// in the settings env block; an attended one carries none, and neither can
// inherit one from the operator's shell by accident.
func TestUnattendedMarkerReachesHooksThroughTheSettingsEnv(t *testing.T) {
	envOf := func(t *testing.T) map[string]string {
		t.Helper()
		var v struct {
			Env map[string]string `json:"env"`
		}
		if err := json.Unmarshal([]byte(claudeSafetySettingsJSON("k1", "")), &v); err != nil {
			t.Fatal(err)
		}
		return v.Env
	}

	t.Setenv(unattendedEnv, "1")
	if got := envOf(t)[unattendedEnv]; got != "1" {
		t.Errorf("unattended: settings env %s = %q, want 1", unattendedEnv, got)
	}

	t.Setenv(unattendedEnv, "")
	if got, ok := envOf(t)[unattendedEnv]; ok {
		t.Errorf("attended: settings env carries %s=%q", unattendedEnv, got)
	}

	// The [attach] inner may lack the variable and learn the posture from the
	// carrier flag: it counts.
	old := unattendedCarried
	unattendedCarried = true
	t.Cleanup(func() { unattendedCarried = old })
	if got := envOf(t)[unattendedEnv]; got != "1" {
		t.Errorf("carried: settings env %s = %q, want 1", unattendedEnv, got)
	}
}
