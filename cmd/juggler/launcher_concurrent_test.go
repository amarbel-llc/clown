package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
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

func (b *healthBarrier) afterHealthy(context.Context, *child) {
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

// releasedByDeadline reports whether a start was let go by the deadline
// rather than by every start arriving or returning.
func (b *healthBarrier) releasedByDeadline() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.expired && b.arrived+b.returned < b.n
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
	if barrier.releasedByDeadline() {
		t.Fatal("the health barrier was released by its deadline, not by every start")
	}

	var winner rm.Instance
	wins := 0
	for o := range outcomes {
		if o.err == nil {
			wins++
			winner = o.in
			continue
		}
		if msg := o.err.Error(); msg != `alias "a" is already starting` && msg != `alias "a" is already running` {
			t.Errorf("losing start: err = %q, want alias \"a\" is already starting (or running)", msg)
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

// startAndStop checks that alias can be started and stopped again, so no
// earlier start left it stuck.
func startAndStop(t *testing.T, l *llauncher, alias string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	in, err := l.Start(ctx, rm.StartInstanceParams{Alias: alias, Model: alias})
	if err != nil {
		t.Fatalf("retry Start %s: %v", alias, err)
	}
	if err := l.Stop(ctx, alias); err != nil {
		t.Fatalf("Stop %s: %v", alias, err)
	}
	if processAlive(in.PID) {
		t.Fatalf("pid %d still running after Stop", in.PID)
	}
	if _, ok := l.reg.Get(alias); ok {
		t.Fatalf("alias %s still registered after Stop", alias)
	}
}

func assertNothingTracked(t *testing.T, l *llauncher) {
	t.Helper()
	if got := l.reg.List(); len(got) != 0 {
		t.Errorf("registry %+v, want empty", got)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.children) != 0 || len(l.starting) != 0 {
		t.Errorf("children %v, starting %v, want both empty", l.children, l.starting)
	}
}

// A child that dies after turning healthy but before it is registered is
// not registered: the alias is not left "running" with a dead child.
func TestLauncherChildDeadBeforeRegisteringIsNotRegistered(t *testing.T) {
	bin := buildFakeLlamaServer(t)
	l := newLauncher(bin, rm.NewRegistry(), shortTempDir(t))
	l.afterHealthy = func(_ context.Context, ch *child) {
		_ = syscall.Kill(-ch.cmd.Process.Pid, syscall.SIGKILL)
		<-ch.exited
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if in, err := l.Start(ctx, rm.StartInstanceParams{Alias: "a", Model: "a"}); err == nil {
		t.Fatalf("Start of a child that died after health succeeded: %+v", in)
	}
	assertNothingTracked(t, l)
	l.afterHealthy = nil
	startAndStop(t, l, "a")
}

// Stop of a child that is already dead still clears the alias.
func TestLauncherStopOfADeadChildClearsIt(t *testing.T) {
	reg := rm.NewRegistry()
	l := newLauncher(buildFakeLlamaServer(t), reg, shortTempDir(t))
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ch := &child{cmd: cmd, exited: make(chan struct{})}
	ch.waitErr = cmd.Wait()
	close(ch.exited)
	l.children["a"] = ch
	if err := reg.Add(rm.Instance{Alias: "a", PID: cmd.Process.Pid}); err != nil {
		t.Fatal(err)
	}
	if err := l.Stop(context.Background(), "a"); err != nil {
		t.Fatalf("Stop of a dead child: %v", err)
	}
	assertNothingTracked(t, l)
}

// An alias still in the registry (Stop's grace window: children cleared,
// registry not yet) is "already running", and nothing is spawned for it.
func TestLauncherStartRefusesAnAliasStillRegistered(t *testing.T) {
	bin, spawnLog := spawnRecordingLlamaServer(t)
	reg := rm.NewRegistry()
	l := newLauncher(bin, reg, shortTempDir(t))
	if err := reg.Add(rm.Instance{Alias: "a"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := l.Start(ctx, rm.StartInstanceParams{Alias: "a", Model: "a"})
	if err == nil || err.Error() != `alias "a" is already running` {
		t.Fatalf("Start of a registered alias: err = %v, want alias \"a\" is already running", err)
	}
	if pids := spawnedPIDs(t, spawnLog); len(pids) != 0 {
		for _, pid := range pids {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		t.Fatalf("spawned %v for an alias that is still registered", pids)
	}
}

// A start that fails releases the alias, so a retry of it succeeds.
func TestLauncherFailedStartsReleaseTheAlias(t *testing.T) {
	fake := buildFakeLlamaServer(t)
	dir := shortTempDir(t)
	neverHealthy := filepath.Join(dir, "never-healthy")
	if err := os.WriteFile(neverHealthy, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := newLauncher(fake, rm.NewRegistry(), dir)

	t.Run("health failure", func(t *testing.T) {
		l.llamaServerPath = neverHealthy
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if _, err := l.Start(ctx, rm.StartInstanceParams{Alias: "a"}); err == nil {
			t.Fatal("Start of a never-healthy child succeeded")
		}
		assertNothingTracked(t, l)
		l.llamaServerPath = fake
		startAndStop(t, l, "a")
	})

	t.Run("cancelled before spawning", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := l.Start(ctx, rm.StartInstanceParams{Alias: "a"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Start with a cancelled ctx: err = %v, want context.Canceled", err)
		}
		assertNothingTracked(t, l)
		startAndStop(t, l, "a")
	})
}
