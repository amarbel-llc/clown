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
	"sync"
	"testing"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

// clown#82: a client that disconnects cancels its in-flight, client-scoped
// call (a start's wait for health, a download); a stop always completes.

// blockingLauncher holds Start until release or ctx is done, and Stop until
// release, and reports what each saw of its ctx.
type blockingLauncher struct {
	started   chan struct{}
	cancelled chan error // one per Start that saw its ctx done
	stopping  chan struct{}
	stopped   chan error // Stop's ctx.Err() once released
	release   chan struct{}
	once      sync.Once
}

func newBlockingLauncher() *blockingLauncher {
	return &blockingLauncher{
		started:   make(chan struct{}, 16),
		cancelled: make(chan error, 16),
		stopping:  make(chan struct{}, 16),
		stopped:   make(chan error, 16),
		release:   make(chan struct{}),
	}
}

func (b *blockingLauncher) releaseAll() { b.once.Do(func() { close(b.release) }) }

func (b *blockingLauncher) Start(ctx context.Context, p rm.StartInstanceParams) (rm.Instance, error) {
	b.started <- struct{}{}
	select {
	case <-ctx.Done():
		b.cancelled <- ctx.Err()
		return rm.Instance{}, ctx.Err()
	case <-b.release:
		return rm.Instance{Alias: p.Alias, Port: 1, Bind: "127.0.0.1"}, nil
	}
}

func (b *blockingLauncher) Stop(ctx context.Context, alias string) error {
	b.stopping <- struct{}{}
	<-b.release
	b.stopped <- ctx.Err()
	return nil
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

func sendRaw(t *testing.T, sock, method string, params any) net.Conn {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.WriteFrame(c, rm.Envelope{JSONRPC: "2.0", ID: "1", Method: method, Params: mustJSON(t, params)}); err != nil {
		t.Fatal(err)
	}
	return c
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
	c := sendRaw(t, sock, rm.MethodStartInstance, rm.StartInstanceParams{Alias: "a", Model: "a"})
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
	c := sendRaw(t, sock, rm.MethodResolveModel, rm.ResolveModelParams{Name: "m"})
	recv(t, l.started, "the resolve's start to begin")
	c.Close()
	recv(t, l.cancelled, "the resolve's start to be cancelled after the disconnect")
}

// A stop is not client-scoped: it completes, uncancelled, even when the
// client leaves while it runs.
func TestDisconnectDoesNotCancelAStop(t *testing.T) {
	l := newBlockingLauncher()
	sock := serveWith(t, newServer(rm.NewRegistry(), l))
	c := sendRaw(t, sock, rm.MethodStopInstance, rm.StopInstanceParams{Alias: "a"})
	recv(t, l.stopping, "the stop to begin")
	c.Close()
	// Give a cancellation, were there one, time to land before release.
	time.Sleep(200 * time.Millisecond)
	l.releaseAll()
	if err := recv(t, l.stopped, "the stop to finish"); err != nil {
		t.Fatalf("stop ctx err = %v, want a stop that is never cancelled", err)
	}
}

// A connection that stays open gets its answers, and pipelined requests on
// one connection are answered in order.
func TestOpenConnectionRequestsAreUnaffected(t *testing.T) {
	l := newBlockingLauncher()
	l.releaseAll()
	sock := serveWith(t, newServer(rm.NewRegistry(), l))
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, id := range []string{"1", "2"} {
		if err := rm.WriteFrame(c, rm.Envelope{JSONRPC: "2.0", ID: json.Number(id), Method: rm.MethodStartInstance, Params: mustJSON(t, rm.StartInstanceParams{Alias: "a" + id})}); err != nil {
			t.Fatal(err)
		}
	}
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
	select {
	case err := <-l.cancelled:
		t.Fatalf("a start on an open connection was cancelled: %v", err)
	default:
	}
}

// The goroutines of disconnected connections end.
func TestDisconnectLeavesNoGoroutines(t *testing.T) {
	l := newBlockingLauncher()
	t.Cleanup(l.releaseAll)
	sock := serveWith(t, newServer(rm.NewRegistry(), l))
	// Let Serve's own goroutines settle before counting.
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	for range 5 {
		c := sendRaw(t, sock, rm.MethodStartInstance, rm.StartInstanceParams{Alias: "a"})
		recv(t, l.started, "the start to begin")
		c.Close()
		recv(t, l.cancelled, "the start to be cancelled")
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines %d after 5 disconnects, want at most %d", runtime.NumGoroutine(), before)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
