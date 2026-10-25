package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// killGrandchildAfter kills the process whose pid the test's script wrote to
// pidFile, so the grandchild does not outlive the test.
func killGrandchildAfter(t *testing.T, pidFile string) {
	t.Cleanup(func() {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 0 {
			return
		}
		// Only while that pid is still the test's sleep: a process already
		// gone may have had its pid reused.
		cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		if err != nil || !strings.HasPrefix(filepath.Base(strings.SplitN(string(cmdline), "\x00", 2)[0]), "sleep") {
			return
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	})
}

// M1: a preflight that leaves a grandchild holding its stdout must not stall
// the serial idle goroutine: the verdict is read once the preflight exits.
func TestPreflightDoesNotWaitForAGrandchildHoldingStdout(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "spinclass")
	pidFile := filepath.Join(dir, "grandchild.pid")
	killGrandchildAfter(t, pidFile)
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"restart-preflight) sleep 20 & echo $! > '" + pidFile + "'; echo '{\"verdict\":\"none\",\"reason\":\"r\",\"when\":\"\"}' ;;\n" +
		"esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &restartPreflight{bin: bin, principal: restartTestPrincipal, logPath: filepath.Join(dir, "l.log")}
	start := time.Now()
	v, err := p.run()
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the preflight took %v: it waited for the grandchild", took)
	}
	if err != nil || v.Verdict != "none" {
		t.Fatalf("verdict %+v, err %v", v, err)
	}
}

// M1: the same for the countdown's posh calls.
func TestCountdownRunDoesNotWaitForAGrandchildHoldingStdout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	killGrandchildAfter(t, pidFile)
	start := time.Now()
	out, code, err := runForCountdown([]string{"/bin/sh", "-c", "sleep 20 & echo $! > '" + pidFile + "'; echo '{\"modal\":\"ok\"}'"})
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %v", took)
	}
	if err != nil || code != 0 || !poshProbeHasModal(out) {
		t.Fatalf("out %q, code %d, err %v", out, code, err)
	}
}
