package agentscope

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	var kills []func() error
	l := testLaunch()
	l.OnStarted = func(kill func() error) {
		kills = append(kills, kill)
		_ = kill()
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
	_ = kills[0]()
	after, _ := os.ReadFile(calls)
	if strings.Count(string(after), "kill --signal=SIGKILL") != strings.Count(before, "kill --signal=SIGKILL") {
		t.Fatalf("a kill after Run returned reached systemctl: %q", after)
	}
}

// S2: a hanging `systemctl kill` is bounded: the kill returns an error and
// Run is not held up.
func TestAHangingUnitKillIsBounded(t *testing.T) {
	old := systemctlKillTimeout
	systemctlKillTimeout = 200 * time.Millisecond
	t.Cleanup(func() { systemctlKillTimeout = old })
	dir := t.TempDir()
	hang := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(hang, []byte("#!/bin/sh\ncase \"$*\" in *kill*) exec sleep 30 ;; esac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var killErr error
	took := time.Duration(0)
	l := testLaunch()
	l.OnStarted = func(kill func() error) {
		start := time.Now()
		killErr = kill()
		took = time.Since(start)
	}
	tools := Tools{SystemdRun: fakeSystemdRun(t), Systemctl: hang, show: func(string) (Status, error) {
		return Status{ActiveState: "inactive"}, nil
	}}
	done := make(chan struct{})
	go func() { _, _ = Run(l, tools, nil); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run was held up by a hanging systemctl kill")
	}
	if killErr == nil || took > 5*time.Second {
		t.Fatalf("kill = %v after %v, want a bounded error", killErr, took)
	}
}

func TestOnStartedDoesNotFireOnAFailedLaunch(t *testing.T) {
	calls := 0
	refused := testLaunch()
	refused.EgressSocket = "" // SystemdRunArgs refuses
	refused.OnStarted = func(func() error) { calls++ }
	tools := Tools{SystemdRun: fakeSystemdRun(t), Systemctl: "/nonexistent/systemctl"}
	if _, err := Run(refused, tools, nil); err == nil {
		t.Fatal("want a refusal")
	}
	missing := testLaunch()
	missing.OnStarted = func(func() error) { calls++ }
	if _, err := Run(missing, Tools{SystemdRun: "/nonexistent/systemd-run", Systemctl: "/nonexistent/systemctl"}, nil); err == nil {
		t.Fatal("want a start failure")
	}
	if calls != 0 {
		t.Fatalf("OnStarted fired %d times on failed launches", calls)
	}
}
