package sessionstatus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func readStatus(t *testing.T, path string) Status {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st Status
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("status %s: %v\n%s", path, err, b)
	}
	return st
}

// The file is $XDG_STATE_HOME/clown/status/<hex sha256(principal)>.json.
func TestPathIsTheHashOfThePrincipal(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	sum := sha256.Sum256([]byte("clown-abc"))
	want := filepath.Join(state, "clown", "status", hex.EncodeToString(sum[:])+".json")
	got, err := Path("clown-abc")
	if err != nil || got != want {
		t.Fatalf("Path = %q, %v; want %q", got, err, want)
	}
}

func newTestWriter(t *testing.T) (*Writer, string, *[]string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// Tests read the file after each call: no coalescing unless one asks.
	old := flushInterval
	flushInterval = 0
	t.Cleanup(func() { flushInterval = old })
	var mu sync.Mutex
	logs := &[]string{}
	w, err := Open("p-1", "repo/branch", func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		*logs = append(*logs, fmt.Sprintf(format, args...))
	})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := Path("p-1")
	return w, path, logs
}

// The start write records the frontend's identity and no hook yet; it lands
// as 0600 in a 0700 dir.
func TestStartWritesAnIdleStatus(t *testing.T) {
	w, path, _ := newTestWriter(t)
	w.Start()
	st := readStatus(t, path)
	if st.Version != 1 || st.Principal != "p-1" || st.SpinclassSessionKey != "repo/branch" {
		t.Fatalf("identity fields: %+v", st)
	}
	if st.TurnRunning || st.TurnStartedAt != "" || st.LastHookAt != "" || st.Unit != "" || st.MainPID != 0 || st.TranscriptPath != "" || st.ClaudeSessionID != "" {
		t.Fatalf("a fresh status must claim nothing: %+v", st)
	}
	if st.FrontendPID != os.Getpid() {
		t.Fatalf("frontend_pid %d, want %d", st.FrontendPID, os.Getpid())
	}
	if _, err := time.Parse(time.RFC3339Nano, st.WrittenAt); err != nil {
		t.Fatalf("written_at %q: %v", st.WrittenAt, err)
	}
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Fatalf("file mode %v, %v", fi.Mode(), err)
	}
	di, err := os.Lstat(filepath.Dir(path))
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v, %v", di.Mode(), err)
	}
}

// frontend_start_ticks is field 22 of /proc/<pid>/stat.
func TestStartTicksMatchesProcSelfStat(t *testing.T) {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Skip("no /proc on this host")
	}
	s := string(b)
	fields := strings.Fields(s[strings.LastIndexByte(s, ')')+2:])
	want, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || want == 0 {
		t.Fatalf("test parse of %q: %v", s, err)
	}
	if got := StartTicks(os.Getpid()); got != want {
		t.Fatalf("StartTicks = %d, want %d", got, want)
	}
	w, path, _ := newTestWriter(t)
	w.Start()
	if st := readStatus(t, path); st.FrontendStartTicks != want {
		t.Fatalf("frontend_start_ticks %d, want %d", st.FrontendStartTicks, want)
	}
	if StartTicks(-1) != 0 {
		t.Fatal("an unknown pid must give 0")
	}
}

// A turn start sets turn_running, turn_started_at and last_hook_at and takes
// the payload; the idle hook clears turn_running, keeps turn_started_at and
// moves last_hook_at.
func TestTurnStartThenIdle(t *testing.T) {
	w, path, _ := newTestWriter(t)
	w.Start()
	w.TurnStarted(Payload{TranscriptPath: "/home/u/.claude/projects/x/s.jsonl", SessionID: "sess-1"})
	busy := readStatus(t, path)
	if !busy.TurnRunning || busy.TurnStartedAt == "" || busy.LastHookAt != busy.TurnStartedAt {
		t.Fatalf("busy: %+v", busy)
	}
	if busy.TranscriptPath != "/home/u/.claude/projects/x/s.jsonl" || busy.ClaudeSessionID != "sess-1" {
		t.Fatalf("payload not recorded: %+v", busy)
	}
	time.Sleep(2 * time.Millisecond)
	w.TurnEnded()
	idle := readStatus(t, path)
	if idle.TurnRunning || idle.TurnStartedAt != busy.TurnStartedAt || idle.LastHookAt <= busy.LastHookAt {
		t.Fatalf("idle: %+v (busy %+v)", idle, busy)
	}
	if idle.TranscriptPath != busy.TranscriptPath || idle.ClaudeSessionID != "sess-1" {
		t.Fatalf("idle must keep the last payload: %+v", idle)
	}
	// A later turn without a payload keeps the last known transcript.
	w.TurnStarted(Payload{})
	if again := readStatus(t, path); again.TranscriptPath != busy.TranscriptPath || !again.TurnRunning {
		t.Fatalf("payload-less turn: %+v", again)
	}
}

// The unit and main pid are written when they become known.
func TestUnitAndMainPIDAreWritten(t *testing.T) {
	w, path, _ := newTestWriter(t)
	w.Start()
	w.SetUnit("clown-agent-x.service")
	if st := readStatus(t, path); st.Unit != "clown-agent-x.service" || st.MainPID != 0 {
		t.Fatalf("after SetUnit: %+v", st)
	}
	w.SetMainPID(4242)
	if st := readStatus(t, path); st.Unit != "clown-agent-x.service" || st.MainPID != 4242 {
		t.Fatalf("after SetMainPID: %+v", st)
	}
}

// Remove deletes the file, and nothing writes it again afterwards.
func TestRemoveDeletesAndStopsWrites(t *testing.T) {
	w, path, _ := newTestWriter(t)
	w.Start()
	w.Remove()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("after Remove: %v", err)
	}
	w.TurnEnded()
	w.SetMainPID(1)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a write after Remove recreated the file: %v", err)
	}
}

// A nil writer (not armed) does nothing.
func TestNilWriterIsANoOp(t *testing.T) {
	var w *Writer
	w.Start()
	w.TurnStarted(Payload{})
	w.TurnEnded()
	w.SetUnit("u")
	w.SetMainPID(1)
	w.Remove()
}

// A reader never sees a partial file: every read while writes run parses.
func TestReaderNeverSeesAPartialFile(t *testing.T) {
	w, path, _ := newTestWriter(t)
	w.Start()
	long := "/" + strings.Repeat("t", 3000)
	var stop atomic.Bool
	var reads, bad atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			b, err := os.ReadFile(path)
			if err != nil {
				bad.Add(1)
				continue
			}
			var st Status
			if json.Unmarshal(b, &st) != nil || st.Version != 1 {
				bad.Add(1)
			}
			reads.Add(1)
		}
	}()
	for i := 0; i < 500; i++ {
		w.TurnStarted(Payload{TranscriptPath: long, SessionID: strconv.Itoa(i)})
		w.TurnEnded()
	}
	stop.Store(true)
	wg.Wait()
	if bad.Load() != 0 || reads.Load() == 0 {
		t.Fatalf("reads %d, missing or partial %d", reads.Load(), bad.Load())
	}
}

// A flood of turn hooks is coalesced: a few writes, not one per call, and the
// last state still lands.
func TestFloodOfTurnHooksIsCoalesced(t *testing.T) {
	w, path, _ := newTestWriter(t)
	flushInterval = 50 * time.Millisecond
	w.Start()
	var writes atomic.Int32
	stop := make(chan struct{})
	go func() {
		var last time.Time
		for {
			select {
			case <-stop:
				return
			default:
			}
			if fi, err := os.Stat(path); err == nil && fi.ModTime() != last {
				last = fi.ModTime()
				writes.Add(1)
			}
			time.Sleep(time.Millisecond)
		}
	}()
	start := time.Now()
	n := 0
	for time.Since(start) < 300*time.Millisecond {
		w.TurnStarted(Payload{SessionID: "s"})
		w.TurnEnded()
		n++
	}
	w.TurnStarted(Payload{SessionID: "final"})
	deadline := time.Now().Add(5 * time.Second)
	for readStatus(t, path).ClaudeSessionID != "final" {
		if time.Now().After(deadline) {
			t.Fatal("the last change never landed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	if got := writes.Load(); got > 20 || n < 100 {
		t.Fatalf("observed %d file replacements for %d hook pairs; want a handful", got, n)
	}
}

// Remove cancels a pending coalesced write: the file stays gone.
func TestRemoveCancelsAPendingWrite(t *testing.T) {
	w, path, _ := newTestWriter(t)
	flushInterval = 50 * time.Millisecond
	w.Start()
	w.TurnStarted(Payload{})
	w.Remove()
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a pending write recreated the file: %v", err)
	}
}

// Field 22 is found after a comm holding spaces and ')'.
func TestParseStartTicks(t *testing.T) {
	line := "123 (a b) c)) S 1 123 123 0 -1 4194560 1 0 0 0 0 0 0 0 20 0 1 0 987654 1000 1 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0"
	if got := parseStartTicks(line); got != 987654 {
		t.Fatalf("parseStartTicks = %d, want 987654", got)
	}
	for _, bad := range []string{"", "no parens", "1 (x) S 1 2"} {
		if parseStartTicks(bad) != 0 {
			t.Errorf("parseStartTicks(%q) != 0", bad)
		}
	}
}

// A write that fails is logged once and never panics or blocks.
func TestWriteFailureIsLoggedOnce(t *testing.T) {
	w, path, logs := newTestWriter(t)
	// The status dir's place is taken by a regular file.
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(path)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Dir(path), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	w.Start()
	w.TurnStarted(Payload{})
	w.TurnEnded()
	w.Remove()
	if len(*logs) != 1 || !strings.Contains((*logs)[0], "status") {
		t.Fatalf("logs = %q; want exactly one line", *logs)
	}
}

// OnFail fires on every failed write, not only the first (which is all the log
// line reports).
func TestOnFailCountsEveryFailure(t *testing.T) {
	w, path, logs := newTestWriter(t)
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(path)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Dir(path), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	w.OnFail(func() { n.Add(1) })
	w.Start()
	w.SetMainPID(1)
	w.SetUnit("u")
	if n.Load() != 3 || len(*logs) != 1 {
		t.Fatalf("OnFail ran %d times, %d log lines; want 3 and 1", n.Load(), len(*logs))
	}
	var nilw *Writer
	nilw.OnFail(func() {})
}

// A wider dir is narrowed to 0700; a symlinked status dir is refused, and a
// symlink at the file's name is replaced, never written through.
func TestDirHardening(t *testing.T) {
	t.Run("narrows a wide dir", func(t *testing.T) {
		w, path, _ := newTestWriter(t)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		w.Start()
		if di, err := os.Lstat(filepath.Dir(path)); err != nil || di.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v, %v", di.Mode(), err)
		}
	})
	t.Run("refuses a symlinked dir", func(t *testing.T) {
		w, path, logs := newTestWriter(t)
		elsewhere := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(filepath.Dir(path)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
		w.Start()
		if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
			t.Fatalf("wrote through a symlinked dir: %v", entries)
		}
		if len(*logs) != 1 {
			t.Fatalf("logs = %q; want the refusal logged once", *logs)
		}
	})
	t.Run("replaces a symlink leaf", func(t *testing.T) {
		w, path, _ := newTestWriter(t)
		victim := filepath.Join(t.TempDir(), "victim")
		if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, path); err != nil {
			t.Fatal(err)
		}
		w.Start()
		if b, _ := os.ReadFile(victim); string(b) != "keep" {
			t.Fatalf("wrote through a symlink leaf: %q", b)
		}
		if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
			t.Fatalf("leaf not replaced by a regular file: %v %v", fi, err)
		}
		if readStatus(t, path).Principal != "p-1" {
			t.Fatal("status not written")
		}
	})
}

// ParsePayload takes {transcript_path, session_id} and refuses what it
// cannot vouch for.
func TestParsePayload(t *testing.T) {
	p, ok := ParsePayload([]byte(`{"transcript_path":"/a/b.jsonl","session_id":"s-1"}`))
	if !ok || p.TranscriptPath != "/a/b.jsonl" || p.SessionID != "s-1" {
		t.Fatalf("valid: %+v %v", p, ok)
	}
	for _, bad := range []string{
		"",
		"garbage",
		`{"transcript_path":"relative/x"}`,
		`{"transcript_path":"/a\u0000b"}`,
		`{"session_id":"` + strings.Repeat("s", 300) + `"}`,
		`{"transcript_path":"/` + strings.Repeat("p", MaxPayload) + `"}`,
	} {
		if p, ok := ParsePayload([]byte(bad)); ok {
			t.Errorf("ParsePayload(%.40q) = %+v, want refused", bad, p)
		}
	}
}
