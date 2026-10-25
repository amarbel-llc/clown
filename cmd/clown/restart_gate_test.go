package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/sessions"
)

func recordedPin(pin *sessions.ProfilePin) profileLookup {
	return func(id string) (sessions.ProfilePin, bool, error) {
		if pin == nil || id != restartTestPrincipal {
			return sessions.ProfilePin{}, false, nil
		}
		return *pin, true, nil
	}
}

func testRestartProfileCheck(profileName string, now *sessions.ProfilePin, nowErr error, recorded *sessions.ProfilePin) restartProfileCheck {
	f := parsedFlags{provider: "claude", profile: profileName, forwarded: []string{"--resume", restartTestPrincipal}}
	f.identity.Key = restartTestPrincipal
	return restartProfileCheck{
		relaunch: f,
		pinNow: func(name string) (*sessions.ProfilePin, error) {
			if nowErr != nil {
				return nil, nowErr
			}
			return now, nil
		},
		recorded: recordedPin(recorded),
	}
}

// The relaunch's profile gate, run now: the same pin passes; a changed digest,
// a profile that no longer resolves, or dropping the pinned profile refuses.
func TestRestartProfileCheck(t *testing.T) {
	pin := &sessions.ProfilePin{Name: "worker", Digest: "d1"}
	if err := testRestartProfileCheck("worker", pin, nil, pin).check(); err != nil {
		t.Fatalf("unchanged profile refused: %v", err)
	}
	if err := testRestartProfileCheck("", nil, nil, nil).check(); err != nil {
		t.Fatalf("no profile, no pin refused: %v", err)
	}
	for name, c := range map[string]restartProfileCheck{
		"changed digest":  testRestartProfileCheck("worker", &sessions.ProfilePin{Name: "worker", Digest: "d2"}, nil, pin),
		"unresolvable":    testRestartProfileCheck("worker", nil, errors.New("unknown profile \"worker\""), pin),
		"pinned, dropped": testRestartProfileCheck("", nil, nil, pin),
		"unpinnable":      testRestartProfileCheck("worker", &sessions.ProfilePin{Name: "worker", Digest: sessions.UnpinnableDigest}, nil, &sessions.ProfilePin{Name: "worker", Digest: sessions.UnpinnableDigest}),
	} {
		if err := c.check(); err == nil {
			t.Errorf("%s: the relaunch would refuse, the dry run must too", name)
		}
	}
}

// H1: a relaunch the profile gate would refuse must not stop the provider:
// the session keeps running and the refusal is logged.
func TestRestartRefusedByTheProfileGateLeavesTheProviderRunning(t *testing.T) {
	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	logPath := filepath.Join(t.TempDir(), "l.log")
	stops, countdowns := 0, 0
	check := testRestartProfileCheck("worker", &sessions.ProfilePin{Name: "worker", Digest: "d2"}, nil, &sessions.ProfilePin{Name: "worker", Digest: "d1"})
	fr := frontendRestarter{
		principal:    restartTestPrincipal,
		stop:         func() { stops++ },
		countdown:    func(string) (bool, string) { countdowns++; return true, "" },
		profileCheck: check.check,
		logPath:      logPath,
	}
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", Reason: "r"})
	if stops != 0 || countdowns != 0 || pendingRelaunch.Load() != nil {
		t.Fatalf("stops %d, countdowns %d, plan %v; want the session left running", stops, countdowns, pendingRelaunch.Load())
	}
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "profile") {
		t.Fatalf("the refusal must be logged:\n%s", b)
	}
}
