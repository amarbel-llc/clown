package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// The argv contract with spinclass (spinclass#419; spinclass has the mirror
// test, with a fake of this verb): spinclass's [session-entry].restart
// hand-off runs `clown restart --session <principal> --when idle --reason
// <text>` and reads only exit 0 (queued) against non-zero. Each row runs the
// real verb against a real control listener with the frontend's own handler,
// and pins what the frontend receives and queues.
func TestRestartVerbArgvContract(t *testing.T) {
	const key = restartTestPrincipal
	long := strings.Repeat("a", 1000)
	for _, c := range []struct {
		name      string
		args      []string
		code      int
		reason    string // the reason the frontend receives (raw)
		queued    string // the reason it queues (sanitized)
		frontend  bool   // a live frontend listens
		stderrHas string
	}{
		{"spinclass shape", []string{"--session", key, "--when", "idle", "--reason", "new tools"}, 0, "new tools", "new tools", true, ""},
		{"when defaults to idle", []string{"--session", key, "--reason", "r"}, 0, "r", "r", true, ""},
		{"no reason", []string{"--session", key, "--when", "idle"}, 0, "", "", true, ""},
		{"quotes", []string{"--session", key, "--when", "idle", "--reason", `say "hi" and 'bye'`}, 0, `say "hi" and 'bye'`, `say "hi" and 'bye'`, true, ""},
		{"newlines", []string{"--session", key, "--when", "idle", "--reason", "line one\nline two\r\n"}, 0, "line one\nline two\r\n", "line one line two", true, ""},
		{"long", []string{"--session", key, "--when", "idle", "--reason", long}, 0, long, long[:restartReasonMaxRunes-3] + "...", true, ""},
		{"missing --session", []string{"--when", "idle", "--reason", "r"}, 2, "", "", true, "--session is required"},
		{"unknown flag --key", []string{"--key", key, "--when", "idle"}, 2, "", "", true, `unknown argument "--key"`},
		{"bad session key", []string{"--session", "a/b", "--when", "idle"}, 2, "", "", true, "is not a session key"},
		{"dot-dot session key", []string{"--session", "..", "--when", "idle"}, 2, "", "", true, "is not a session key"},
		{"--when now", []string{"--session", key, "--when", "now", "--reason", "r"}, 2, "", "", true, "only idle"},
		{"flag last without a value", []string{"--session", key, "--reason"}, 2, "", "", true, "--reason needs a value"},
		{"no frontend", []string{"--session", key, "--when", "idle", "--reason", "r"}, 1, "", "", false, "no live clown frontend"},
	} {
		t.Run(c.name, func(t *testing.T) {
			runtime := shortSocketDir(t)
			t.Setenv("XDG_RUNTIME_DIR", runtime)
			q := &restartQueue{}
			var mu sync.Mutex
			var got []controlMessage
			if c.frontend {
				handler := restartControlHandler("claude", "", q)
				l, err := startControlListener(controlSocketPath(runtime, key), func(m controlMessage) controlReply {
					mu.Lock()
					got = append(got, m)
					mu.Unlock()
					return handler(m)
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(l.Close)
			}
			var stdout, stderr bytes.Buffer
			if code := runRestartVerb(c.args, &stdout, &stderr); code != c.code {
				t.Fatalf("exit %d, want %d (stderr %q)", code, c.code, stderr.String())
			}
			if c.stderrHas != "" && !strings.Contains(stderr.String(), c.stderrHas) {
				t.Errorf("stderr %q lacks %q", stderr.String(), c.stderrHas)
			}
			mu.Lock()
			defer mu.Unlock()
			if c.code != 0 {
				if len(got) != 0 {
					t.Errorf("a refused argv reached the frontend: %+v", got)
				}
				if _, ok := q.take(); ok {
					t.Error("a refused argv was queued")
				}
				return
			}
			if len(got) != 1 || got[0].Op != "restart" || got[0].When != "idle" || got[0].Reason != c.reason {
				t.Fatalf("frontend received %+v, want one restart, when idle, reason %q", got, c.reason)
			}
			r, ok := q.take()
			if !ok || r.When != "idle" || r.Reason != c.queued {
				t.Fatalf("queued %+v (%v), want when idle, reason %q", r, ok, c.queued)
			}
			if !strings.Contains(stdout.String(), "restart queued for "+key+" (when idle)") {
				t.Errorf("stdout %q", stdout.String())
			}
		})
	}
}

// A frontend that refuses (here: its idle trigger is not armed) makes the
// verb exit 1 with the frontend's reason, and nothing is queued.
func TestRestartVerbReportsAFrontendRefusal(t *testing.T) {
	runtime := shortSocketDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	q := &restartQueue{}
	l, err := startControlListener(controlSocketPath(runtime, restartTestPrincipal), restartControlHandler("claude", "self-close is off", q))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	var stdout, stderr bytes.Buffer
	if code := runRestartVerb([]string{"--session", restartTestPrincipal, "--when", "idle", "--reason", "r"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "is not armed (self-close is off)") {
		t.Errorf("stderr %q", stderr.String())
	}
	if _, ok := q.take(); ok {
		t.Error("a refused restart was queued")
	}
}
