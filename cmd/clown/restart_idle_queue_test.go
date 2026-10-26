package main

import (
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// clown#327: `clown restart --when idle` on a session that is already idle
// must ask spinclass at once. The tests run the real idle listener, the real
// control socket and the real `clown restart` verb; only spinclass is a fake.

const idleVerdict = `{"verdict":"restart","reason":"r","when":"idle"}` + "\n"

type idleQueueRig struct {
	t     *testing.T
	r     *idleRunner
	rec   *recordedRestarts
	calls string
	busy  *atomic.Bool
	sock  string
}

func newIdleQueueRig(t *testing.T) *idleQueueRig {
	t.Helper()
	runtime := shortSocketDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	bin, calls := fakeIdleSpinclass(t, idleVerdict, "0")
	q := &restartQueue{}
	rec := &recordedRestarts{}
	r := newTestIdleRunner(t, bin, q, rec)
	busy := &atomic.Bool{}
	r.busy = busy.Load
	trig := &restartTriggers{now: r.triggerNow, idle: r.queueIdle}
	cl, err := startControlListener(controlSocketPath(runtime, restartTestPrincipal), restartControlHandlerTriggers("claude", "", q, trig))
	if err != nil {
		t.Fatal(err)
	}
	sock := idleSocketPath(runtime, restartTestPrincipal)
	il, err := startIdleListenerTracking(sock, busy, r.onIdle)
	if err != nil {
		cl.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		il.Close()
		cl.Close()
		r.waitIdle()
	})
	return &idleQueueRig{t: t, r: r, rec: rec, calls: calls, busy: busy, sock: sock}
}

func (g *idleQueueRig) askIdleRestart() {
	g.t.Helper()
	var stdout, stderr strings.Builder
	if code := runRestartVerb([]string{"--session", restartTestPrincipal, "--when", "idle", "--reason", "r"}, &stdout, &stderr); code != 0 {
		g.t.Fatalf("clown restart exit %d: %s", code, stderr.String())
	}
}

func (g *idleQueueRig) send(b byte) {
	g.t.Helper()
	c, err := net.Dial("unixgram", g.sock)
	if err != nil {
		g.t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte{b}); err != nil {
		g.t.Fatal(err)
	}
}

func (g *idleQueueRig) preflights() int {
	n := 0
	for _, c := range readCalls(g.t, g.calls) {
		if c == "restart-preflight|"+restartTestPrincipal {
			n++
		}
	}
	return n
}

func (g *idleQueueRig) waitPreflights(want int) {
	g.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for g.preflights() < want {
		if time.Now().After(deadline) {
			g.t.Fatalf("preflights = %d, want %d (calls %q)", g.preflights(), want, readCalls(g.t, g.calls))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An already-idle frontend asks spinclass exactly once, with no datagram.
func TestRestartIdleOnAnIdleSessionPreflightsWithoutADatagram(t *testing.T) {
	g := newIdleQueueRig(t)
	g.askIdleRestart()
	g.waitPreflights(1)
	g.r.waitIdle()
	if g.preflights() != 1 || g.rec.n.Load() != 1 {
		t.Fatalf("preflights = %d, restarts = %d, want one each", g.preflights(), g.rec.n.Load())
	}
}

// A busy frontend waits for its Stop datagram, which then runs the preflight.
func TestRestartIdleOnABusySessionWaitsForTheStop(t *testing.T) {
	g := newIdleQueueRig(t)
	g.send(turnStartedDatagram)
	deadline := time.Now().Add(5 * time.Second)
	for !g.busy.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the busy datagram was not seen")
		}
		time.Sleep(5 * time.Millisecond)
	}
	g.askIdleRestart()
	g.r.waitIdle()
	time.Sleep(200 * time.Millisecond)
	if n := g.preflights(); n != 0 {
		t.Fatalf("preflights = %d while the turn runs, want 0", n)
	}
	g.send(1)
	g.waitPreflights(1)
}

// A turn that starts while the kicked transition waits for mu leaves the
// request queued instead of consuming it mid-turn.
func TestRestartIdleKickSeesATurnThatStartedWhileItWaited(t *testing.T) {
	g := newIdleQueueRig(t)
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	g.r.waitingForMu = func() { once.Do(func() { close(reached); <-release }) }
	g.askIdleRestart()
	<-reached
	g.busy.Store(true)
	close(release)
	g.r.waitIdle()
	if n := g.preflights(); n != 0 {
		t.Fatalf("preflights = %d, want 0 for a request that met a running turn", n)
	}
	if !g.r.queue.hasPending() {
		t.Fatal("the request must stay queued for the turn's Stop")
	}
	g.send(1)
	g.waitPreflights(1)
}

// A request racing a Stop datagram is asked about once.
func TestRestartIdleRacingAPokeIsAskedOnce(t *testing.T) {
	for i := 0; i < 5; i++ {
		g := newIdleQueueRig(t)
		g.askIdleRestart()
		g.send(1)
		g.waitPreflights(1)
		g.r.waitIdle()
		time.Sleep(100 * time.Millisecond)
		if n := g.preflights(); n != 1 {
			t.Fatalf("round %d: preflights = %d, want 1", i, n)
		}
	}
}
