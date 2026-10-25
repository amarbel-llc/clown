package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F4: a restart dropped after the preflight consumed the request says so, on
// the frontend's stderr and in the log, and that the requester must ask again.
func TestACancelledRestartIsReported(t *testing.T) {
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	for name, fr := range map[string]frontendRestarter{
		"countdown cancelled": {countdown: func(string) (bool, string) { return false, "posh modal: cancelled by a viewer" }},
		"turn started":        {busy: func() bool { return true }},
		"profile refused":     {profileCheck: func() error { return errors.New("profile changed") }},
	} {
		t.Run(name, func(t *testing.T) {
			pendingRelaunch.Store(nil)
			providerRunning.Store(true)
			var notice bytes.Buffer
			fr.principal, fr.stop, fr.notify = restartTestPrincipal, func() { t.Fatal("stopped") }, &notice
			fr.logPath = filepath.Join(t.TempDir(), "l.log")
			fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", Reason: "r"})
			log, _ := os.ReadFile(fr.logPath)
			for what, s := range map[string]string{"stderr": notice.String(), "log": string(log)} {
				if !strings.Contains(s, "cancelled") || !strings.Contains(s, "ask again") {
					t.Errorf("%s = %q, want a cancel naming that the requester must ask again", what, s)
				}
			}
			if strings.Count(notice.String(), "\n") > 2 {
				t.Errorf("stderr notice must be one line: %q", notice.String())
			}
		})
	}
}

// F6: spinclass's reason is sanitized before it reaches the log, so an
// embedded newline cannot forge a log line.
func TestPreflightReasonIsSanitizedInTheLog(t *testing.T) {
	for _, verdict := range []string{"none", "restart"} {
		bin, _ := fakeIdleSpinclass(t, `{"verdict":"`+verdict+`","reason":"x\n2026-01-01T00:00:00Z forged line","when":"idle"}`+"\n", "0")
		q := &restartQueue{}
		q.put(restartRequest{When: "idle"})
		var rec recordedRestarts
		r := newTestIdleRunner(t, bin, q, &rec)
		r.onIdle()
		log, _ := os.ReadFile(r.logPath)
		if strings.Contains(string(log), "\n2026-01-01T00:00:00Z forged line") {
			t.Fatalf("verdict %s: a newline in the reason reached the log:\n%s", verdict, log)
		}
	}
}

// F5: the idle-notify log never follows a symlink planted at its fixed path.
func TestIdleNotifyLogDoesNotFollowSymlinks(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(tmp, "clown-idle-notify.log")); err != nil {
		t.Fatal(err)
	}
	runIdleNotify([]string{filepath.Join(shortSocketDir(t), "absent.sock")})
	if b, _ := os.ReadFile(target); len(b) != 0 {
		t.Fatalf("idle-notify wrote through a symlink: %q", b)
	}
}
