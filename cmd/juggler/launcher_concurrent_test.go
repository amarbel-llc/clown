package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

// spawnRecordingLlamaServer wraps the fake llama-server in a script that
// records each spawn's pid in the returned file before exec'ing it.
func spawnRecordingLlamaServer(t *testing.T) (bin, spawnLog string) {
	t.Helper()
	fake := buildFakeLlamaServer(t)
	dir := t.TempDir()
	spawnLog = filepath.Join(dir, "spawned")
	bin = filepath.Join(dir, "llama-server")
	script := "#!/bin/sh\necho $$ >> '" + spawnLog + "'\nexec '" + fake + "' \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, spawnLog
}

func spawnedPIDs(t *testing.T, spawnLog string) []int {
	t.Helper()
	data, err := os.ReadFile(spawnLog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, line := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(line)
		if err != nil {
			t.Fatalf("spawn log line %q: %v", line, err)
		}
		pids = append(pids, pid)
	}
	return pids
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// healthBarrier holds every start that turns healthy until n starts have
// either turned healthy or returned without doing so, so starts that would
// race really are all in flight together. A deadline keeps a start that
// never arrives from hanging the test.
type healthBarrier struct {
	mu       sync.Mutex
	cond     *sync.Cond
	n        int
	arrived  int
	returned int
	expired  bool
}

func newHealthBarrier(n int) *healthBarrier {
	b := &healthBarrier{n: n}
	b.cond = sync.NewCond(&b.mu)
	time.AfterFunc(10*time.Second, func() {
		b.mu.Lock()
		b.expired = true
		b.mu.Unlock()
		b.cond.Broadcast()
	})
	return b
}

func (b *healthBarrier) afterHealthy() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.arrived++
	b.cond.Broadcast()
	for b.arrived+b.returned < b.n && !b.expired {
		b.cond.Wait()
	}
}

func (b *healthBarrier) returnedEarly() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.returned++
	b.cond.Broadcast()
}

func (b *healthBarrier) arrivals() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.arrived
}

type startOutcome struct {
	in  rm.Instance
	err error
}

// Concurrent starts of one alias (clown#312): exactly one wins, its child
// stays tracked and running, and the others fail fast without spawning.
func TestLauncherConcurrentStartsOfOneAlias(t *testing.T) {
	const n = 6
	bin, spawnLog := spawnRecordingLlamaServer(t)
	reg := rm.NewRegistry()
	l := newLauncher(bin, reg, shortTempDir(t))
	barrier := newHealthBarrier(n)
	l.afterHealthy = barrier.afterHealthy
	baseline := quiescedGoroutines()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	outcomes := make(chan startOutcome, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in, err := l.Start(ctx, rm.StartInstanceParams{Alias: "a", Model: "a"})
			if err != nil {
				barrier.returnedEarly()
			}
			outcomes <- startOutcome{in, err}
		}()
	}
	wg.Wait()
	close(outcomes)

	var winner rm.Instance
	wins := 0
	for o := range outcomes {
		if o.err == nil {
			wins++
			winner = o.in
			continue
		}
		if !strings.Contains(o.err.Error(), "already") {
			t.Errorf("losing start: err = %v, want an already starting/running error", o.err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d starts succeeded, want exactly 1", wins)
	}
	t.Cleanup(func() { _ = syscall.Kill(-winner.PID, syscall.SIGKILL) })

	if got, ok := reg.Get("a"); !ok || got.PID != winner.PID {
		t.Fatalf("registry entry %+v (present %v), want the winner's pid %d", got, ok, winner.PID)
	}
	l.mu.Lock()
	ch, tracked := l.children["a"]
	l.mu.Unlock()
	if !tracked || ch.cmd.Process.Pid != winner.PID {
		t.Fatalf("tracked child %v (present %v), want the winner's pid %d", ch, tracked, winner.PID)
	}
	if !processAlive(winner.PID) {
		t.Fatalf("winner pid %d is not running", winner.PID)
	}
	if pids := spawnedPIDs(t, spawnLog); len(pids) != 1 || pids[0] != winner.PID {
		for _, pid := range pids {
			if pid != winner.PID && processAlive(pid) {
				t.Errorf("losing start's llama-server pid %d is still running (orphaned)", pid)
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
		t.Fatalf("spawned pids %v, want only the winner's %d", pids, winner.PID)
	}

	if err := l.Stop(ctx, "a"); err != nil {
		t.Fatalf("Stop winner: %v", err)
	}
	if processAlive(winner.PID) {
		t.Fatalf("winner pid %d still running after Stop", winner.PID)
	}
	if len(reg.List()) != 0 {
		t.Fatalf("registry %+v after Stop, want empty", reg.List())
	}
	http.DefaultClient.CloseIdleConnections()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := quiescedGoroutines()
		if got <= baseline {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines: %d after Stop, baseline %d", got, baseline)
		}
	}
}

// Starts of different aliases still run concurrently: every one is in
// flight at once, and all succeed.
func TestLauncherConcurrentStartsOfDifferentAliases(t *testing.T) {
	const n = 4
	bin := buildFakeLlamaServer(t)
	reg := rm.NewRegistry()
	l := newLauncher(bin, reg, shortTempDir(t))
	barrier := newHealthBarrier(n)
	l.afterHealthy = barrier.afterHealthy

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := range n {
		alias := "a" + strconv.Itoa(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := l.Start(ctx, rm.StartInstanceParams{Alias: alias, Model: alias})
			if err != nil {
				barrier.returnedEarly()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("start: %v", err)
		}
	}
	if got := barrier.arrivals(); got != n {
		t.Errorf("%d of %d starts were in flight together", got, n)
	}
	for i := range n {
		if err := l.Stop(ctx, "a"+strconv.Itoa(i)); err != nil {
			t.Errorf("Stop a%d: %v", i, err)
		}
	}
}
