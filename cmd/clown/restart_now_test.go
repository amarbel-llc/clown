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
// trigger, a forged `now` reaches the preflight and stops at anything but a
// well-formed restart verdict.
func TestRestartNowControlMessageCannotRestartOnItsOwn(t *testing.T) {
	for _, c := range []struct{ name, out, exit string }{
		{"none", `{"verdict":"none","reason":"no restart requested","when":""}` + "\n", "0"},
		{"refused", `{"verdict":"refused","reason":"a merge job is running","when":"now"}` + "\n", "0"},
		{"garbage", "{not a verdict\n", "0"},
		{"non-zero exit", nowVerdict, "3"},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin, calls := fakeIdleSpinclass(t, c.out, c.exit)
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
		})
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

// An idle poke during a `now` preflight waits for it, then preflights the
// idle request queued meanwhile: one preflight at a time, each request asked
// about once. Without the shared mutex the poke would find the run in flight
// and leave the idle request unasked (one preflight); with overlapping runs
// the fake records an overlap.
func TestIdlePokeDuringANowWaitsAndPreflightsEachRequestOnce(t *testing.T) {
	dir := t.TempDir()
	release, started, calls := filepath.Join(dir, "release"), filepath.Join(dir, "started"), filepath.Join(dir, "calls")
	lock, overlaps := filepath.Join(dir, "lock"), filepath.Join(dir, "overlaps")
	bin := filepath.Join(dir, "spinclass")
	script := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> '" + calls + "'\ncase \"$1\" in\n" +
		"restart-preflight) mkdir '" + lock + "' 2>/dev/null || echo overlap >> '" + overlaps + "'; : > '" + started + "'; " +
		"while [ ! -e '" + release + "' ]; do sleep 0.05; done; rmdir '" + lock + "'; echo '{\"verdict\":\"none\",\"reason\":\"\",\"when\":\"\"}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	q := &restartQueue{}
	var rec recordedRestarts
	r := newTestIdleRunner(t, bin, q, &rec)
	if !r.triggerNow(restartRequest{When: "now", Reason: "now"}) {
		t.Fatal("now refused")
	}
	waitForFile(t, started)
	// An idle request arrives while the now's preflight is held.
	if reply := restartControlHandlerNow("claude", "", q, r.triggerNow)(controlMessage{Op: "restart", When: "idle", Reason: "idle"}); !reply.OK {
		t.Fatalf("idle during a now: %+v", reply)
	}
	waiting := make(chan struct{})
	r.waitingForMu = func() { close(waiting) }
	idleDone := make(chan struct{})
	go func() { r.onIdle(); close(idleDone) }()
	<-waiting
	// The poke has done its close-self-run step and is about to take mu,
	// which the now's run holds until release, so it cannot get past it
	// before the release below.
	if r.mu.TryLock() {
		r.mu.Unlock()
		t.Fatal("the now's run does not hold the runner's mutex during its preflight")
	}
	_ = os.WriteFile(release, nil, 0o600)
	r.waitNow()
	<-idleDone
	n := 0
	for _, c := range readCalls(t, calls) {
		if c == "restart-preflight" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("preflights = %d, want 2: the now's, then the queued idle's (calls %q)", n, readCalls(t, calls))
	}
	if b, err := os.ReadFile(overlaps); err == nil {
		t.Fatalf("preflights overlapped: %s", b)
	}
	if _, ok := q.take(); ok {
		t.Fatal("a request was left unasked")
	}
}

// A hung close-self-run does not wedge `now`, and does not drop a close: a
// `now` while it hangs is accepted and preflighted at once (it never waits on
// close-self-run), and a second idle poke waits for the hung run and then
// runs its own close-self-run.
func TestHungCloseSelfRunDoesNotWedgeNowOrDropAClose(t *testing.T) {
	dir := t.TempDir()
	calls, release, count := filepath.Join(dir, "calls"), filepath.Join(dir, "release"), filepath.Join(dir, "count")
	bin := filepath.Join(dir, "spinclass")
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"close-self-run) n=$(cat '" + count + "' 2>/dev/null || echo 0); n=$((n+1)); echo $n > '" + count + "'; " +
		"echo csr-start-$n >> '" + calls + "'; : > '" + dir + "'/started-$n; " +
		"while [ ! -e '" + release + "' ]; do sleep 0.05; done; echo csr-end-$n >> '" + calls + "' ;;\n" +
		"restart-preflight) echo preflight >> '" + calls + "'; echo '{\"verdict\":\"none\",\"reason\":\"\",\"when\":\"\"}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	q := &restartQueue{}
	var rec recordedRestarts
	r := newTestIdleRunner(t, bin, q, &rec)
	done1, done2 := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		// Ends the fake's wait loop, so no child outlives the test.
		_ = os.WriteFile(release, nil, 0o600)
		<-done1
		r.waitNow()
	})
	go func() { r.onIdle(); close(done1) }()
	waitForFile(t, filepath.Join(dir, "started-1"))

	if !r.triggerNow(restartRequest{When: "now"}) {
		t.Fatal("a now during a hung close-self-run was refused")
	}
	nowDone := make(chan struct{})
	go func() { r.waitNow(); close(nowDone) }()
	select {
	case <-nowDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the now waited on the hung close-self-run")
	}
	if got := strings.Join(readCalls(t, calls), ","); got != "csr-start-1,preflight" {
		t.Fatalf("calls = %q, want the hung close-self-run and the now's preflight", got)
	}

	waiting := make(chan struct{})
	r.waitingForCloseMu = func() { close(waiting) }
	go func() { r.onIdle(); close(done2) }()
	<-waiting
	// The second poke is at the close-self-run lock the first one holds
	// until release: its own run cannot have started.
	if _, err := os.Stat(filepath.Join(dir, "started-2")); err == nil {
		t.Fatal("a second close-self-run started while the first was still running")
	}
	_ = os.WriteFile(release, nil, 0o600)
	for _, d := range []chan struct{}{done1, done2} {
		select {
		case <-d:
		case <-time.After(10 * time.Second):
			t.Fatal("an idle poke did not finish after the release")
		}
	}
	if got := strings.Join(readCalls(t, calls), ","); got != "csr-start-1,preflight,csr-end-1,csr-start-2,csr-end-2" {
		t.Fatalf("calls = %q, want the second poke's close-self-run after the first ended, not dropped", got)
	}
}

// A `now` whose check finds a plan stored between the handler's check and
// the trigger is refused, not answered OK and then ignored.
func TestTriggerNowRefusesWhenARestartIsUnderway(t *testing.T) {
	t.Cleanup(func() { pendingRelaunch.Store(nil) })
	bin, calls := fakeIdleSpinclass(t, nowVerdict, "0")
	q := &restartQueue{}
	var rec recordedRestarts
	r := newTestIdleRunner(t, bin, q, &rec)
	pendingRelaunch.Store(&relaunchPlan{principal: restartTestPrincipal})
	if r.triggerNow(restartRequest{When: "now"}) {
		t.Fatal("triggerNow accepted while a restart is underway")
	}
	r.waitNow()
	if got := readCalls(t, calls); len(got) != 0 {
		t.Fatalf("calls = %q", got)
	}
	if _, ok := q.take(); ok {
		t.Fatal("queued while underway")
	}
	if r.inFlight.Load() {
		t.Fatal("inFlight left set by a refused trigger")
	}
	pendingRelaunch.Store(nil)
	r.restart = func(restartRequest, preflightVerdict) {}
	if !r.triggerNow(restartRequest{When: "now"}) {
		t.Fatal("a now after the restart cleared was refused")
	}
	r.waitNow()
}

// A frontend exiting with a restart check in flight, or one queued, says so
// in the log (spinclass may be holding its restart stamp); one exiting for
// its own restart (plan stored) or with nothing pending says nothing.
func TestIdleRunnerNotesARestartLeftAtExit(t *testing.T) {
	t.Cleanup(func() { pendingRelaunch.Store(nil) })
	for _, c := range []struct {
		name     string
		setup    func(r *idleRunner)
		wantNote string
	}{
		{"in flight", func(r *idleRunner) { r.inFlight.Store(true) }, "in flight"},
		{"queued", func(r *idleRunner) { r.queue.put(restartRequest{When: "idle"}) }, "queued"},
		{"own restart", func(r *idleRunner) {
			r.inFlight.Store(true)
			pendingRelaunch.Store(&relaunchPlan{principal: restartTestPrincipal})
		}, ""},
		{"nothing", func(*idleRunner) {}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			pendingRelaunch.Store(nil)
			var rec recordedRestarts
			r := newTestIdleRunner(t, "/nonexistent", &restartQueue{}, &rec)
			c.setup(r)
			r.noteExit()
			log, _ := os.ReadFile(r.logPath)
			if c.wantNote == "" {
				if len(log) != 0 {
					t.Fatalf("logged %q, want nothing", log)
				}
				return
			}
			if !strings.Contains(string(log), c.wantNote) || !strings.Contains(string(log), "exiting") {
				t.Fatalf("log %q lacks an exit note mentioning %q", log, c.wantNote)
			}
		})
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
	for _, c := range []struct {
		name, line string
		wantStop   bool
	}{
		{"when idle", `{"verdict":"restart","reason":"r","when":"idle"}`, false},
		{"when empty", `{"verdict":"restart","reason":"r","when":""}`, false},
		{"when omitted", `{"verdict":"restart","reason":"r"}`, false},
		{"when NOW", `{"verdict":"restart","reason":"r","when":"NOW"}`, false},
		{"when now", `{"verdict":"restart","reason":"r","when":"now"}`, true},
	} {
		for _, countdown := range []string{"inline", "none"} {
			t.Run(c.name+"/"+countdown, func(t *testing.T) {
				v, err := parsePreflightVerdict([]byte(c.line + "\n"))
				if err != nil {
					t.Fatal(err)
				}
				pendingRelaunch.Store(nil)
				providerRunning.Store(true)
				t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
				busy := func() bool { return true }
				stops := 0
				fr := frontendRestarter{
					principal: restartTestPrincipal,
					stop:      func() { stops++ },
					busy:      busy,
					stopGrace: time.Hour,
					logPath:   filepath.Join(t.TempDir(), "l.log"),
				}
				if countdown == "inline" {
					fr.countdown = restartCountdown{
						lookPath:    func(string) (string, error) { return "", os.ErrNotExist },
						interactive: true,
						notify:      &discardWriter{},
						sleep:       func(time.Duration) {},
						busy:        busy,
					}.proceed
				}
				// The request came over the socket as `now` in every case.
				fr.restart(restartRequest{When: "now"}, v)
				if got := stops == 1; got != c.wantStop {
					t.Fatalf("stopped %v, want %v: only a `now` verdict stops a busy session", got, c.wantStop)
				}
			})
		}
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
