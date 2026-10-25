package permstat

import (
	"encoding/json"
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

// isLockTimeout reports the one error the concurrency test tolerates: a hook
// that gave up waiting for the lock (by design, it skips the bookkeeping).
func isLockTimeout(err error) bool {
	return err != nil && strings.Contains(err.Error(), "locked for over")
}

// Concurrent requests and turn boundaries: every request is declined
// exactly once, none lost to a removed file and none counted twice. The only
// error tolerated is a lock timeout; a request that timed out is not pending,
// so it is not declined either.
func TestConcurrentRequestsAndBoundariesCountEachRequestOnce(t *testing.T) {
	timeouts := 0
	for iter := 0; iter < 200; iter++ {
		dir := t.TempDir()
		const requests = 8
		var mu sync.Mutex
		prompted, declined, lost := 0, 0, 0
		var errs []error
		count := func(ms []toolcost.Metric, err error, request bool) {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case isLockTimeout(err):
				timeouts++
				if request {
					lost++
				}
			case err != nil:
				errs = append(errs, err)
			}
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
				ms, err := Handle(ev("PermissionRequest", "Bash", fmt.Sprintf(`{"command":"echo %d"}`, i)), dir)
				count(ms, err, true)
			}(i)
		}
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ms, err := Handle(ev("Stop", "", ""), dir)
				count(ms, err, false)
			}()
		}
		wg.Wait()
		ms, err := Handle(ev("Stop", "", ""), dir)
		if err != nil {
			t.Fatalf("iteration %d: the final Stop, with nothing else running: %v", iter, err)
		}
		count(ms, nil, false)
		if len(errs) != 0 {
			t.Fatalf("iteration %d: errors other than a lock timeout: %v", iter, errs)
		}
		if prompted != requests || declined != requests-lost {
			t.Fatalf("iteration %d: prompted %d, declined %d, lost to a lock timeout %d; want %d prompted, %d declined", iter, prompted, declined, lost, requests, requests-lost)
		}
	}
	if timeouts != 0 {
		t.Logf("lock timeouts tolerated: %d", timeouts)
	}
}

// A hook waiting on the lock of a file that is then replaced at its path
// (renamed over) works on the file the path now names, not the old one.
func TestAReplacedFileIsReopened(t *testing.T) {
	dir := t.TempDir()
	if _, err := Handle(ev("PermissionRequest", "Bash", bash), dir); err != nil {
		t.Fatal(err)
	}
	path := statePath(dir, "s-1")
	old, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(old.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan []toolcost.Metric, 1)
	go func() {
		ms, _ := Handle(ev("PostToolUse", "Bash", bash), dir)
		done <- ms
	}()
	// Let the hook open the old file and start waiting on its lock, then put
	// a copy in its place and release the old one.
	time.Sleep(50 * time.Millisecond)
	tmp := filepath.Join(dir, "replacement.tmp")
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	old.Close()
	var ms []toolcost.Metric
	select {
	case ms = <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("PostToolUse stalled")
	}
	if len(ms) != 1 || !strings.HasSuffix(ms[0].Name, ".approved") {
		t.Fatalf("PostToolUse = %v, want approved", ms)
	}
	// The approval came out of the file at the path, so nothing is left there.
	if ms, err := Handle(ev("Stop", "", ""), dir); err != nil || len(ms) != 0 {
		t.Fatalf("Stop = %v, %v; want nothing declined (the request was approved in the file the path names)", ms, err)
	}
}

// writeAged writes a state file with the given pending count, last
// modified age ago.
func writeAged(t *testing.T, path string, pendingN int, age time.Duration) {
	t.Helper()
	st := state{Version: stateVersion}
	for i := 0; i < pendingN; i++ {
		st.Pending = append(st.Pending, pending{Key: "builtin.Bash", Match: fmt.Sprint(i)})
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// mtime is the last write, not liveness: a person can sit at a dialog for a
// day. A file still holding a pending request is swept only past 7 days; an
// empty one past 24h.
func TestSweepKeepsPendingRequestsForAWeek(t *testing.T) {
	dir := t.TempDir()
	awayADay := filepath.Join(dir, "0000away.json")
	abandoned := filepath.Join(dir, "0000abandoned.json")
	emptyOld := filepath.Join(dir, "0000empty.json")
	writeAged(t, awayADay, 1, 48*time.Hour)
	writeAged(t, abandoned, 1, 8*24*time.Hour)
	writeAged(t, emptyOld, 0, 48*time.Hour)
	if _, err := Handle(ev("PermissionRequest", "Bash", bash), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(awayADay); err != nil {
		t.Fatalf("a 2-day-old file with a pending request was swept: %v", err)
	}
	for _, p := range []string{abandoned, emptyOld} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s was not swept: %v", filepath.Base(p), err)
		}
	}
}

// One sweep removes at most 16 files.
func TestSweepRemovesAtMost16(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		writeAged(t, filepath.Join(dir, fmt.Sprintf("old%02d.json", i)), 0, 48*time.Hour)
	}
	if _, err := Handle(ev("PermissionRequest", "Bash", bash), dir); err != nil {
		t.Fatal(err)
	}
	left := 0
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("old%02d.json", i))); err == nil {
			left++
		}
	}
	if left != 4 {
		t.Fatalf("%d of 20 old files left, want 4 (16 swept)", left)
	}
}

// The sweep looks past the first batch of directory entries.
func TestSweepReadsPastTheFirstBatch(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 1000; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("recent%04d.json", i)), []byte(`{"version":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := filepath.Join(dir, "zzzold.json")
	writeAged(t, old, 0, 48*time.Hour)
	if _, err := Handle(ev("PermissionRequest", "Bash", bash), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("an old file among 1000 entries was not swept: %v", err)
	}
}

// Odd tool input (duplicate keys, invalid UTF-8) neither panics nor breaks
// pairing.
func TestOddToolInputPairs(t *testing.T) {
	for name, input := range map[string]string{
		"duplicate keys": `{"a":1,"a":2}`,
		"invalid utf-8":  "{\"a\":\"\xff\xfe\"}",
	} {
		raw := func(event string) []byte {
			return []byte(`{"hook_event_name":"` + event + `","session_id":"s-1","tool_name":"Bash","tool_input":` + input + `}`)
		}
		got := replay(t, raw("PermissionRequest"), raw("PostToolUse"), ev("Stop", "", ""))
		want := []string{"clown.permission.builtin.Bash.prompted:1|c", "clown.permission.builtin.Bash.approved:1|c"}
		if !slices.Equal(got, want) {
			t.Fatalf("%s: lines = %q, want %q", name, got, want)
		}
	}
}
