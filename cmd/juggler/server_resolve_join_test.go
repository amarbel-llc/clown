package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	rm "code.linenisgreat.com/clown/internal/0/juggler"
)

// clown#315: ResolveModel of a local alias whose Start is in progress joins
// that Start instead of failing "already starting"; StartInstance still
// fails fast.

// joinSignallingLauncher is the real launcher, telling the test when a
// ResolveModel has begun to wait on a pending Start.
type joinSignallingLauncher struct {
	*llauncher
	waiting chan struct{}
}

func (j joinSignallingLauncher) WaitStarting(ctx context.Context, alias string) (error, error) {
	select {
	case j.waiting <- struct{}{}:
	default:
	}
	return j.llauncher.WaitStarting(ctx, alias)
}

// joinFixture is a server over a real launcher whose first Start is held,
// once healthy, in hold until the test lets it go.
type joinFixture struct {
	s       *server
	l       *llauncher
	reg     *rm.Registry
	waiting chan struct{}
	spawns  string
}

func newJoinFixture(t *testing.T, hold func(context.Context, *child)) joinFixture {
	t.Helper()
	bin, spawns := spawnRecordingLlamaServer(t)
	modelsDir := shortTempDir(t)
	if err := os.WriteFile(filepath.Join(modelsDir, "m.gguf"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := rm.NewRegistry()
	l := newLauncher(bin, reg, modelsDir)
	var once sync.Once
	l.afterHealthy = func(ctx context.Context, ch *child) { once.Do(func() { hold(ctx, ch) }) }
	waiting := make(chan struct{}, 1)
	s := newServer(reg, joinSignallingLauncher{llauncher: l, waiting: waiting})
	s.modelsDir = modelsDir
	s.remoteModelsPath = filepath.Join(shortTempDir(t), "models.toml")
	t.Cleanup(func() {
		for _, pid := range spawnedPIDs(t, spawns) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
	return joinFixture{s: s, l: l, reg: reg, waiting: waiting, spawns: spawns}
}

func (f joinFixture) resolve(ctx context.Context, id json.Number) rm.Envelope {
	return f.s.dispatch(ctx, rm.Envelope{
		JSONRPC: "2.0", ID: id, Method: rm.MethodResolveModel,
		Params: mustJSONNoT(rm.ResolveModelParams{Name: "m"}),
	})
}

func mustJSONNoT(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func resolvedURL(t *testing.T, resp rm.Envelope) string {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("ResolveModel error: %+v", resp.Error)
	}
	var res rm.ResolveModelResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.Kind != rm.ModelKindLocal || res.URL == "" {
		t.Fatalf("res = %+v", res)
	}
	return res.URL
}

// joined waits for the second resolve to be waiting on the first Start. It
// runs in the launcher's goroutine, so it reports rather than failing; the
// test checks the result.
func joined(waiting <-chan struct{}) bool {
	select {
	case <-waiting:
		return true
	case <-time.After(10 * time.Second):
		return false
	}
}

func awaitJoin(t *testing.T, waiting <-chan struct{}) {
	t.Helper()
	if !joined(waiting) {
		t.Fatal("the second ResolveModel never waited on the pending start")
	}
}

// Two resolves of one local model: the second joins the first's Start and
// both get its URL, with one llama-server spawned.
func TestResolveModelJoinsAPendingStart(t *testing.T) {
	release := make(chan struct{})
	f := newJoinFixture(t, func(context.Context, *child) { <-release })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	first := make(chan rm.Envelope, 1)
	go func() { first <- f.resolve(ctx, "1") }()
	second := make(chan rm.Envelope, 1)
	waitUntil(t, func() bool { return len(f.l.PendingStarts()) == 1 })
	go func() { second <- f.resolve(ctx, "2") }()
	awaitJoin(t, f.waiting)
	close(release)

	u1, u2 := resolvedURL(t, <-first), resolvedURL(t, <-second)
	if u1 != u2 {
		t.Fatalf("URLs %q and %q, want the one instance's", u1, u2)
	}
	if pids := spawnedPIDs(t, f.spawns); len(pids) != 1 {
		t.Fatalf("spawned %v, want one llama-server", pids)
	}
	if err := f.l.Stop(ctx, "m"); err != nil {
		t.Fatal(err)
	}
}

// A joined Start that fails gives its error to the joiner, which starts
// nothing of its own.
func TestResolveModelJoinerGetsTheStartsError(t *testing.T) {
	var f joinFixture
	var didJoin bool
	f = newJoinFixture(t, func(_ context.Context, ch *child) {
		didJoin = joined(f.waiting)
		_ = syscall.Kill(-ch.cmd.Process.Pid, syscall.SIGKILL)
		<-ch.exited
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	first := make(chan rm.Envelope, 1)
	go func() { first <- f.resolve(ctx, "1") }()
	waitUntil(t, func() bool { return len(f.l.PendingStarts()) == 1 })
	resp := f.resolve(ctx, "2")
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "exited after turning healthy") {
		t.Fatalf("joiner resp = %+v, want the first start's error", resp)
	}
	if r := <-first; r.Error == nil {
		t.Fatalf("first resp = %+v, want its start's error", r)
	}
	if !didJoin {
		t.Fatal("the second ResolveModel never waited on the pending start")
	}
	if pids := spawnedPIDs(t, f.spawns); len(pids) != 1 {
		t.Fatalf("spawned %v, want only the first start's", pids)
	}
	assertNothingTracked(t, f.l)
}

// A joined Start whose own client left is not the joiner's failure: the
// joiner starts the model itself.
func TestResolveModelJoinerRestartsAStartItsClientLeft(t *testing.T) {
	firstCtx, leave := context.WithCancel(context.Background())
	defer leave()
	var f joinFixture
	var didJoin bool
	f = newJoinFixture(t, func(context.Context, *child) {
		didJoin = joined(f.waiting)
		leave()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	first := make(chan rm.Envelope, 1)
	go func() { first <- f.resolve(firstCtx, "1") }()
	waitUntil(t, func() bool { return len(f.l.PendingStarts()) == 1 })
	resolvedURL(t, f.resolve(ctx, "2"))
	if r := <-first; r.Error == nil {
		t.Fatalf("first resp = %+v, want its cancelled start's error", r)
	}
	if !didJoin {
		t.Fatal("the second ResolveModel never waited on the pending start")
	}
	if pids := spawnedPIDs(t, f.spawns); len(pids) != 2 {
		t.Fatalf("spawned %v, want the abandoned start and the joiner's own", pids)
	}
	if err := f.l.Stop(ctx, "m"); err != nil {
		t.Fatal(err)
	}
}

// StartInstance of an alias whose Start is in progress still fails at once.
func TestStartInstanceStillFailsFastWhileStarting(t *testing.T) {
	release := make(chan struct{})
	f := newJoinFixture(t, func(context.Context, *child) { <-release })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	first := make(chan rm.Envelope, 1)
	go func() { first <- f.resolve(ctx, "1") }()
	waitUntil(t, func() bool { return len(f.l.PendingStarts()) == 1 })
	resp := f.s.dispatch(ctx, rm.Envelope{
		JSONRPC: "2.0", ID: "2", Method: rm.MethodStartInstance,
		Params: mustJSONNoT(rm.StartInstanceParams{Alias: "m", Model: "m"}),
	})
	close(release)
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "already starting") {
		t.Fatalf("StartInstance resp = %+v, want already starting", resp)
	}
	resolvedURL(t, <-first)
	if err := f.l.Stop(ctx, "m"); err != nil {
		t.Fatal(err)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached within 10s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
