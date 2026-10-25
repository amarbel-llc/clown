package permstat

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/toolcost"
)

// A turn boundary declines a pending request once: a second boundary right
// after it (Stop then SessionEnd, Stop then UserPromptSubmit) counts nothing.
func TestTwoBoundariesDeclineOnce(t *testing.T) {
	for _, second := range []string{"SessionEnd", "UserPromptSubmit"} {
		got := replay(t,
			ev("PermissionRequest", "Bash", bash),
			ev("Stop", "", ""),
			ev(second, "", ""),
		)
		want := []string{"clown.permission.builtin.Bash.prompted:1|c", "clown.permission.builtin.Bash.declined:1|c"}
		if !slices.Equal(got, want) {
			t.Fatalf("Stop then %s: lines = %q, want %q", second, got, want)
		}
	}
}

// The pairing key is the tool input as JSON, not its bytes: the same input
// with its keys reordered or spaced differently still pairs.
func TestPairingIgnoresKeyOrderAndWhitespace(t *testing.T) {
	got := replay(t,
		ev("PermissionRequest", "Bash", `{"command":"touch X","description":"d"}`),
		ev("PostToolUse", "Bash", `{ "description" : "d",
			"command":"touch X" }`),
		ev("Stop", "", ""),
	)
	want := []string{"clown.permission.builtin.Bash.prompted:1|c", "clown.permission.builtin.Bash.approved:1|c"}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}

// A PostToolUse with nothing pending (an auto-allowed call) creates no state
// file.
func TestAnAutoAllowedCallCreatesNoStateFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := Handle(ev("PostToolUse", "Read", `{"file_path":"a"}`), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath(dir, "s-1")); !os.IsNotExist(err) {
		t.Fatalf("state file created for an auto-allowed call: %v", err)
	}
}

// State files of sessions killed before their turn ended are swept once
// they are old; recent ones stay.
func TestStaleStateFilesAreSwept(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "0000old.json")
	recent := filepath.Join(dir, "0000new.json")
	for _, p := range []string{old, recent} {
		if err := os.WriteFile(p, []byte(`{"version":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if _, err := Handle(ev("PermissionRequest", "Bash", bash), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("a 48h-old state file was not swept: %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("a recent state file was swept: %v", err)
	}
}

// A symlink planted as the state directory is refused, and nothing is
// written through it.
func TestASymlinkedStateDirIsRefused(t *testing.T) {
	target := t.TempDir()
	dir := filepath.Join(t.TempDir(), "permstat")
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Handle(ev("PermissionRequest", "Bash", bash), dir); err == nil {
		t.Fatal("a symlinked state dir must be refused")
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Fatalf("wrote through the symlinked dir: %v", entries)
	}
}

// A state file whose lock is held elsewhere does not stall the hook: it
// gives up after a short wait and skips counting (prompted still counts).
func TestAHeldLockDoesNotStall(t *testing.T) {
	dir := t.TempDir()
	if _, err := Handle(ev("PermissionRequest", "Bash", bash), dir); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(statePath(dir, "s-1"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"PermissionRequest", "PostToolUse", "Stop"} {
		start := time.Now()
		done := make(chan []toolcost.Metric, 1)
		go func() {
			ms, _ := Handle(ev(e, "Bash", bash), dir)
			done <- ms
		}()
		select {
		case ms := <-done:
			if took := time.Since(start); took > 2*time.Second {
				t.Fatalf("%s took %v under a held lock", e, took)
			}
			if e == "PermissionRequest" && (len(ms) != 1 || !strings.HasSuffix(ms[0].Name, ".prompted")) {
				t.Fatalf("PermissionRequest under a held lock = %v, want prompted still counted", ms)
			}
		case <-time.After(4 * time.Second):
			t.Fatalf("%s stalled on a held lock", e)
		}
	}
}

// Concurrent requests and turn boundaries: every request is declined
// exactly once, none lost to a removed file and none counted twice.
func TestConcurrentRequestsAndBoundariesCountEachRequestOnce(t *testing.T) {
	for iter := 0; iter < 200; iter++ {
		dir := t.TempDir()
		const requests = 8
		var mu sync.Mutex
		prompted, declined := 0, 0
		count := func(ms []toolcost.Metric) {
			mu.Lock()
			defer mu.Unlock()
			for _, m := range ms {
				switch {
				case strings.HasSuffix(m.Name, ".prompted"):
					prompted++
				case strings.HasSuffix(m.Name, ".declined"):
					declined++
				}
			}
		}
		var wg sync.WaitGroup
		for i := 0; i < requests; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				ms, _ := Handle(ev("PermissionRequest", "Bash", fmt.Sprintf(`{"command":"echo %d"}`, i)), dir)
				count(ms)
			}(i)
		}
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ms, _ := Handle(ev("Stop", "", ""), dir)
				count(ms)
			}()
		}
		wg.Wait()
		ms, _ := Handle(ev("Stop", "", ""), dir)
		count(ms)
		if prompted != requests || declined != requests {
			t.Fatalf("iteration %d: prompted %d, declined %d; want %d each", iter, prompted, declined, requests)
		}
	}
}
