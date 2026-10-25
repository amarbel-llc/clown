package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
// (No troupe assertion here: nothing on this path calls troupe, so one could
// not fail. TestRestartNeverRevokesTheIdentity covers idle to exec.)
func TestRelaunchPeeksAtTheSessionBeforeExec(t *testing.T) {
	for _, c := range []struct {
		name, body string
		exec       bool
		code       int
		log        string
	}{
		// The worktree is present in every case: a closed state wins over it.
		{"closed, worktree present", `echo '{"verdict":"none","reason":"tombstoned","when":"","state":"closed"}'`, false, 0, "closed"},
		{"closed but verdict not none", `echo '{"verdict":"restart","state":"closed"}'`, true, 1, "unknown"},
		{"closed without verdict", `echo '{"state":"closed"}'`, true, 1, "unknown"},
		{"live", `echo '{"verdict":"none","reason":"","when":"","state":"live"}'`, true, 1, "live"},
		{"non-zero (no --peek)", `echo 'unknown flag: --peek' >&2; exit 2`, true, 1, "unknown"},
		{"garbage", `echo 'not json'`, true, 1, "unknown"},
		{"missing state", `echo '{"verdict":"none","reason":"","when":""}'`, true, 1, "unknown"},
		{"other state", `echo '{"verdict":"none","state":"maybe"}'`, true, 1, "unknown"},
		{"timeout", "sleep 30 &\nwait", true, 1, "unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin, calls := fakePeekSpinclass(t, c.body)
			var execs []execCall
			r := testRelauncher(t, shortSocketDir(t), &execs)
			r.stderr = &bytes.Buffer{}
			r.logPath = filepath.Join(t.TempDir(), "close-self-run.log")
			r.spinclass = bin
			r.environ = os.Environ()
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
	r.logPath = filepath.Join(t.TempDir(), "close-self-run.log")
	r.relaunch(&relaunchPlan{principal: restartTestPrincipal, worktree: t.TempDir()})
	if len(execs) != 1 {
		t.Fatalf("execs = %d, want a relaunch", len(execs))
	}
	log, _ := os.ReadFile(r.logPath)
	if !strings.Contains(string(log), "spinclass is not on PATH") || !strings.Contains(string(log), "unknown") {
		t.Fatalf("log = %q, want the missing spinclass logged as unknown", log)
	}
}

// The peek runs with the launch environment (plus the principal), not the
// frontend's current one, which runWithFlags has added to.
func TestPeekUsesTheLaunchEnvironment(t *testing.T) {
	t.Setenv("CLOWN_PEEK_TEST_RUNTIME_ONLY", "leaked")
	envFile := filepath.Join(t.TempDir(), "env")
	bin, _ := fakePeekSpinclass(t, "env > '"+envFile+"'\necho '{\"verdict\":\"none\",\"state\":\"live\"}'")
	launch := []string{
		"PATH=" + os.Getenv("PATH"), "CLOWN_PEEK_TEST_LAUNCH=kept", "CLOWN_SESSION_ID=stale",
		// restartEnv drops these; so does the peek.
		"CLAUDE_SESSION_ID=claude-id", "CLOWN_RESTARTED_REASON=old", "CLOWN_PROFILE=p", "CLOWN_PROFILE_DIGEST=d", "CLOWN_PROFILE_SESSION=s",
	}
	if state, err := peekSessionState(bin, restartTestPrincipal, launch, 5*time.Second); err != nil || state != "live" {
		t.Fatalf("state %q, err %v", state, err)
	}
	b, _ := os.ReadFile(envFile)
	env := string(b)
	if !strings.Contains(env, "CLOWN_PEEK_TEST_LAUNCH=kept") || strings.Contains(env, "CLOWN_PEEK_TEST_RUNTIME_ONLY") {
		t.Fatalf("peek env = %q; want the launch env only", env)
	}
	for _, gone := range []string{"CLAUDE_SESSION_ID=", "CLOWN_RESTARTED_REASON=", "CLOWN_PROFILE=", "CLOWN_PROFILE_DIGEST=", "CLOWN_PROFILE_SESSION="} {
		if strings.Contains("\n"+env, "\n"+gone) {
			t.Errorf("peek env still has %s", gone)
		}
	}
	if n := strings.Count("\n"+env, "\nCLOWN_SESSION_ID="); n != 1 || !strings.Contains(env, "CLOWN_SESSION_ID="+restartTestPrincipal+"\n") {
		t.Fatalf("CLOWN_SESSION_ID entries = %d in %q; want exactly the principal", n, env)
	}
}

// A timed-out peek is killed with its whole process group, so a grandchild
// it left behind does not survive.
func TestPeekTimeoutKillsTheProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	killGrandchildAfter(t, pidFile)
	bin, _ := fakePeekSpinclass(t, "sleep 30 &\necho $! > '"+pidFile+"'\nwait")
	// A generous timeout so the fake has written its pid file under load.
	start := time.Now()
	if _, err := peekSessionState(bin, restartTestPrincipal, os.Environ(), 3*time.Second); err == nil {
		t.Fatal("want a timeout")
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Fatalf("took %v", took)
	}
	var pid string
	waitFor(t, "the pid file", func() bool {
		b, err := os.ReadFile(pidFile)
		pid = strings.TrimSpace(string(b))
		return err == nil && pid != ""
	})
	waitFor(t, "the grandchild to be gone", func() bool {
		stat, err := os.ReadFile("/proc/" + pid + "/stat")
		if err != nil {
			return true
		}
		// A zombie waiting for its reaper is gone too.
		fields := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
		return len(fields) > 0 && fields[0] == "Z"
	})
}

// The peek runs after the old serve socket is gone, never before.
func TestPeekRunsAfterTheServeSocketWait(t *testing.T) {
	runtime := shortSocketDir(t)
	ln := listenTestServeSocket(t, runtime)
	go func() {
		time.Sleep(300 * time.Millisecond)
		ln.Close() // unlinks the socket file before the fd closes
	}()
	// The fake records whether the old serve socket file still exists when
	// it runs: an event, not a clock.
	sock := filepath.Join(runtime, "clown", "serve", restartTestPrincipal, "spinclass.sock")
	seen := filepath.Join(t.TempDir(), "seen")
	bin, _ := fakePeekSpinclass(t, "if [ -e '"+sock+"' ]; then echo present > '"+seen+"'; else echo gone > '"+seen+"'; fi\necho '{\"verdict\":\"none\",\"state\":\"live\"}'")
	var execs []execCall
	r := testRelauncher(t, runtime, &execs)
	r.stderr = &bytes.Buffer{}
	r.spinclass = bin
	r.environ = os.Environ()
	r.relaunch(&relaunchPlan{principal: restartTestPrincipal, worktree: t.TempDir()})
	b, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("no peek: %v", err)
	}
	if got := strings.TrimSpace(string(b)); got != "gone" {
		t.Fatalf("the peek saw the serve socket %s; it must run after the wait", got)
	}
}
