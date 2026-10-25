package agentscope

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeSystemdRun(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "systemd-run")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeSystemctl records each invocation's arguments, one line each.
func fakeSystemctl(t *testing.T) (bin, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	bin = filepath.Join(dir, "systemctl")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+calls+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, calls
}

// OnStarted fires once when systemd-run runs, with a kill that SIGKILLs the
// UNIT (not systemd-run): `systemctl --user kill --signal=SIGKILL <unit>`.
func TestOnStartedFiresOnceAndKillsTheUnit(t *testing.T) {
	systemctl, calls := fakeSystemctl(t)
	var kills []func()
	l := testLaunch()
	l.OnStarted = func(kill func()) {
		kills = append(kills, kill)
		kill()
	}
	tools := Tools{SystemdRun: fakeSystemdRun(t), Systemctl: systemctl, show: func(string) (Status, error) {
		return Status{ActiveState: "inactive"}, nil
	}}
	_, _ = Run(l, tools, nil)
	if len(kills) != 1 {
		t.Fatalf("OnStarted calls = %d, want one", len(kills))
	}
	b, _ := os.ReadFile(calls)
	if !strings.Contains(string(b), "--user kill --signal=SIGKILL "+l.Unit) {
		t.Fatalf("systemctl calls = %q, want a SIGKILL of the unit", b)
	}

	// After Run returned, the kill is a no-op: the unit is gone or another
	// launch's.
	before := string(b)
	kills[0]()
	after, _ := os.ReadFile(calls)
	if strings.Count(string(after), "kill --signal=SIGKILL") != strings.Count(before, "kill --signal=SIGKILL") {
		t.Fatalf("a kill after Run returned reached systemctl: %q", after)
	}
}

func TestOnStartedDoesNotFireOnAFailedLaunch(t *testing.T) {
	calls := 0
	refused := testLaunch()
	refused.EgressSocket = "" // SystemdRunArgs refuses
	refused.OnStarted = func(func()) { calls++ }
	tools := Tools{SystemdRun: fakeSystemdRun(t), Systemctl: "/nonexistent/systemctl"}
	if _, err := Run(refused, tools, nil); err == nil {
		t.Fatal("want a refusal")
	}
	missing := testLaunch()
	missing.OnStarted = func(func()) { calls++ }
	if _, err := Run(missing, Tools{SystemdRun: "/nonexistent/systemd-run", Systemctl: "/nonexistent/systemctl"}, nil); err == nil {
		t.Fatal("want a start failure")
	}
	if calls != 0 {
		t.Fatalf("OnStarted fired %d times on failed launches", calls)
	}
}
