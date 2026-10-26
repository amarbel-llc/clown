package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

// neverHealthyLlamaServer is a llama-server that records its pid and then
// never answers /health, so a start of it stays in the health wait.
func neverHealthyLlamaServer(t *testing.T) (bin, spawnLog string) {
	t.Helper()
	dir := t.TempDir()
	spawnLog = filepath.Join(dir, "spawned")
	bin = filepath.Join(dir, "llama-server")
	script := "#!/bin/sh\necho $$ >> '" + spawnLog + "'\nexec sleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, spawnLog
}

func waitForSpawn(t *testing.T, spawnLog string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if pids := spawnedPIDs(t, spawnLog); len(pids) > 0 {
			return pids[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("llama-server was never spawned")
	return 0
}

// startInBackground runs a Start bounded by a short deadline, so a start
// nothing stops still ends, and returns its error.
func startInBackground(l *llauncher, alias string) <-chan error {
	errc := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := l.Start(ctx, rm.StartInstanceParams{Alias: alias, Model: alias})
		errc <- err
	}()
	return errc
}

func assertPIDsDead(t *testing.T, pids []int) {
	t.Helper()
	for _, pid := range pids {
		if processAlive(pid) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			t.Errorf("llama-server pid %d still running", pid)
		}
	}
}

// Stop during a start's health wait (clown#313) stops that start: Stop
// succeeds, the half-started child is killed, nothing is registered.
func TestLauncherStopDuringTheHealthWaitStopsTheStart(t *testing.T) {
	bin, spawnLog := neverHealthyLlamaServer(t)
	l := newLauncher(bin, rm.NewRegistry(), shortTempDir(t))
	errc := startInBackground(l, "a")
	pid := waitForSpawn(t, spawnLog)

	if err := l.Stop(context.Background(), "a"); err != nil {
		t.Fatalf("Stop of a starting alias: %v", err)
	}
	// Stop returns once the start has killed its child and dropped its
	// reservation; Start's own return follows.
	assertPIDsDead(t, []int{pid})
	assertNothingTracked(t, l)
	assertStoppedWhileStarting(t, errc, "during the health wait")
}

// assertStoppedWhileStarting waits, bounded, for a background start's error
// and checks Stop stopped it at the given point.
func assertStoppedWhileStarting(t *testing.T, errc <-chan error, when string) {
	t.Helper()
	select {
	case err := <-errc:
		if !errors.Is(err, errStoppedWhileStarting) || !strings.Contains(err.Error(), when) {
			t.Fatalf("stopped start: err = %v, want %q stopped while starting", err, when)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stopped start never returned")
	}
}

// Stop after a child turned healthy stops that start: Start's re-check of
// ctx after health sees the cancel, and nothing is registered.
func TestLauncherStopAfterHealthyStopsTheStart(t *testing.T) {
	bin, spawnLog := spawnRecordingLlamaServer(t)
	l := newLauncher(bin, rm.NewRegistry(), shortTempDir(t))
	inHook := make(chan struct{})
	stopReturned := make(chan struct{})
	l.afterHealthy = func(ctx context.Context, _ *child) {
		close(inHook)
		// Hold the start here until Stop has cancelled it (or returned
		// without doing so).
		select {
		case <-ctx.Done():
		case <-stopReturned:
		case <-time.After(10 * time.Second):
		}
	}
	errc := startInBackground(l, "a")
	<-inHook

	stopErr := l.Stop(context.Background(), "a")
	close(stopReturned)
	if stopErr != nil {
		t.Fatalf("Stop of an alias turning healthy: %v", stopErr)
	}
	assertPIDsDead(t, spawnedPIDs(t, spawnLog))
	assertNothingTracked(t, l)
	assertStoppedWhileStarting(t, errc, "after it turned healthy")
}

// register refuses a start cancelled after Start's own checks: Stop
// cancels under l.mu, which register holds while it checks.
func TestLauncherRegisterRefusesACancelledStart(t *testing.T) {
	reg := rm.NewRegistry()
	l := newLauncher(buildFakeLlamaServer(t), reg, shortTempDir(t))
	ctx, cancel := context.WithCancelCause(context.Background())
	ps, err := l.reserveAlias("a", cancel)
	if err != nil {
		t.Fatal(err)
	}
	cancel(errStoppedWhileStarting)
	ch := &child{exited: make(chan struct{})} // live: not exited
	err = l.register(ctx, rm.Instance{Alias: "a"}, ch)
	if !errors.Is(err, errStoppedWhileStarting) || !strings.Contains(err.Error(), "before registering") {
		t.Fatalf("register of a cancelled start: err = %v, want stopped while starting, before registering", err)
	}
	if _, ok := reg.Get("a"); ok {
		t.Error("a cancelled start was registered")
	}
	l.mu.Lock()
	_, tracked := l.children["a"]
	reserved := l.starting["a"] == ps
	l.mu.Unlock()
	if tracked || !reserved {
		t.Errorf("tracked %v, still reserved %v; want untracked and reserved until endStart", tracked, reserved)
	}
	l.endStart("a", ps)
	assertNothingTracked(t, l)
}

// PendingStarts lists the pending aliases sorted, so StopAll's Stopped
// list is stable.
func TestLauncherPendingStartsAreSorted(t *testing.T) {
	l := newLauncher(buildFakeLlamaServer(t), rm.NewRegistry(), shortTempDir(t))
	want := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	for i := len(want) - 1; i >= 0; i-- {
		alias := want[i]
		_, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		if _, err := l.reserveAlias(alias, cancel); err != nil {
			t.Fatal(err)
		}
	}
	for range 5 {
		if got := l.PendingStarts(); !slices.Equal(got, want) {
			t.Fatalf("PendingStarts = %v, want %v", got, want)
		}
	}
}

// stubLauncher answers Stop per alias, and reports pending starts.
type stubLauncher struct {
	pending []string
	stopErr map[string]error
}

func (s *stubLauncher) Start(context.Context, rm.StartInstanceParams) (rm.Instance, error) {
	return rm.Instance{}, errors.New("stub")
}

func (s *stubLauncher) Stop(_ context.Context, alias string) error {
	return s.stopErr[alias]
}

func (s *stubLauncher) PendingStarts() []string {
	return s.pending
}

// StopAll lists only the aliases its own Stops stopped: not a pending
// start that failed on its own first ("not running"), nor one whose start
// did not end in time.
func TestStopAllListsOnlyWhatItStopped(t *testing.T) {
	reg := rm.NewRegistry()
	if err := reg.Add(rm.Instance{Alias: "running"}); err != nil {
		t.Fatal(err)
	}
	l := &stubLauncher{
		pending: []string{"gone", "ok", "wedged"},
		stopErr: map[string]error{
			"gone":   errors.New(`alias "gone" not running`),
			"wedged": errors.New(`alias "wedged": its start did not end within 10s of Stop`),
		},
	}
	s := newServer(reg, l)
	resp := s.dispatch(context.Background(), rm.Envelope{JSONRPC: "2.0", ID: "1", Method: rm.MethodStopAll})
	if resp.Error != nil {
		t.Fatalf("StopAll: %+v", resp.Error)
	}
	var res rm.StopAllResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if want := []string{"ok", "running"}; !slices.Equal(res.Stopped, want) {
		t.Errorf("StopAll stopped %v, want %v", res.Stopped, want)
	}
}

// StopAll stops pending starts as well as registered instances.
func TestStopAllStopsPendingStarts(t *testing.T) {
	bin, spawnLog := neverHealthyLlamaServer(t)
	reg := rm.NewRegistry()
	l := newLauncher(bin, reg, shortTempDir(t))
	s := newServer(reg, l)
	errc := startInBackground(l, "a")
	pid := waitForSpawn(t, spawnLog)

	resp := s.dispatch(context.Background(), rm.Envelope{JSONRPC: "2.0", ID: "1", Method: rm.MethodStopAll})
	if resp.Error != nil {
		t.Fatalf("StopAll: %+v", resp.Error)
	}
	var res rm.StopAllResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Stopped, []string{"a"}) {
		t.Errorf("StopAll stopped %v, want [a]", res.Stopped)
	}
	if err := <-errc; err == nil {
		t.Fatal("the start StopAll stopped succeeded")
	}
	assertPIDsDead(t, []int{pid})
	assertNothingTracked(t, l)
}

// Starts and stops of one alias racing each other leave nothing running,
// tracked or leaked once a final Stop has run.
func TestLauncherStartStopStorm(t *testing.T) {
	bin, spawnLog := spawnRecordingLlamaServer(t)
	l := newLauncher(bin, rm.NewRegistry(), shortTempDir(t))
	// Hold each healthy start briefly, so stops land on pending starts.
	l.afterHealthy = func(ctx context.Context, _ *child) {
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	baseline := quiescedGoroutines()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var stoppedWhileStarting, started atomic.Int64
	startersDone := make(chan struct{})
	var starters, stoppers sync.WaitGroup
	for range 2 {
		starters.Add(1)
		go func() {
			defer starters.Done()
			for range 8 {
				_, err := l.Start(ctx, rm.StartInstanceParams{Alias: "a", Model: "a"})
				switch {
				case err == nil:
					started.Add(1)
				case errors.Is(err, errStoppedWhileStarting):
					stoppedWhileStarting.Add(1)
				}
			}
		}()
		stoppers.Add(1)
		go func() {
			defer stoppers.Done()
			for {
				_ = l.Stop(ctx, "a")
				select {
				case <-startersDone:
					return
				case <-time.After(30 * time.Millisecond):
				}
			}
		}()
	}
	starters.Wait()
	close(startersDone)
	stoppers.Wait()
	_ = l.Stop(ctx, "a")
	if stoppedWhileStarting.Load() == 0 {
		t.Errorf("no start was stopped while starting (%d started): the storm never interleaved", started.Load())
	}

	assertNothingTracked(t, l)
	assertPIDsDead(t, spawnedPIDs(t, spawnLog))
	http.DefaultClient.CloseIdleConnections()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := quiescedGoroutines()
		if got <= baseline {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines: %d after the storm, baseline %d", got, baseline)
		}
	}
}
