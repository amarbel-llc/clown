package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

// clown#82: a client that disconnects cancels its in-flight, client-scoped
// call (a start's wait for health, a download) and its queued requests; a
// stop always completes. The protocol rule: a client keeps its write side
// open until its reply arrives; EOF on the read side (a close, or a
// half-close) means the client is gone.

// blockingLauncher holds Start until release or ctx is done, and Stop until
// release, and reports what each saw of its ctx.
type blockingLauncher struct {
	started   chan string // the alias of each Start that began
	cancelled chan error  // one per Start that saw its ctx done
	finished  chan string // the alias of each Start released normally
	stopping  chan struct{}
	stopped   chan error // Stop's ctx.Err() once released
	release   chan struct{}
	once      sync.Once
}

func newBlockingLauncher() *blockingLauncher {
	return &blockingLauncher{
		started:   make(chan string, 16),
		cancelled: make(chan error, 16),
		finished:  make(chan string, 16),
		stopping:  make(chan struct{}, 16),
		stopped:   make(chan error, 16),
		release:   make(chan struct{}),
	}
}

func (b *blockingLauncher) releaseAll() { b.once.Do(func() { close(b.release) }) }

func (b *blockingLauncher) Start(ctx context.Context, p rm.StartInstanceParams) (rm.Instance, error) {
	b.started <- p.Alias
	select {
	case <-ctx.Done():
		b.cancelled <- ctx.Err()
		return rm.Instance{}, ctx.Err()
	case <-b.release:
		b.finished <- p.Alias
		return rm.Instance{Alias: p.Alias, Port: 1, Bind: "127.0.0.1"}, nil
	}
}

func (b *blockingLauncher) Stop(ctx context.Context, alias string) error {
	b.stopping <- struct{}{}
	<-b.release
	b.stopped <- ctx.Err()
	return nil
}

// connEvents are the server's per-connection test hooks as channels.
type connEvents struct {
	readEnded, handleDone chan struct{}
}

func withConnEvents(s *server) connEvents {
	ev := connEvents{readEnded: make(chan struct{}, 16), handleDone: make(chan struct{}, 16)}
	s.onReadEnd = func() { ev.readEnded <- struct{}{} }
	s.onHandleDone = func() { ev.handleDone <- struct{}{} }
	return ev
}

func serveWith(t *testing.T, s *server) (sock string) {
	t.Helper()
	sock = filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.Serve(ctx, ln)
	return sock
}

func dialRaw(t *testing.T, sock string) *net.UnixConn {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c.(*net.UnixConn)
}

func writeReq(t *testing.T, c net.Conn, id, method string, params any) {
	t.Helper()
	if err := rm.WriteFrame(c, rm.Envelope{JSONRPC: "2.0", ID: json.Number(id), Method: method, Params: mustJSON(t, params)}); err != nil {
		t.Fatal(err)
	}
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

func TestDisconnectCancelsAnInFlightStart(t *testing.T) {
	l := newBlockingLauncher()
	t.Cleanup(l.releaseAll)
	sock := serveWith(t, newServer(rm.NewRegistry(), l))
	c := dialRaw(t, sock)
	writeReq(t, c, "1", rm.MethodStartInstance, rm.StartInstanceParams{Alias: "a", Model: "a"})
	recv(t, l.started, "the start to begin")
	c.Close()
	if err := recv(t, l.cancelled, "the start's context to be cancelled after the disconnect"); !errors.Is(err, context.Canceled) {
		t.Fatalf("start ctx err = %v, want context.Canceled", err)
	}
}

func TestDisconnectCancelsAResolveModelStart(t *testing.T) {
	l := newBlockingLauncher()
	t.Cleanup(l.releaseAll)
	s := newServer(rm.NewRegistry(), l)
	s.modelsDir = shortTempDir(t)
	if err := os.WriteFile(filepath.Join(s.modelsDir, "m.gguf"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.remoteModelsPath = filepath.Join(shortTempDir(t), "models.toml")
	sock := serveWith(t, s)
	c := dialRaw(t, sock)
	writeReq(t, c, "1", rm.MethodResolveModel, rm.ResolveModelParams{Name: "m"})
	recv(t, l.started, "the resolve's start to begin")
	c.Close()
	recv(t, l.cancelled, "the resolve's start to be cancelled after the disconnect")
}

// The protocol rule, pinned: a client that sends its request and then
// half-closes (shuts its write side) is gone; its start is cancelled and no
// reply is written. In-tree clients (internal/juggler Client) never
// half-close.
func TestHalfCloseIsADisconnect(t *testing.T) {
	l := newBlockingLauncher()
	t.Cleanup(l.releaseAll)
	sock := serveWith(t, newServer(rm.NewRegistry(), l))
	c := dialRaw(t, sock)
	writeReq(t, c, "1", rm.MethodStartInstance, rm.StartInstanceParams{Alias: "a"})
	recv(t, l.started, "the start to begin")
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	recv(t, l.cancelled, "the half-closed client's start to be cancelled")
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if resp, err := rm.ReadFrame(bufio.NewReader(c)); err == nil {
		t.Fatalf("a half-closed client got a reply %+v; want none", resp)
	}
}

// Pipelined starts with the client leaving: the in-flight one is cancelled
// at once and the queued one is never started.
func TestDisconnectSkipsQueuedRequests(t *testing.T) {
	l := newBlockingLauncher()
	t.Cleanup(l.releaseAll)
	s := newServer(rm.NewRegistry(), l)
	ev := withConnEvents(s)
	sock := serveWith(t, s)
	c := dialRaw(t, sock)
	writeReq(t, c, "1", rm.MethodStartInstance, rm.StartInstanceParams{Alias: "first"})
	writeReq(t, c, "2", rm.MethodStartInstance, rm.StartInstanceParams{Alias: "queued"})
	if got := recv(t, l.started, "the first start"); got != "first" {
		t.Fatalf("first start = %q", got)
	}
	c.Close()
	recv(t, ev.readEnded, "the server to see the disconnect")
	recv(t, l.cancelled, "the in-flight start to be cancelled")
	recv(t, ev.handleDone, "the connection's handler to end")
	select {
	case got := <-l.started:
		t.Fatalf("the queued start %q ran after its client left", got)
	default:
	}
}

// A stop is not client-scoped: it completes, uncancelled, even when the
// client leaves while it runs.
func TestDisconnectDoesNotCancelAStop(t *testing.T) {
	l := newBlockingLauncher()
	s := newServer(rm.NewRegistry(), l)
	ev := withConnEvents(s)
	sock := serveWith(t, s)
	c := dialRaw(t, sock)
	writeReq(t, c, "1", rm.MethodStopInstance, rm.StopInstanceParams{Alias: "a"})
	recv(t, l.stopping, "the stop to block in the launcher")
	c.Close()
	recv(t, ev.readEnded, "the server to see the disconnect")
	l.releaseAll()
	if err := recv(t, l.stopped, "the stop to finish"); err != nil {
		t.Fatalf("stop ctx err = %v, want a stop that is never cancelled", err)
	}
}

// A connection that stays open gets its answers: two pipelined starts, the
// first still in flight when the second arrives, are both answered in
// order, and neither is cancelled.
func TestOpenConnectionRequestsAreUnaffected(t *testing.T) {
	l := newBlockingLauncher()
	t.Cleanup(l.releaseAll)
	sock := serveWith(t, newServer(rm.NewRegistry(), l))
	c := dialRaw(t, sock)
	writeReq(t, c, "1", rm.MethodStartInstance, rm.StartInstanceParams{Alias: "a1"})
	recv(t, l.started, "the first start to be in flight")
	writeReq(t, c, "2", rm.MethodStartInstance, rm.StartInstanceParams{Alias: "a2"})
	l.releaseAll()
	br := bufio.NewReader(c)
	for _, want := range []string{"1", "2"} {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		resp, err := rm.ReadFrame(br)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Error != nil || string(resp.ID) != want {
			t.Fatalf("response %+v, want a result for id %s", resp, want)
		}
	}
	if got := []string{recv(t, l.finished, "first finish"), recv(t, l.finished, "second finish")}; got[0] != "a1" || got[1] != "a2" {
		t.Fatalf("finished %q, want [a1 a2]", got)
	}
	select {
	case err := <-l.cancelled:
		t.Fatalf("a start on an open connection was cancelled: %v", err)
	default:
	}
}

// The goroutines of disconnected connections end: every handler reports
// done, and the goroutine count returns to its quiesced baseline.
func TestDisconnectLeavesNoGoroutines(t *testing.T) {
	l := newBlockingLauncher()
	t.Cleanup(l.releaseAll)
	s := newServer(rm.NewRegistry(), l)
	ev := withConnEvents(s)
	sock := serveWith(t, s)
	baseline := quiescedGoroutines()
	for range 5 {
		c := dialRaw(t, sock)
		writeReq(t, c, "1", rm.MethodStartInstance, rm.StartInstanceParams{Alias: "a"})
		recv(t, l.started, "the start to begin")
		c.Close()
		recv(t, l.cancelled, "the start to be cancelled")
		recv(t, ev.handleDone, "the handler to end")
	}
	deadline := time.Now().Add(5 * time.Second)
	for n := runtime.NumGoroutine(); n > baseline; n = runtime.NumGoroutine() {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines %d after 5 disconnects, baseline %d", n, baseline)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// quiescedGoroutines is the goroutine count once it has held still for a
// few polls.
func quiescedGoroutines() int {
	n, stable := runtime.NumGoroutine(), 0
	for stable < 5 {
		time.Sleep(20 * time.Millisecond)
		if m := runtime.NumGoroutine(); m == n {
			stable++
		} else {
			n, stable = m, 0
		}
	}
	return n
}

// The launcher does not spawn for a start whose client already left.
func TestLauncherStartDoesNotSpawnForACancelledContext(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "spawned")
	bin := filepath.Join(dir, "llama-server")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n: > '"+marker+"'\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	reg := rm.NewRegistry()
	l := newLauncher(bin, reg, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := l.Start(ctx, rm.StartInstanceParams{Alias: "a"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start with a cancelled ctx: err = %v, want context.Canceled", err)
	}
	// Spawned and then killed during the health wait would be the old path.
	if strings.Contains(err.Error(), "waitHealthy") {
		t.Fatalf("err = %v: the child was spawned before the cancelled ctx was noticed", err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("llama-server was spawned for a cancelled start")
	}
	if len(reg.List()) != 0 {
		t.Fatalf("registry %+v, want empty", reg.List())
	}
}

// A start whose client leaves just as the child turns healthy is not
// registered: the child is killed, as for any departed client's start.
func TestLauncherStartCancelledAfterHealthIsNotRegistered(t *testing.T) {
	bin := buildFakeLlamaServer(t)
	reg := rm.NewRegistry()
	l := newLauncher(bin, reg, shortTempDir(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l.afterHealthy = cancel
	if _, err := l.Start(ctx, rm.StartInstanceParams{Alias: "a", Model: "a"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start cancelled after health: err = %v, want context.Canceled", err)
	}
	if len(reg.List()) != 0 {
		t.Fatalf("registry %+v, want empty", reg.List())
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.children) != 0 {
		t.Fatalf("children %v, want none tracked", l.children)
	}
}
