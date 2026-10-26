package main

import (
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
)

// Tests never send to a developer's statsd: the sink is a no-op unless a test
// records it.
func init() { countMetric = func(string) {} }

// recordMetrics swaps the sink for a recorder for one test.
func recordMetrics(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var got []string
	old := countMetric
	countMetric = func(name string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, name)
	}
	t.Cleanup(func() { countMetric = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

func TestPreflightCountsAskedAndVerdict(t *testing.T) {
	for _, c := range []struct {
		name, out, exit string
		want            []string
	}{
		{"restart", `{"verdict":"restart","reason":"r","when":"idle"}` + "\n", "0", []string{metricPreflightAsked, metricPreflightVerdict + "restart"}},
		{"none", `{"verdict":"none","reason":"","when":""}` + "\n", "0", []string{metricPreflightAsked, metricPreflightVerdict + "none"}},
		{"refused", `{"verdict":"refused","reason":"x","when":"idle"}` + "\n", "0", []string{metricPreflightAsked, metricPreflightVerdict + "refused"}},
		{"failed", "", "1", []string{metricPreflightAsked, metricPreflightError}},
		{"garbage", "nope\n", "0", []string{metricPreflightAsked, metricPreflightError}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := recordMetrics(t)
			bin, _ := fakeIdleSpinclass(t, c.out, c.exit)
			q := &restartQueue{}
			q.put(restartRequest{When: "idle"})
			var rec recordedRestarts
			newTestIdleRunner(t, bin, q, &rec).preflightQueued()
			if g := got(); !slices.Equal(g, c.want) {
				t.Fatalf("counted %q, want %q", g, c.want)
			}
		})
	}
}

// No queued request, no preflight, no count.
func TestNothingQueuedCountsNothing(t *testing.T) {
	got := recordMetrics(t)
	bin, _ := fakeIdleSpinclass(t, "", "0")
	var rec recordedRestarts
	newTestIdleRunner(t, bin, &restartQueue{}, &rec).preflightQueued()
	if g := got(); len(g) != 0 {
		t.Fatalf("counted %q", g)
	}
}

func TestCancelledRestartIsCounted(t *testing.T) {
	got := recordMetrics(t)
	providerRunning.Store(true)
	t.Cleanup(func() { providerRunning.Store(false) })
	fr := frontendRestarter{
		principal:    restartTestPrincipal,
		profileCheck: func() error { return errors.New("drift") },
	}
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", When: "idle"})
	if g := got(); !slices.Equal(g, []string{metricRestartCancelled}) {
		t.Fatalf("counted %q", g)
	}
}

func TestRelaunchCountsExecAndAbort(t *testing.T) {
	got := recordMetrics(t)
	r := relauncher{
		lookPath: func(string) (string, error) { return "/bin/clown", nil },
		exec:     func(string, []string, []string) error { return errors.New("stub exec failed") },
		stderr:   io.Discard,
	}
	if code := r.relaunch(&relaunchPlan{principal: restartTestPrincipal}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if g := got(); !slices.Equal(g, []string{metricRelaunchExec, metricRelaunchAborted}) {
		t.Fatalf("counted %q", g)
	}
	got2 := recordMetrics(t)
	r.lookPath = func(string) (string, error) { return "", errors.New("none") }
	r.relaunch(&relaunchPlan{principal: restartTestPrincipal})
	if g := got2(); !slices.Equal(g, []string{metricRelaunchAborted}) {
		t.Fatalf("no clown on PATH counted %q", g)
	}
}
