package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/buildcfg"
)

// spinclass#419: the relaunch marker is CLOWN_RESTARTED_REASON's presence,
// whatever its value; it is consumed.
func TestConsumeRestartedNoticeReportsTheRelaunch(t *testing.T) {
	t.Setenv(restartedReasonEnv, "")
	notice, relaunched := consumeRestartedNotice()
	if !relaunched || notice == "" {
		t.Fatalf("set but empty: notice %q, relaunched %v; want a notice and true", notice, relaunched)
	}
	if _, ok := os.LookupEnv(restartedReasonEnv); ok {
		t.Fatal("CLOWN_RESTARTED_REASON was not consumed")
	}
	if notice, relaunched := consumeRestartedNotice(); relaunched || notice != "" {
		t.Fatalf("unset: notice %q, relaunched %v; want neither", notice, relaunched)
	}
}

// spinclass#419: after the mint, a relaunched frontend peeks again. Closed:
// the provider is not started (closed() true) and nothing is revoked (no
// troupe verb at all). Live or unknown (a spinclass without --peek, a
// failure, none on PATH): it starts. Each outcome is logged.
func TestPostMintPeek(t *testing.T) {
	troupeLog := filepath.Join(t.TempDir(), "troupe.calls")
	troupe := filepath.Join(t.TempDir(), "troupe")
	if err := os.WriteFile(troupe, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+troupeLog+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := buildcfg.TroupePath
	buildcfg.TroupePath = troupe
	t.Cleanup(func() { buildcfg.TroupePath = old })

	for _, c := range []struct {
		name, body string
		noBin      bool
		closed     bool
		log        string
	}{
		{"closed", `echo '{"verdict":"none","reason":"closing","when":"","state":"closed"}'`, false, true, "closed after the mint; not starting"},
		{"live", `echo '{"verdict":"none","reason":"","when":"","state":"live"}'`, false, false, "live after the mint; starting"},
		{"no --peek", `echo 'unknown flag: --peek' >&2; exit 2`, false, false, "unknown after the mint"},
		{"garbage", `echo 'not json'`, false, false, "unknown after the mint"},
		{"not on PATH", ``, true, false, "not on PATH"},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin, calls := fakePeekSpinclass(t, c.body)
			if c.noBin {
				bin = ""
			}
			logPath := filepath.Join(t.TempDir(), "close-self-run.log")
			p := postMintPeek{spinclass: bin, principal: restartTestPrincipal, environ: os.Environ(), logPath: logPath, timeout: 5 * time.Second}
			if got := p.closed(); got != c.closed {
				t.Fatalf("closed() = %v, want %v", got, c.closed)
			}
			if !c.noBin {
				b, _ := os.ReadFile(calls)
				if want := "restart-preflight --peek|" + restartTestPrincipal + "\n"; string(b) != want {
					t.Fatalf("spinclass calls = %q, want %q", b, want)
				}
			}
			log, _ := os.ReadFile(logPath)
			if !strings.Contains(string(log), c.log) {
				t.Fatalf("log = %q, want %q", log, c.log)
			}
			if b, err := os.ReadFile(troupeLog); err == nil {
				t.Fatalf("the post-mint peek ran troupe: %q", b)
			}
		})
	}
}
