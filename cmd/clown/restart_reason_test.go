package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// M4: only printable text survives: C0/C1 controls become spaces, bidi and
// zero-width format characters are dropped, and the length is capped.
func TestSanitizeRestartReason(t *testing.T) {
	if got := sanitizeRestartReason("‮evil​ x\u0085y⁦z"); got != "evil x yz" {
		t.Fatalf("got %q", got)
	}
	long := sanitizeRestartReason(strings.Repeat("é", 5000))
	if n := utf8.RuneCountInString(long); n != 300 || !strings.HasSuffix(long, "...") {
		t.Fatalf("not capped: %d runes", n)
	}
}

// M4: the cap and the filter apply when the request is queued.
func TestRestartControlSanitizesTheQueuedReason(t *testing.T) {
	q := &restartQueue{}
	reply := restartControlHandler("claude", "", q)(controlMessage{Op: "restart", When: "idle", Reason: "a‮" + strings.Repeat("x", 5000)})
	if !reply.OK {
		t.Fatalf("reply = %+v", reply)
	}
	got, _ := q.take()
	if strings.ContainsRune(got.Reason, '‮') || utf8.RuneCountInString(got.Reason) > 300 {
		t.Fatalf("queued reason not sanitized: %d runes", utf8.RuneCountInString(got.Reason))
	}
}

// H4: the relaunch carries spinclass's reason (the verdict), never the one the
// control socket brought; without one, a fixed string.
func TestRestartReasonComesFromTheVerdict(t *testing.T) {
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	for _, c := range []struct{ req, verdict, want string }{
		{"from the socket", "from spinclass", "from spinclass"},
		{"from the socket", "", restartFallbackReason},
	} {
		pendingRelaunch.Store(nil)
		providerRunning.Store(true)
		graceDone := make(chan struct{}, 1)
		fr := frontendRestarter{
			principal: restartTestPrincipal, stop: func() {}, logPath: filepath.Join(t.TempDir(), "l.log"),
			stopGrace: time.Millisecond, kill: func() error { return nil },
			graceChecked: func(bool) { graceDone <- struct{}{} },
		}
		fr.restart(restartRequest{When: "idle", Reason: c.req}, preflightVerdict{Verdict: "restart", Reason: c.verdict})
		<-graceDone
		if p := pendingRelaunch.Load(); p == nil || p.reason != c.want {
			t.Fatalf("req %q verdict %q: plan %+v, want reason %q", c.req, c.verdict, p, c.want)
		}
	}
}
