package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
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
	// Stop returns after the start has ended: its error is already there.
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("the stopped start succeeded")
		}
	default:
		t.Fatal("Stop returned before the start it stopped had ended")
	}
	assertPIDsDead(t, []int{pid})
	assertNothingTracked(t, l)
}

// Stop after a child turned healthy but before it is registered stops
// that start too: it is not registered after Stop returned.
func TestLauncherStopBeforeRegisteringStopsTheStart(t *testing.T) {
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
	if err := <-errc; err == nil {
		t.Fatal("the stopped start succeeded")
	}
	assertPIDsDead(t, spawnedPIDs(t, spawnLog))
	assertNothingTracked(t, l)
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
	baseline := quiescedGoroutines()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 8 {
				if g%2 == 0 {
					_, _ = l.Start(ctx, rm.StartInstanceParams{Alias: "a", Model: "a"})
				} else {
					_ = l.Stop(ctx, "a")
					time.Sleep(50 * time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()
	_ = l.Stop(ctx, "a")

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
