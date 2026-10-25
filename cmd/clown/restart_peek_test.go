package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/buildcfg"
)

// fakePeekSpinclass is a `spinclass` whose `restart-preflight --peek` runs
// body (a shell fragment); every invocation's args and CLOWN_SESSION_ID are
// appended to calls.
func fakePeekSpinclass(t *testing.T, body string) (bin, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	bin = filepath.Join(dir, "spinclass")
	script := "#!/bin/sh\nprintf '%s|%s\\n' \"$*\" \"$CLOWN_SESSION_ID\" >> '" + calls + "'\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, calls
}

// spinclass#419 --peek: right before the exec, a session spinclass reports
// closed is not relaunched; live relaunches; anything else is unknown and
// relaunches (a deployed spinclass without --peek exits non-zero).
func TestRelaunchPeeksAtTheSessionBeforeExec(t *testing.T) {
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
		exec       bool
		code       int
		log        string
	}{
		{"closed", `echo '{"verdict":"none","reason":"tombstoned","when":"","state":"closed"}'`, false, 0, "closed"},
		{"live", `echo '{"verdict":"none","reason":"","when":"","state":"live"}'`, true, 1, "live"},
		{"non-zero (no --peek)", `echo 'unknown flag: --peek' >&2; exit 2`, true, 1, "unknown"},
		{"garbage", `echo 'not json'`, true, 1, "unknown"},
		{"missing state", `echo '{"verdict":"none","reason":"","when":""}'`, true, 1, "unknown"},
		{"other state", `echo '{"verdict":"none","state":"maybe"}'`, true, 1, "unknown"},
		{"timeout", `exec sleep 30`, true, 1, "unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin, calls := fakePeekSpinclass(t, c.body)
			var execs []execCall
			r := testRelauncher(t, shortSocketDir(t), &execs)
			r.stderr = &bytes.Buffer{}
			r.logPath = filepath.Join(t.TempDir(), "close-self-run.log")
			r.spinclass = bin
			r.peekTimeout = 300 * time.Millisecond
			start := time.Now()
			code := r.relaunch(&relaunchPlan{principal: restartTestPrincipal, worktree: t.TempDir()})
			if took := time.Since(start); took > 5*time.Second {
				t.Fatalf("the peek was not bounded: %v", took)
			}
			if got := len(execs) == 1; got != c.exec {
				t.Fatalf("exec called = %v, want %v", got, c.exec)
			}
			if code != c.code {
				t.Fatalf("code = %d, want %d", code, c.code)
			}
			b, _ := os.ReadFile(calls)
			if want := "restart-preflight --peek|" + restartTestPrincipal; !strings.Contains(string(b), want) {
				t.Fatalf("spinclass calls = %q, want %q", b, want)
			}
			log, _ := os.ReadFile(r.logPath)
			if !strings.Contains(string(log), c.log) {
				t.Fatalf("log = %q, want %q", log, c.log)
			}
		})
	}
	if b, err := os.ReadFile(troupeLog); err == nil {
		t.Fatalf("the relaunch ran troupe: %q", b)
	}
}

// Without spinclass on PATH the peek is unknown, and the worktree check
// still decides.
func TestRelaunchWithoutSpinclassFallsBackToTheWorktreeCheck(t *testing.T) {
	var execs []execCall
	r := testRelauncher(t, shortSocketDir(t), &execs)
	r.stderr = &bytes.Buffer{}
	r.spinclass = ""
	gone := filepath.Join(t.TempDir(), "gone")
	if code := r.relaunch(&relaunchPlan{principal: restartTestPrincipal, worktree: gone}); code != 0 || len(execs) != 0 {
		t.Fatalf("code %d, execs %d; the worktree check must still skip a closed session", code, len(execs))
	}
	r.relaunch(&relaunchPlan{principal: restartTestPrincipal, worktree: t.TempDir()})
	if len(execs) != 1 {
		t.Fatalf("execs = %d, want a relaunch", len(execs))
	}
}
