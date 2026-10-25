package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeIdleSpinclass is a `spinclass` that records every invocation (verb and
// CLOWN_SESSION_ID, one line each, in order) and answers restart-preflight with
// the given stdout and exit code.
func fakeIdleSpinclass(t *testing.T, preflightOut, preflightExit string) (bin, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	out := filepath.Join(dir, "preflight.out")
	if err := os.WriteFile(out, []byte(preflightOut), 0o600); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "spinclass")
	script := "#!/bin/sh\nprintf '%s|%s\\n' \"$1\" \"$CLOWN_SESSION_ID\" >> '" + calls + "'\n" +
		"case \"$1\" in\n" +
		"restart-preflight) cat '" + out + "'; exit " + preflightExit + " ;;\n" +
		"*) exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, calls
}

func readCalls(t *testing.T, calls string) []string {
	t.Helper()
	b, err := os.ReadFile(calls)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

type recordedRestarts struct {
	n    atomic.Int32
	last atomic.Pointer[restartRequest]
}

func newTestIdleRunner(t *testing.T, bin string, q *restartQueue, rec *recordedRestarts) *idleRunner {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "close-self-run.log")
	return &idleRunner{
		closer:    &selfCloseRunner{bin: bin, principal: restartTestPrincipal, logPath: logPath},
		preflight: &restartPreflight{bin: bin, principal: restartTestPrincipal, logPath: logPath},
		queue:     q,
		restart: func(r restartRequest, _ preflightVerdict) {
			rec.n.Add(1)
			rec.last.Store(&r)
		},
		logPath: logPath,
	}
}

// Without a pending restart, an idle transition runs close-self-run only.
func TestIdleWithoutAPendingRestartNeverRunsPreflight(t *testing.T) {
	bin, calls := fakeIdleSpinclass(t, `{"verdict":"restart","reason":"x","when":"idle"}`+"\n", "0")
	var rec recordedRestarts
	r := newTestIdleRunner(t, bin, &restartQueue{}, &rec)
	r.onIdle()
	if got := readCalls(t, calls); len(got) != 1 || got[0] != "close-self-run|"+restartTestPrincipal {
		t.Fatalf("calls = %q, want close-self-run only", got)
	}
	if rec.n.Load() != 0 {
		t.Fatal("restarted without a pending request")
	}
}

// Verdict restart: close-self-run first, then the preflight with the
// principal, then exactly one restart; the next idle does not restart again.
func TestIdleVerdictRestartRestartsOnceAndNotAgain(t *testing.T) {
	bin, calls := fakeIdleSpinclass(t, `{"verdict":"restart","reason":"pick up new tools","when":"idle"}`+"\n", "0")
	q := &restartQueue{}
	q.put(restartRequest{When: "idle", Reason: "pick up new tools"})
	var rec recordedRestarts
	r := newTestIdleRunner(t, bin, q, &rec)

	r.onIdle()
	want := []string{"close-self-run|" + restartTestPrincipal, "restart-preflight|" + restartTestPrincipal}
	if got := readCalls(t, calls); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %q, want %q (fixed order, principal in CLOWN_SESSION_ID)", got, want)
	}
	if rec.n.Load() != 1 || rec.last.Load().Reason != "pick up new tools" {
		t.Fatalf("restarts = %d (%+v), want one with the queued reason", rec.n.Load(), rec.last.Load())
	}

	r.onIdle()
	if rec.n.Load() != 1 {
		t.Fatalf("a second idle restarted again: %d", rec.n.Load())
	}
	if got := readCalls(t, calls); len(got) != 3 || got[2] != "close-self-run|"+restartTestPrincipal {
		t.Fatalf("second idle calls = %q, want only another close-self-run", got)
	}
}

// Anything but a well-formed "restart" verdict does nothing, and the pending
// request is gone either way (preflight consumed or refused it).
func TestIdleNonRestartVerdictsDoNothing(t *testing.T) {
	for _, c := range []struct{ name, out, exit string }{
		{"none", `{"verdict":"none","reason":"no request","when":""}` + "\n", "0"},
		{"refused", `{"verdict":"refused","reason":"a merge job is running","when":"idle"}` + "\n", "0"},
		{"non-zero exit", `{"verdict":"restart","reason":"x","when":"idle"}` + "\n", "1"},
		{"garbage", "not json at all\n", "0"},
		{"empty", "", "0"},
		{"unknown verdict", `{"verdict":"maybe","reason":"x","when":"idle"}` + "\n", "0"},
		{"two lines", `{"verdict":"restart","reason":"x","when":"idle"}` + "\n" + `{"verdict":"restart"}` + "\n", "0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin, calls := fakeIdleSpinclass(t, c.out, c.exit)
			q := &restartQueue{}
			q.put(restartRequest{When: "idle", Reason: "x"})
			var rec recordedRestarts
			r := newTestIdleRunner(t, bin, q, &rec)
			r.onIdle()
			if rec.n.Load() != 0 {
				t.Fatal("must not restart")
			}
			if got := readCalls(t, calls); len(got) != 2 {
				t.Fatalf("calls = %q, want close-self-run then one preflight", got)
			}
			if _, ok := q.take(); ok {
				t.Fatal("the pending restart must be dropped")
			}
			r.onIdle()
			if got := readCalls(t, calls); len(got) != 3 {
				t.Fatalf("a dropped restart must not be preflighted again: %q", got)
			}
			log, _ := os.ReadFile(r.logPath)
			if !strings.Contains(string(log), "restart-preflight") {
				t.Errorf("the decision must be logged:\n%s", log)
			}
		})
	}
}

// A close that lands first leaves spinclass nothing to restart (preflight says
// none): the pending restart is dropped, never performed.
func TestIdleCloseThenPendingRestartDropsTheRestart(t *testing.T) {
	dir := t.TempDir()
	closed := filepath.Join(dir, "closed")
	calls := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "spinclass")
	script := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> '" + calls + "'\n" +
		"case \"$1\" in\n" +
		"close-self-run) : > '" + closed + "' ;;\n" +
		"restart-preflight) if [ -e '" + closed + "' ]; then echo '{\"verdict\":\"none\",\"reason\":\"session closed\",\"when\":\"\"}'; else echo '{\"verdict\":\"restart\",\"reason\":\"x\",\"when\":\"idle\"}'; fi ;;\n" +
		"esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	q := &restartQueue{}
	q.put(restartRequest{When: "idle", Reason: "x"})
	var rec recordedRestarts
	r := newTestIdleRunner(t, bin, q, &rec)
	r.onIdle()
	if rec.n.Load() != 0 {
		t.Fatal("a closed session must not restart")
	}
	if _, ok := q.take(); ok {
		t.Fatal("the pending restart must be dropped after the close")
	}
	if got := readCalls(t, calls); strings.Join(got, ",") != "close-self-run,restart-preflight" {
		t.Fatalf("calls = %q", got)
	}
}

func TestParsePreflightVerdict(t *testing.T) {
	v, err := parsePreflightVerdict([]byte(`{"verdict":"restart","reason":"r","when":"idle"}` + "\n"))
	if err != nil || v != (preflightVerdict{Verdict: "restart", Reason: "r", When: "idle"}) {
		t.Fatalf("v = %+v, err = %v", v, err)
	}
	for _, bad := range []string{"", "\n", "{", `{"verdict":"go"}`, `{"verdict":"restart"}` + "\n" + `{}`, `[1]`} {
		if _, err := parsePreflightVerdict([]byte(bad)); err == nil {
			t.Errorf("parsePreflightVerdict(%q) accepted", bad)
		}
	}
}
