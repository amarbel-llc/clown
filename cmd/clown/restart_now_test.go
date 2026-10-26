package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// `clown restart --when now` (spinclass#419): the control message only makes
// the frontend ask spinclass at once instead of at the next idle; the verdict
// still decides, and the countdown and the profile gate still run.

const nowVerdict = `{"verdict":"restart","reason":"restart of k requested by op: now","when":"now"}` + "\n"

// A queued `now` runs the preflight at once (no idle poke, no close-self-run)
// and restarts on a "restart" verdict, with that verdict.
func TestRestartNowRunsThePreflightAtOnce(t *testing.T) {
	bin, calls := fakeIdleSpinclass(t, nowVerdict, "0")
	q := &restartQueue{}
	var rec recordedRestarts
	r := newTestIdleRunner(t, bin, q, &rec)
	var verdict preflightVerdict
	r.restart = func(req restartRequest, v preflightVerdict) {
		rec.n.Add(1)
		rec.last.Store(&req)
		verdict = v
	}
	if !r.triggerNow(restartRequest{When: "now", Reason: "socket text"}) {
		t.Fatal("triggerNow refused with nothing in flight")
	}
	r.waitNow()
	if got := readCalls(t, calls); len(got) != 1 || got[0] != "restart-preflight|"+restartTestPrincipal {
		t.Fatalf("calls = %q, want one preflight and no close-self-run", got)
	}
	if rec.n.Load() != 1 || verdict.When != "now" || verdict.Reason != "restart of k requested by op: now" {
		t.Fatalf("restarts = %d with verdict %+v; want one with spinclass's verdict", rec.n.Load(), verdict)
	}
}

// Without a "restart" verdict a `now` does nothing, and the request is gone.
func TestRestartNowWithoutARestartVerdictDoesNothing(t *testing.T) {
	for _, c := range []struct{ name, out, exit string }{
		{"none", `{"verdict":"none","reason":"no request","when":""}` + "\n", "0"},
		{"refused", `{"verdict":"refused","reason":"a merge job is running","when":"now"}` + "\n", "0"},
		{"non-zero exit", nowVerdict, "1"},
		{"garbage", "not json\n", "0"},
		{"empty", "", "0"},
		{"two lines", nowVerdict + nowVerdict, "0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin, calls := fakeIdleSpinclass(t, c.out, c.exit)
			q := &restartQueue{}
			var rec recordedRestarts
			r := newTestIdleRunner(t, bin, q, &rec)
			r.triggerNow(restartRequest{When: "now"})
			r.waitNow()
			if rec.n.Load() != 0 {
				t.Fatal("restarted without a restart verdict")
			}
			if got := readCalls(t, calls); len(got) != 1 {
				t.Fatalf("calls = %q, want exactly one preflight", got)
			}
			if _, ok := q.take(); ok {
				t.Fatal("the request must be dropped")
			}
		})
	}
}

// A control message on its own cannot restart: through the real handler and
// trigger, a forged `now` that spinclass has no request for (verdict none)
// reaches the preflight and stops there.
func TestRestartNowControlMessageCannotRestartOnItsOwn(t *testing.T) {
	bin, calls := fakeIdleSpinclass(t, `{"verdict":"none","reason":"no restart requested","when":""}`+"\n", "0")
	q := &restartQueue{}
	var rec recordedRestarts
	r := newTestIdleRunner(t, bin, q, &rec)
	handle := restartControlHandlerNow("claude", "", q, r.triggerNow)
	if reply := handle(controlMessage{Op: "restart", When: "now", Reason: "forged"}); !reply.OK {
		t.Fatalf("reply = %+v", reply)
	}
	r.waitNow()
	if rec.n.Load() != 0 {
		t.Fatal("a control message restarted without spinclass's verdict")
	}
	if got := readCalls(t, calls); len(got) != 1 || !strings.HasPrefix(got[0], "restart-preflight|") {
		t.Fatalf("calls = %q, want the preflight asked once", got)
	}
}

// End to end over the socket: `clown restart --when now` is accepted (exit
// 0), and with spinclass answering anything but restart nothing restarts.
func TestRestartVerbNowWithoutARestartVerdictDoesNothing(t *testing.T) {
	for _, out := range []string{
		`{"verdict":"none","reason":"no request","when":""}` + "\n",
		`{"verdict":"refused","reason":"a merge job is running","when":"now"}` + "\n",
	} {
		runtime := shortSocketDir(t)
		t.Setenv("XDG_RUNTIME_DIR", runtime)
		bin, calls := fakeIdleSpinclass(t, out, "0")
		q := &restartQueue{}
		var rec recordedRestarts
		r := newTestIdleRunner(t, bin, q, &rec)
		l, err := startControlListener(controlSocketPath(runtime, restartTestPrincipal), restartControlHandlerNow("claude", "", q, r.triggerNow))
		if err != nil {
			t.Fatal(err)
		}
		var stdout, stderr strings.Builder
		code := runRestartVerb([]string{"--session", restartTestPrincipal, "--when", "now", "--reason", "r"}, &stdout, &stderr)
		r.waitNow()
		l.Close()
		if code != 0 {
			t.Fatalf("exit %d (%s), want 0: accepted", code, stderr.String())
		}
		if rec.n.Load() != 0 {
			t.Fatalf("verdict %s: restarted", out)
		}
		if got := readCalls(t, calls); len(got) != 1 {
			t.Fatalf("calls = %q, want one preflight", got)
		}
	}
}

// The reply comes back as soon as the request is accepted, before a slow
// preflight finishes (the verb waits 10 s, the preflight may take 30 s).
func TestRestartNowRepliesBeforeThePreflight(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	bin := filepath.Join(dir, "spinclass")
	script := "#!/bin/sh\nwhile [ ! -e '" + release + "' ]; do sleep 0.05; done\necho '{\"verdict\":\"none\",\"reason\":\"\",\"when\":\"\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	q := &restartQueue{}
	var rec recordedRestarts
	r := newTestIdleRunner(t, bin, q, &rec)
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600); r.waitNow() })
	replied := make(chan controlReply, 1)
	go func() {
		replied <- restartControlHandlerNow("claude", "", q, r.triggerNow)(controlMessage{Op: "restart", When: "now"})
	}()
	select {
	case reply := <-replied:
		if !reply.OK {
			t.Fatalf("reply = %+v", reply)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reply waited for the preflight")
	}
}

// A `now` while a preflight or restart is in flight, or while a restart is
// underway, is refused and not queued.
func TestRestartNowRefusedWhileInFlightOrUnderway(t *testing.T) {
	t.Cleanup(func() { pendingRelaunch.Store(nil) })
	q := &restartQueue{}
	inFlight := func(restartRequest) bool { return false }
	handle := restartControlHandlerNow("claude", "", q, inFlight)
	if reply := handle(controlMessage{Op: "restart", When: "now"}); reply.OK || !strings.Contains(reply.Error, "already underway") {
		t.Fatalf("in flight: reply = %+v, want already underway", reply)
	}
	if _, ok := q.take(); ok {
		t.Fatal("queued while in flight")
	}
	pendingRelaunch.Store(&relaunchPlan{principal: restartTestPrincipal})
	triggered := false
	if reply := restartControlHandlerNow("claude", "", q, func(restartRequest) bool { triggered = true; return true })(controlMessage{Op: "restart", When: "now"}); reply.OK || !strings.Contains(reply.Error, "already underway") || triggered {
		t.Fatalf("underway: reply = %+v", reply)
	}
	if _, ok := q.take(); ok {
		t.Fatal("queued while underway")
	}
}

// The idle runner's own preflight counts as in flight: a `now` arriving
// during it is refused.
func TestRestartNowRefusedDuringTheIdleRunnersPreflight(t *testing.T) {
	dir := t.TempDir()
	release, started := filepath.Join(dir, "release"), filepath.Join(dir, "started")
	bin := filepath.Join(dir, "spinclass")
	script := "#!/bin/sh\ncase \"$1\" in\nrestart-preflight) : > '" + started + "'; while [ ! -e '" + release + "' ]; do sleep 0.05; done; echo '{\"verdict\":\"none\",\"reason\":\"\",\"when\":\"\"}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	q := &restartQueue{}
	var rec recordedRestarts
	r := newTestIdleRunner(t, bin, q, &rec)
	q.put(restartRequest{When: "idle"})
	idleDone := make(chan struct{})
	go func() { r.onIdle(); close(idleDone) }()
	waitForFile(t, started)
	if r.triggerNow(restartRequest{When: "now"}) {
		t.Fatal("a now started during the idle runner's preflight")
	}
	if _, ok := q.take(); ok {
		t.Fatal("a refused now was queued")
	}
	_ = os.WriteFile(release, nil, 0o600)
	<-idleDone
}

// An idle poke during a `now` preflight waits for it and does not preflight
// the same request again.
func TestIdlePokeDuringANowDoesNotPreflightTwice(t *testing.T) {
	dir := t.TempDir()
	release, started, calls := filepath.Join(dir, "release"), filepath.Join(dir, "started"), filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "spinclass")
	script := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> '" + calls + "'\ncase \"$1\" in\nrestart-preflight) : > '" + started + "'; while [ ! -e '" + release + "' ]; do sleep 0.05; done; echo '{\"verdict\":\"none\",\"reason\":\"\",\"when\":\"\"}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	q := &restartQueue{}
	var rec recordedRestarts
	r := newTestIdleRunner(t, bin, q, &rec)
	r.triggerNow(restartRequest{When: "now"})
	waitForFile(t, started)
	idleDone := make(chan struct{})
	go func() { r.onIdle(); close(idleDone) }()
	_ = os.WriteFile(release, nil, 0o600)
	r.waitNow()
	<-idleDone
	n := 0
	for _, c := range readCalls(t, calls) {
		if c == "restart-preflight" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("preflights = %d, want 1 (calls %q)", n, readCalls(t, calls))
	}
}

// A `now` replaces a queued `idle` (newest wins): the now's run takes it, and
// the next idle poke has nothing left to preflight.
func TestRestartNowReplacesAQueuedIdle(t *testing.T) {
	bin, calls := fakeIdleSpinclass(t, nowVerdict, "0")
	q := &restartQueue{}
	var rec recordedRestarts
	r := newTestIdleRunner(t, bin, q, &rec)
	handle := restartControlHandlerNow("claude", "", q, r.triggerNow)
	if reply := handle(controlMessage{Op: "restart", When: "idle", Reason: "first"}); !reply.OK {
		t.Fatalf("idle: %+v", reply)
	}
	if reply := handle(controlMessage{Op: "restart", When: "now", Reason: "second"}); !reply.OK {
		t.Fatalf("now: %+v", reply)
	}
	r.waitNow()
	if rec.n.Load() != 1 || rec.last.Load().When != "now" || rec.last.Load().Reason != "second" {
		t.Fatalf("restarts = %d (%+v), want one, for the now", rec.n.Load(), rec.last.Load())
	}
	r.onIdle()
	if got := readCalls(t, calls); strings.Join(got, ",") != "restart-preflight|"+restartTestPrincipal+",close-self-run|"+restartTestPrincipal {
		t.Fatalf("calls = %q, want the now's preflight, then the idle's close-self-run only", got)
	}
}

// `now` is refused where `idle` is: an unarmed trigger, a non-claude
// provider, and a frontend with no immediate trigger at all.
func TestRestartNowRefusedWhereIdleIs(t *testing.T) {
	for _, c := range []struct {
		name, provider, unarmed string
		now                     func(restartRequest) bool
		want                    string
	}{
		{"unarmed", "claude", "CLOWN_DISABLE_SELF_CLOSE=1", func(restartRequest) bool { return true }, "is not armed"},
		{"no idle trigger", "opencode", "provider is not claude", func(restartRequest) bool { return true }, "has none"},
		{"no now trigger", "claude", "", nil, "--when now"},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := &restartQueue{}
			reply := restartControlHandlerNow(c.provider, c.unarmed, q, c.now)(controlMessage{Op: "restart", When: "now"})
			if reply.OK || !strings.Contains(reply.Error, c.want) {
				t.Fatalf("reply = %+v, want a refusal mentioning %q", reply, c.want)
			}
			if _, ok := q.take(); ok {
				t.Fatal("queued a refused now")
			}
		})
	}
}

func TestControlRefusesAnUnknownWhen(t *testing.T) {
	q := &restartQueue{}
	reply := restartControlHandlerNow("claude", "", q, func(restartRequest) bool { return true })(controlMessage{Op: "restart", When: "soon"})
	if reply.OK || !strings.Contains(reply.Error, "idle or now") {
		t.Fatalf("reply = %+v", reply)
	}
	if _, ok := q.take(); ok {
		t.Fatal("queued an unknown when")
	}
}

// Under a "now" verdict a running turn is expected: neither the countdown nor
// the restarter cancels for it, and the countdown is told so. A viewer's
// cancel and a profile refusal still drop the restart.
func TestRestarterNowStopsABusySession(t *testing.T) {
	for _, c := range []struct {
		name      string
		countdown func(string, bool) (bool, string)
		profile   error
		wantStop  bool
	}{
		{"busy", func(_ string, cancelOnTurn bool) (bool, string) {
			if cancelOnTurn {
				t.Error("a now restart asked the countdown to cancel on a turn")
			}
			return true, "elapsed"
		}, nil, true},
		{"viewer cancel", func(string, bool) (bool, string) { return false, "posh modal: cancelled by a viewer" }, nil, false},
		{"profile refusal", func(string, bool) (bool, string) { return true, "elapsed" }, os.ErrPermission, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			pendingRelaunch.Store(nil)
			providerRunning.Store(true)
			t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
			stops := 0
			fr := frontendRestarter{
				principal:    restartTestPrincipal,
				stop:         func() { stops++ },
				countdown:    c.countdown,
				profileCheck: func() error { return c.profile },
				busy:         func() bool { return true },
				stopGrace:    time.Hour,
				logPath:      filepath.Join(t.TempDir(), "l.log"),
			}
			fr.restart(restartRequest{When: "now"}, preflightVerdict{Verdict: "restart", Reason: "r", When: "now"})
			if got := stops == 1; got != c.wantStop {
				t.Fatalf("stops = %d, want stopped %v", stops, c.wantStop)
			}
		})
	}
}

// The `now` behaviour follows spinclass's verdict, not the socket: a request
// that arrived as `now` whose verdict says idle keeps the busy cancel.
func TestRestarterBusyCancelFollowsTheVerdict(t *testing.T) {
	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	stops := 0
	fr := frontendRestarter{
		principal: restartTestPrincipal,
		stop:      func() { stops++ },
		countdown: func(_ string, cancelOnTurn bool) (bool, string) { return !cancelOnTurn, "" },
		busy:      func() bool { return true },
		logPath:   filepath.Join(t.TempDir(), "l.log"),
	}
	fr.restart(restartRequest{When: "now"}, preflightVerdict{Verdict: "restart", Reason: "r", When: "idle"})
	if stops != 0 {
		t.Fatal("a socket `now` with an idle verdict stopped a busy session")
	}
}

// The inline countdown keeps its 30 s for `now` but does not end on a turn.
func TestCountdownNowIgnoresATurn(t *testing.T) {
	var slept time.Duration
	cd := restartCountdown{
		lookPath:    func(string) (string, error) { return "", os.ErrNotExist },
		interactive: true,
		notify:      &discardWriter{},
		sleep:       func(d time.Duration) { slept += d },
		busy:        func() bool { return true },
	}
	proceed, _ := cd.proceed("r", false)
	if !proceed || slept < restartCountdownSeconds*time.Second {
		t.Fatalf("proceed %v after %v; want the full countdown, then proceed", proceed, slept)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
