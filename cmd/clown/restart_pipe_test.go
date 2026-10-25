package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// M1: a preflight that leaves a grandchild holding its stdout must not stall
// the serial idle goroutine: the verdict is read once the preflight exits.
func TestPreflightDoesNotWaitForAGrandchildHoldingStdout(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "spinclass")
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"restart-preflight) sleep 20 & echo '{\"verdict\":\"none\",\"reason\":\"r\",\"when\":\"\"}' ;;\n" +
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
	start := time.Now()
	out, code, err := runForCountdown([]string{"/bin/sh", "-c", "sleep 20 & echo '{\"modal\":\"ok\"}'"})
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %v", took)
	}
	if err != nil || code != 0 || !poshProbeHasModal(out) {
		t.Fatalf("out %q, code %d, err %v", out, code, err)
	}
}
