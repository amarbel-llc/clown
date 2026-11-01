package clownname

import (
	"sync"
	"testing"
)

// TestClaimConcurrentCallsDoNotCollide races many goroutines through Claim
// with NO presence records available (XDG_STATE_HOME points at an empty temp
// dir, so jobwake.ListPresence sees nothing live) — every caller sees an
// empty live-set, so without the flock every one of them would independently
// compute the exact same "first free" answer (Pool[0]) and collide. The
// flock's job is serializing them so each successive claim, if it could
// observe the PRIOR claims, would pick the next one — but since Claim never
// persists anything itself (that's the caller's job, via CLOWN_NAME env +
// the presence-registration path), this test only proves the lock itself
// does not deadlock/error under concurrent access; true no-collision
// end-to-end (this call's result actually persisted before the next call's
// read) is exercised by the wiring in cmd/clown/main.go + jobwake, not here.
func TestClaimConcurrentCallsDoNotDeadlock(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	const n = 20
	var wg sync.WaitGroup
	results := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = Claim()
		}(i)
	}
	wg.Wait()

	for i, r := range results {
		if r == "" {
			t.Errorf("Claim() at index %d returned empty; Claim must always return a name", i)
		}
	}
}

func TestClaimDegradesGracefullyWithoutXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	// HOME must resolve for the ~/.local/state fallback; rely on the test
	// environment's real HOME rather than overriding it, since Claim must
	// degrade to SOME name even if home resolution or locking fails, never
	// an empty string or a panic.
	if got := Claim(); got == "" {
		t.Fatal("Claim() must never return an empty name, even in a degraded environment")
	}
}
