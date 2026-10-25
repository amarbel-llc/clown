package agentscope

import (
	"os"
	"path/filepath"
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

// OnStarted fires once, with systemd-run's pid, when systemd-run runs (and a
// SIGTERM to the frontend reaches it), and never on a launch that fails before.
func TestOnStartedFiresOnceWithThePid(t *testing.T) {
	var pids []int
	l := testLaunch()
	l.OnStarted = func(pid int) { pids = append(pids, pid) }
	tools := Tools{SystemdRun: fakeSystemdRun(t), Systemctl: "/nonexistent/systemctl", show: func(string) (Status, error) {
		return Status{ActiveState: "inactive"}, nil
	}}
	_, _ = Run(l, tools, nil)
	if len(pids) != 1 || pids[0] <= 0 {
		t.Fatalf("OnStarted calls = %v, want one with a pid", pids)
	}
}

func TestOnStartedDoesNotFireOnAFailedLaunch(t *testing.T) {
	calls := 0
	refused := testLaunch()
	refused.EgressSocket = "" // SystemdRunArgs refuses
	refused.OnStarted = func(int) { calls++ }
	tools := Tools{SystemdRun: fakeSystemdRun(t), Systemctl: "/nonexistent/systemctl"}
	if _, err := Run(refused, tools, nil); err == nil {
		t.Fatal("want a refusal")
	}
	missing := testLaunch()
	missing.OnStarted = func(int) { calls++ }
	if _, err := Run(missing, Tools{SystemdRun: "/nonexistent/systemd-run", Systemctl: "/nonexistent/systemctl"}, nil); err == nil {
		t.Fatal("want a start failure")
	}
	if calls != 0 {
		t.Fatalf("OnStarted fired %d times on failed launches", calls)
	}
}
