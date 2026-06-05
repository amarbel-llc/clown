# Job-Wakeup Channel Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** Build the clown-provided job-wakeup channel — a durable on-disk journal plus a lossy UDS-datagram nudge — so any plugin can `clown job start`/`done` a background task and have the originating (or a targeted) clown session woken by a push notification on terminal transitions.

**Architecture:** A new pure-Go package `internal/jobwake` owns the journal (JSONL per job under `$XDG_STATE_HOME/clown/jobs/<channel-id>/`), the nudge socket (`unixgram` under `$XDG_RUNTIME_DIR`), and the monitor (replay-unacked → bind → emit one stdout line per waking event). `cmd/clown` exposes `job {start,progress,done,read}` and `job-watch` subcommands by extending the `run()` dispatch (main.go:127). clown resolves `CLOWN_SESSION_ID` once at startup and `os.Setenv`s it, so every child (plugin MCP servers via `os.Environ()`, and the monitor via Claude Code) inherits the same channel key. clown registers the `clown job-watch` monitor through a synthesized built-in plugin dir passed with `--plugin-dir`. The on-disk + on-wire formats are the contract (RFC-0009); the CLI is the reference producer/monitor.

**Tech Stack:** Go (stdlib only: `os`, `net` unixgram, `crypto/sha256`, `encoding/json`, `time`, `context`); bats + bats-emo (`require_bin`) for conformance; existing `cmd/clown` flag/dispatch plumbing.

**Rollback:** Purely additive. `CLOWN_DISABLE_JOB_WAKEUP=1` makes `job-watch` exit 0 immediately and the emit subcommands no-op; not registering the built-in monitor disables the push path. No data migration, no wire-format change to existing features. Revert = unset nothing / drop the built-in monitor registration (one call site).

**Normative reference:** `docs/rfcs/0009-job-wakeup-channel.md`. **Feature context:** `docs/features/0013-job-wakeup-channel.md`.

---

## Conventions for every task

- Run Go unit tests with the paved recipe: `just test-go` (or, scoped while iterating, `hamster.go-build` / `go test ./internal/jobwake/...`).
- TDD: write the failing test, run it red, implement minimally, run it green, commit.
- Commit messages: `feat(jobwake): …` / `test(jobwake): …`, signed off `🤡 via [Clown](https://github.com/amarbel-llc/clown)`.
- Do **not** run the full `just` build before `merge-this-session` — the pre-merge hook is the CI lane.

---

### Task 1: jobwake paths + session-key resolution + channel-id

**Files:**
- Create: `internal/jobwake/paths.go`
- Test: `internal/jobwake/paths_test.go`

**Step 1: Write the failing test**

```go
package jobwake

import (
	"path/filepath"
	"testing"
)

func TestSessionKeyResolutionOrder(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "")
	t.Setenv("SPINCLASS_SESSION_ID", "repo/branch")
	if got := SessionKey(); got != "repo/branch" {
		t.Fatalf("want spinclass key, got %q", got)
	}
	t.Setenv("CLOWN_SESSION_ID", "explicit")
	if got := SessionKey(); got != "explicit" {
		t.Fatalf("CLOWN_SESSION_ID must win, got %q", got)
	}
}

func TestSessionKeyGeneratedWhenUnset(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "")
	t.Setenv("SPINCLASS_SESSION_ID", "")
	t.Setenv("CLAUDE_SESSION_ID", "")
	got := SessionKey()
	if len(got) != 32 {
		t.Fatalf("generated key must be 32 hex chars, got %q (len %d)", got, len(got))
	}
}

func TestChannelIDStableAnd32Hex(t *testing.T) {
	a := ChannelID("repo/branch")
	if len(a) != 32 {
		t.Fatalf("channel id must be 32 hex chars, got %q", a)
	}
	if a != ChannelID("repo/branch") {
		t.Fatal("channel id must be deterministic")
	}
	if a == ChannelID("repo/other") {
		t.Fatal("distinct keys must yield distinct channel ids")
	}
}

func TestJournalPathsUnderStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/xsh")
	cid := ChannelID("k")
	want := filepath.Join("/tmp/xsh", "clown", "jobs", cid, "job1.jsonl")
	if got := JournalFile(cid, "job1"); got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}
```

**Step 2: Run red** — `go test ./internal/jobwake/ -run TestSessionKey -v` → FAIL (undefined).

**Step 3: Implement**

```go
// Package jobwake implements the clown job-wakeup channel: a durable journal
// plus a lossy UDS-datagram nudge per docs/rfcs/0009-job-wakeup-channel.md.
package jobwake

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
)

// SessionKey resolves the active session key per RFC-0009 §2:
// CLOWN_SESSION_ID, else SPINCLASS_SESSION_ID, else a generated value.
func SessionKey() string {
	if v := os.Getenv("CLOWN_SESSION_ID"); v != "" {
		return v
	}
	if v := os.Getenv("SPINCLASS_SESSION_ID"); v != "" {
		return v
	}
	if v := os.Getenv("CLAUDE_SESSION_ID"); v != "" {
		return v
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ChannelID derives the filesystem-safe channel identifier from a session key:
// the first 16 bytes of SHA-256(key) as 32 lowercase hex digits (RFC-0009 §2).
func ChannelID(sessionKey string) string {
	sum := sha256.Sum256([]byte(sessionKey))
	return hex.EncodeToString(sum[:16])
}

func stateHome() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "state")
}

func runtimeDir() string {
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
		return filepath.Join(v, "clown", "jobs")
	}
	tmp := os.Getenv("TMPDIR")
	if tmp == "" {
		tmp = "/tmp"
	}
	return filepath.Join(tmp, "clown-jobs-"+strconv.Itoa(os.Getuid()))
}

// JournalDir is the per-channel journal directory (created mode 0700).
func JournalDir(channelID string) string {
	return filepath.Join(stateHome(), "clown", "jobs", channelID)
}

// JournalFile is the JSONL file for one job.
func JournalFile(channelID, jobID string) string {
	return filepath.Join(JournalDir(channelID), jobID+".jsonl")
}

// AckFile is the per-channel monitor ack cursor.
func AckFile(channelID string) string {
	return filepath.Join(JournalDir(channelID), ".ack.json")
}

// SocketPath is the per-channel unixgram nudge socket.
func SocketPath(channelID string) string {
	return filepath.Join(runtimeDir(), channelID+".sock")
}
```

**Step 4: Run green.** **Step 5: Commit.**

---

### Task 2: Record type + event-type registry

**Files:**
- Create: `internal/jobwake/record.go`
- Test: `internal/jobwake/record_test.go`

**Step 1: Failing test**

```go
package jobwake

import (
	"encoding/json"
	"testing"
)

func TestRecordJSONRoundTrip(t *testing.T) {
	r := Record{V: 1, Job: "b-1", Session: "repo/branch", Source: "moxy",
		Type: TypeSucceeded, Seq: 2, TS: "2026-06-05T00:00:00Z", Message: "ok"}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back Record
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back != r {
		t.Fatalf("round trip mismatch: %+v != %+v", back, r)
	}
}

func TestWakePolicy(t *testing.T) {
	for _, ty := range []string{TypeSucceeded, TypeFailed, TypeCancelled, TypeInterrupted} {
		if !IsTerminal(ty) || !IsWaking(ty) {
			t.Errorf("%s must be terminal and waking", ty)
		}
	}
	for _, ty := range []string{TypeStarted, TypeProgress} {
		if IsTerminal(ty) || IsWaking(ty) {
			t.Errorf("%s must not be terminal or waking", ty)
		}
	}
	if IsWaking("needs-attention") {
		t.Error("reserved non-terminal types must not wake in v1")
	}
}
```

**Step 2: Red.** **Step 3: Implement**

```go
package jobwake

const (
	SchemaVersion = 1

	TypeStarted     = "started"
	TypeProgress    = "progress"
	TypeSucceeded   = "succeeded"
	TypeFailed      = "failed"
	TypeCancelled   = "cancelled"
	TypeInterrupted = "interrupted"
)

// Record is one line in a job's JSONL journal (RFC-0009 §4).
type Record struct {
	V         int    `json:"v"`
	Job       string `json:"job"`
	Session   string `json:"session"`
	Source    string `json:"source"`
	Type      string `json:"type"`
	Seq       int    `json:"seq"`
	TS        string `json:"ts"`
	Message   string `json:"message,omitempty"`
	ResultRef string `json:"result_ref,omitempty"`
}

func IsTerminal(t string) bool {
	switch t {
	case TypeSucceeded, TypeFailed, TypeCancelled, TypeInterrupted:
		return true
	}
	return false
}

// IsWaking reports whether an event of this type wakes the agent. In v1 the
// waking set is exactly the terminal set; unknown/reserved types do not wake.
func IsWaking(t string) bool { return IsTerminal(t) }
```

**Step 4: Green.** **Step 5: Commit.**

---

### Task 3: Producer `Start`

**Files:**
- Create: `internal/jobwake/producer.go`
- Test: `internal/jobwake/producer_test.go`

**Step 1: Failing test** (use `t.Setenv("XDG_STATE_HOME", t.TempDir())`, `CLOWN_SESSION_ID=k`):

```go
func TestStartWritesStartedRecord(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, err := Start(StartOpts{Source: "moxy", Label: "build"})
	if err != nil {
		t.Fatal(err)
	}
	recs := mustReadJob(t, ChannelID("k"), id)
	if len(recs) != 1 || recs[0].Type != TypeStarted || recs[0].Seq != 0 {
		t.Fatalf("want one started seq0 record, got %+v", recs)
	}
	if recs[0].Source != "moxy" || recs[0].Session != "k" || recs[0].V != 1 {
		t.Fatalf("bad record fields: %+v", recs[0])
	}
}
```

(Add a `mustReadJob` helper that returns `[]Record`; it can call the `ReadJob` built in Task 6, so order Task 6 before this test runs — or inline a JSONL parse in the helper now and replace later. Simplest: inline parse now.)

**Step 3: Implement** (`Start` resolves target → channel, mkdir 0700, write seq-0 record; `--target` overrides `SessionKey()`):

```go
package jobwake

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

var jobIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

type StartOpts struct {
	Target string // session key; empty => SessionKey()
	Label  string
	Source string
}

func newJobID(label string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	suf := hex.EncodeToString(b)
	label = sanitizeLabel(label)
	if label == "" {
		return suf
	}
	return label + "-" + suf
}

func sanitizeLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, s)
	return strings.Trim(s, "-")
}

func nowTS() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func Start(o StartOpts) (string, error) {
	session := o.Target
	if session == "" {
		session = SessionKey()
	}
	source := o.Source
	if source == "" {
		if v := os.Getenv("CLOWN_JOB_SOURCE"); v != "" {
			source = v
		} else {
			source = "clown"
		}
	}
	cid := ChannelID(session)
	if err := os.MkdirAll(JournalDir(cid), 0o700); err != nil {
		return "", err
	}
	id := newJobID(o.Label)
	if !jobIDRe.MatchString(id) {
		return "", fmt.Errorf("generated job id %q is invalid", id)
	}
	rec := Record{V: SchemaVersion, Job: id, Session: session, Source: source,
		Type: TypeStarted, Seq: 0, TS: nowTS()}
	if err := appendRecord(cid, rec, false); err != nil {
		return "", err
	}
	return id, nil
}
```

(Define `appendRecord(cid, rec, fsync)` in Task 4 alongside `Progress`/`Done`; for this task a minimal append is fine, then Task 4 extends it with seq lookup + fsync.)

**Steps 2/4/5:** red, green, commit.

---

### Task 4: Producer `Progress` + `Done` (seq increment, fsync, terminal-once)

**Files:**
- Modify: `internal/jobwake/producer.go`
- Test: `internal/jobwake/producer_test.go`

**Step 1: Failing tests**

```go
func TestProgressAndDoneIncrementSeq(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, _ := Start(StartOpts{Source: "s"})
	if err := Progress(id, "halfway"); err != nil {
		t.Fatal(err)
	}
	if err := Done(id, TypeSucceeded, "ok", "ref"); err != nil {
		t.Fatal(err)
	}
	recs := mustReadJob(t, ChannelID("k"), id)
	if len(recs) != 3 || recs[1].Seq != 1 || recs[2].Seq != 2 {
		t.Fatalf("want seq 0,1,2; got %+v", recs)
	}
	if recs[2].Type != TypeSucceeded || recs[2].ResultRef != "ref" {
		t.Fatalf("bad terminal record: %+v", recs[2])
	}
}

func TestDoneRejectsSecondTerminal(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, _ := Start(StartOpts{Source: "s"})
	if err := Done(id, TypeFailed, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := Done(id, TypeSucceeded, "", ""); err == nil {
		t.Fatal("second terminal must error")
	}
}

func TestDoneRejectsBadState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, _ := Start(StartOpts{Source: "s"})
	if err := Done(id, "wat", "", ""); err == nil {
		t.Fatal("non-terminal state must error")
	}
}
```

**Step 3: Implement** — `appendRecord` reads current records to compute next seq and detect an existing terminal; terminal append fsyncs:

```go
func appendRecord(cid string, partial Record, fsync bool) error {
	existing, err := ReadJob(cid, partial.Job) // Task 6; tolerate not-found as empty
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, r := range existing {
		if IsTerminal(r.Type) {
			return fmt.Errorf("job %q already terminal (%s)", partial.Job, r.Type)
		}
	}
	partial.Seq = len(existing) // 0,1,2,... since single writer appends in order
	line, err := json.Marshal(partial)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(JournalFile(cid, partial.Job), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	if fsync {
		return f.Sync()
	}
	return nil
}

func Progress(jobID, message string) error {
	session := SessionKey()
	cid := ChannelID(session)
	rec := Record{V: SchemaVersion, Job: jobID, Session: session, Type: TypeProgress,
		TS: nowTS(), Message: oneLine(message)}
	if err := appendRecord(cid, rec, false); err != nil {
		return err
	}
	sendNudge(cid, jobID, TypeProgress) // Task 5
	return nil
}

func Done(jobID, state, message, resultRef string) error {
	if !IsTerminal(state) {
		return fmt.Errorf("invalid terminal state %q", state)
	}
	session := SessionKey()
	cid := ChannelID(session)
	rec := Record{V: SchemaVersion, Job: jobID, Session: session, Type: state,
		TS: nowTS(), Message: oneLine(message), ResultRef: resultRef}
	if err := appendRecord(cid, rec, true); err != nil { // fsync before nudge
		return err
	}
	sendNudge(cid, jobID, state)
	return nil
}

func oneLine(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " ") }
```

(`Start`'s `appendRecord` call gains `false`; it carries its own `source`/`session`. Note `appendRecord` recomputes `Seq` from existing count — drop the explicit `Seq:0` set in `Start` since `appendRecord` now owns seq. Keep `Source`/`Session` set by callers; `Progress`/`Done` may leave `Source` empty — acceptable, or carry it forward by reading the started record's source. Decide: carry source forward by reading `existing[0].Source` in `appendRecord` when `partial.Source == ""`.)

**Steps 2/4/5:** red, green, commit.

---

### Task 5: Nudge send + receive helpers

**Files:**
- Create: `internal/jobwake/nudge.go`
- Test: `internal/jobwake/nudge_test.go`

**Step 1: Failing test** — bind a listener, send, receive `1|job|type`; sending to a missing socket returns no error to the caller:

```go
func TestNudgeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	cid := "chan"
	conn, err := bindNudge(cid)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sendNudge(cid, "job1", TypeSucceeded)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 512)
	n, _, err := conn.ReadFromUnix(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(buf[:n])); got != "1|job1|succeeded" {
		t.Fatalf("want 1|job1|succeeded, got %q", got)
	}
}

func TestSendNudgeNoListenerIsSilent(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	sendNudge("nochan", "j", TypeFailed) // must not panic / must not block
}
```

**Step 3: Implement**

```go
package jobwake

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

func bindNudge(channelID string) (*net.UnixConn, error) {
	p := SocketPath(channelID)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(p) // clear stale socket before bind (RFC-0009 §9)
	addr := &net.UnixAddr{Name: p, Net: "unixgram"}
	return net.ListenUnixgram("unixgram", addr)
}

func sendNudge(channelID, jobID, eventType string) {
	p := SocketPath(channelID)
	raddr := &net.UnixAddr{Name: p, Net: "unixgram"}
	conn, err := net.DialUnix("unixgram", nil, raddr)
	if err != nil {
		return // best-effort; common when no monitor is running (RFC-0009 §6)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(fmt.Sprintf("%d|%s|%s\n", SchemaVersion, jobID, eventType)))
}
```

**Steps 2/4/5:** red, green, commit.

---

### Task 6: Reader (`ReadJob`, `scanWaking`) + ack load/save

**Files:**
- Create: `internal/jobwake/reader.go`
- Test: `internal/jobwake/reader_test.go`

**Step 1: Failing tests** — `ReadJob` parses JSONL skipping malformed lines; `scanWaking` returns only waking records across all jobs sorted by TS; ack round-trips and a missing ack file loads empty.

**Step 3: Implement**

```go
package jobwake

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func ReadJob(channelID, jobID string) ([]Record, error) {
	f, err := os.Open(JournalFile(channelID, jobID))
	if err != nil {
		return nil, err // callers tolerate os.IsNotExist
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Record
		if json.Unmarshal([]byte(line), &r) != nil {
			continue // skip malformed line (RFC-0009 §10)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func scanWaking(channelID string) ([]Record, error) {
	entries, err := os.ReadDir(JournalDir(channelID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Record
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		jobID := strings.TrimSuffix(name, ".jsonl")
		recs, err := ReadJob(channelID, jobID)
		if err != nil {
			continue
		}
		for _, r := range recs {
			if IsWaking(r.Type) {
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}

type ack struct {
	V     int            `json:"v"`
	Acked map[string]int `json:"acked"`
}

func loadAck(channelID string) ack {
	a := ack{V: 1, Acked: map[string]int{}}
	b, err := os.ReadFile(AckFile(channelID))
	if err != nil {
		return a // missing/corrupt => empty (RFC-0009 §10)
	}
	var parsed ack
	if json.Unmarshal(b, &parsed) == nil && parsed.Acked != nil {
		return parsed
	}
	return a
}

func saveAck(channelID string, a ack) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	tmp := AckFile(channelID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, AckFile(channelID)) // atomic
}
```

**Steps 2/4/5:** red, green, commit.

---

### Task 7: Monitor `Watch` (replay + bind + loop)

**Files:**
- Create: `internal/jobwake/watch.go`
- Test: `internal/jobwake/watch_test.go`

**Step 1: Failing test** — a job finished *before* `Watch` starts is replayed exactly once; a second `Watch` (ack persisted) replays nothing; `progress` is never emitted:

```go
func TestWatchReplaysUnackedTerminalOnce(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, _ := Start(StartOpts{Source: "s"})
	_ = Progress(id, "p")
	_ = Done(id, TypeSucceeded, "ok", "")

	emitted := drainWatch(t, "k") // runs Watch in a goroutine, collects emits, cancels after first or 500ms idle
	if len(emitted) != 1 || emitted[0].Type != TypeSucceeded {
		t.Fatalf("want one succeeded emit, got %+v", emitted)
	}

	emitted2 := drainWatch(t, "k")
	if len(emitted2) != 0 {
		t.Fatalf("second watch must replay nothing, got %+v", emitted2)
	}
}
```

**Step 3: Implement** — replay first (ack-gated), then bind socket and loop on datagram-or-ticker re-scanning; emit advances ack:

```go
package jobwake

import (
	"context"
	"time"
)

// rescanInterval is the safety-net re-scan cadence (TUNING LEVER, RFC-0009 §9 /
// FDR-0013). Start at 1s for spinclass parity.
const rescanInterval = time.Second

func Watch(ctx context.Context, sessionKey string, emit func(Record) error) error {
	cid := ChannelID(sessionKey)
	if err := emitUnacked(cid, emit); err != nil {
		return err
	}
	conn, err := bindNudge(cid)
	if err != nil {
		return err
	}
	defer func() { conn.Close(); _ = removeSocket(cid) }()

	datagrams := make(chan struct{}, 64)
	go func() {
		buf := make([]byte, 512)
		for {
			if _, _, err := conn.ReadFromUnix(buf); err != nil {
				return // conn closed on ctx cancel
			}
			select {
			case datagrams <- struct{}{}:
			default:
			}
		}
	}()

	ticker := time.NewTicker(rescanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-datagrams:
			if err := emitUnacked(cid, emit); err != nil {
				return err
			}
		case <-ticker.C:
			if err := emitUnacked(cid, emit); err != nil {
				return err
			}
		}
	}
}

func emitUnacked(cid string, emit func(Record) error) error {
	a := loadAck(cid)
	waking, err := scanWaking(cid)
	if err != nil {
		return err
	}
	for _, r := range waking {
		if prev, ok := a.Acked[r.Job]; ok && r.Seq <= prev {
			continue
		}
		if err := emit(r); err != nil {
			return err
		}
		a.Acked[r.Job] = r.Seq
		if err := saveAck(cid, a); err != nil { // persist after emit => at-least-once
			return err
		}
	}
	return nil
}
```

(Add `removeSocket(cid)` = `os.Remove(SocketPath(cid))`. For the test, `drainWatch` cancels the context once an emit arrives or after a short idle.)

**Steps 2/4/5:** red, green, commit.

---

### Task 8: CLI wiring in `cmd/clown` (`job` + `job-watch`) and notification line

**Files:**
- Create: `cmd/clown/job.go`
- Modify: `cmd/clown/main.go:127-135` (extend `run()` dispatch), and the help/usage block (main.go ~1238).
- Test: `cmd/clown/job_test.go`

**Step 1: Failing tests** — table-drive the subcommand parser: `start` prints a job id, `done --state succeeded` writes a terminal record, `done --state wat` exits non-zero, and `CLOWN_DISABLE_JOB_WAKEUP=1` makes `job-watch` return 0 immediately and emits no-op. Use `t.Setenv` for XDG dirs + `CLOWN_SESSION_ID`. Assert via `jobwake.ReadJob`.

**Step 3: Implement**

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/amarbel-llc/clown/internal/jobwake"
)

func runJob(args []string) int {
	if os.Getenv("CLOWN_DISABLE_JOB_WAKEUP") == "1" {
		return 0 // emit subcommands are no-ops when disabled (RFC-0009 §8)
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "clown job: missing subcommand (start|progress|done|read)")
		return 2
	}
	switch args[0] {
	case "start":
		return jobStart(args[1:])
	case "progress":
		return jobProgress(args[1:])
	case "done":
		return jobDone(args[1:])
	case "read":
		return jobRead(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "clown job: unknown subcommand %q\n", args[0])
		return 2
	}
}

func jobStart(args []string) int {
	fs := flag.NewFlagSet("job start", flag.ContinueOnError)
	target := fs.String("target", "", "target session key")
	label := fs.String("label", "", "job label")
	source := fs.String("source", "", "emitting plugin")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	id, err := jobwake.Start(jobwake.StartOpts{Target: *target, Label: *label, Source: *source})
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown job start: %v\n", err)
		return 1
	}
	fmt.Println(id)
	return 0
}

func jobDone(args []string) int {
	fs := flag.NewFlagSet("job done", flag.ContinueOnError)
	state := fs.String("state", "", "succeeded|failed|cancelled|interrupted")
	message := fs.String("message", "", "human detail")
	resultRef := fs.String("result-ref", "", "opaque result pointer")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "clown job done: missing <job-id>")
		return 2
	}
	if err := jobwake.Done(fs.Arg(0), *state, *message, *resultRef); err != nil {
		fmt.Fprintf(os.Stderr, "clown job done: %v\n", err)
		return 1
	}
	return 0
}

// jobProgress and jobRead are analogous; jobRead prints records (NDJSON with
// --json, else human lines) and advances/peeks the read cursor per RFC-0009 §8.

func runJobWatch(_ []string) int {
	if os.Getenv("CLOWN_DISABLE_JOB_WAKEUP") == "1" {
		return 0 // RFC-0009 §8
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	err := jobwake.Watch(ctx, jobwake.SessionKey(), func(r jobwake.Record) error {
		_, werr := fmt.Println(notificationLine(r))
		return werr
	})
	if err != nil && ctx.Err() != nil {
		return 0 // clean interrupt is a normal monitor shutdown
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown job-watch: %v\n", err)
		return 1
	}
	return 0
}

// notificationLine renders RFC-0009 §9: "[clown-job] <source> <job> <type>: <message> · <result_ref>"
func notificationLine(r jobwake.Record) string {
	line := fmt.Sprintf("[clown-job] %s %s %s", r.Source, r.Job, r.Type)
	if r.Message != "" {
		line += ": " + r.Message
	}
	if r.ResultRef != "" {
		line += " · " + r.ResultRef
	}
	return line
}
```

Extend `run()`:

```go
		case "job":
			return runJob(rawArgs[1:])
		case "job-watch":
			return runJobWatch(rawArgs[1:])
```

Add a `notificationLine` table test (covers the RFC §9 format, including the `: `-omitted and `·` cases).

**Steps 2/4/5:** red, green, commit.

---

### Task 9: Resolve + export `CLOWN_SESSION_ID` at clown startup

**Files:**
- Modify: `cmd/clown/main.go` (early in `run()`, before any plugin-host launch / provider exec)
- Test: `cmd/clown/main_test.go`

**Step 1: Failing test** — after calling the resolve helper with `CLOWN_SESSION_ID` unset and `SPINCLASS_SESSION_ID=repo/branch`, `os.Getenv("CLOWN_SESSION_ID") == "repo/branch"`; with it preset, it is left untouched.

**Step 3: Implement** — a one-liner helper invoked at the top of `run()` (after the `resume`/`sessions-complete`/`job*` early-dispatch, before `parseFlags`/plugin host):

```go
// ensureSessionID resolves the job-wakeup channel key once and exports it so
// every child (plugin MCP servers via os.Environ(), and the Claude-spawned
// job-watch monitor) shares the same channel (RFC-0009 §2).
func ensureSessionID() {
	if os.Getenv("CLOWN_SESSION_ID") == "" {
		_ = os.Setenv("CLOWN_SESSION_ID", jobwake.SessionKey())
	}
}
```

Call it in `run()` just before `parseFlags` (so the `job*`/`job-watch` subcommands, which exit earlier, resolve their own key via `jobwake.SessionKey()` directly — consistent result).

**Steps 2/4/5:** red, green, commit.

---

### Task 10: Register the built-in `clown job-watch` monitor

**Files:**
- Create: `cmd/clown/jobmonitor.go` (synthesize a built-in plugin dir with a `.claude-plugin/plugin.json` declaring the monitor)
- Modify: the site in `cmd/clown/main.go` that assembles the `--plugin-dir` list passed to Claude (search for `--plugin-dir` usage / the compiled-plugin staging code).
- Test: `cmd/clown/jobmonitor_test.go`

**Step 1: Failing test** — the synthesizer writes a temp dir containing `.claude-plugin/plugin.json` whose `experimental.monitors[0]` is `{name:"clown-job-watch", command:"clown job-watch", ...}`; and when `CLOWN_DISABLE_JOB_WAKEUP=1`, the synthesizer returns `("", nil)` (no dir, monitor not registered).

**Step 3: Implement** — mirror the existing staging-dir pattern; write:

```json
{
  "name": "clown-builtin-jobwake",
  "version": "1",
  "experimental": {
    "monitors": [
      {
        "name": "clown-job-watch",
        "command": "clown job-watch",
        "description": "clown job-wakeup channel: wakes this session when a background job finishes"
      }
    ]
  }
}
```

Append the returned dir to the `--plugin-dir` set. Gate on `CLOWN_DISABLE_JOB_WAKEUP`. Clean up the temp dir on shutdown alongside the other staged dirs.

> **Design check during implementation:** confirm whether the monitor `command` must be an absolute path to the clown binary (Claude Code spawns it with the session's PATH). If `clown` is not guaranteed on PATH for the monitor, resolve `os.Executable()` and emit an absolute `command`. Add a test asserting the command is absolute when resolvable.

**Steps 2/4/5:** red, green, commit.

---

### Task 11: bats conformance suite

**Files:**
- Create: `zz-tests_bats/job_wakeup.bats`
- Possibly modify: `justfile` test lane if bats files are enumerated explicitly (check how `plugin_host.bats` is discovered).

**Step 1: Write the suite** (binary injection via bats-emo; isolated `$HOME`/XDG via `setup_test_home`):

```bash
setup() {
  load 'lib/common.bash'
  setup_test_home
  require_bin CLOWN_BIN clown
  export CLOWN_SESSION_ID="repo/branch"
}

@test "start prints a job id and writes a started record" {
  run "$CLOWN_BIN" job start --source moxy --label build
  assert_success
  [[ -n "$output" ]]
}

@test "second done on a terminal job fails" {
  id="$("$CLOWN_BIN" job start --source s)"
  run "$CLOWN_BIN" job done "$id" --state succeeded
  assert_success
  run "$CLOWN_BIN" job done "$id" --state failed
  assert_failure
}

@test "monitor replays an unacked terminal event and formats the line" {
  id="$("$CLOWN_BIN" job start --source moxy --label build)"
  "$CLOWN_BIN" job progress "$id" --message "halfway"
  "$CLOWN_BIN" job done "$id" --state succeeded --message "nix build ok"
  run timeout 5 "$CLOWN_BIN" job-watch
  assert_line --partial "[clown-job] moxy ${id} succeeded: nix build ok"
  refute_line --partial "halfway"   # progress never wakes
}

@test "CLOWN_DISABLE_JOB_WAKEUP makes watch exit 0 and emits no-op" {
  CLOWN_DISABLE_JOB_WAKEUP=1 run "$CLOWN_BIN" job-watch
  assert_success
}
```

(`job-watch` blocks; the test relies on `timeout` ending it after replay — verify replay emits before the timeout. If `job-watch` must be cancelled cleanly, send it EOF on stdin or rely on `timeout`'s SIGTERM; confirm the exit-status assertion accordingly. This mirrors how `chat-watch` is tested in spinclass.)

**Step 2: Run** — via the bats lane recipe (check `just` for the bats target; do **not** invent a path). Expected: green.

**Step 5: Commit.**

---

### Task 12: Man page + orientation-doc update

**Files:**
- Create: `man/man1/clown-job.1` (or scdoc source per `eng-manpages(7)` — check how existing clown man pages are authored before choosing scdoc vs raw).
- Modify: `CLAUDE.md` (AGENTS.md) — add a short bullet under Architecture documenting the job-wakeup channel now that it is implemented (this is the doc-drift update deferred at the design stage).
- Modify: `man/man5/clown-json.5` only if a `clown.json` field is added (none is — the monitor is clown-built-in, so likely no change).

**Steps:** write the man page documenting `clown job {start,progress,done,read}` and `clown job-watch`; cross-reference `clown-json(5)` and RFC-0009. Commit. Flip RFC-0009 and FDR-0013 `status:` from `proposed` to `experimental` in the same commit (working implementation now exists).

---

## Discovered pre-existing issue (file separately, do not fix here)

`internal/pluginhost/server.go:57-59` sets `s.cmd.Env = append(os.Environ(), k+"="+v)` **inside** the `range s.Def.Env` loop, so each iteration resets `s.cmd.Env` and only the **last** `clown.json` `env` entry survives. Plugins declaring multiple env vars are silently broken. This does not affect this feature (CLOWN_SESSION_ID rides `os.Environ()` via the Task 9 `os.Setenv`), but it should be filed via `/eng:file-issue` and tracked as a task-list item.

---

## Execution order & dependencies

1–7 build `internal/jobwake` bottom-up (paths → record → producer → nudge → reader → monitor). 8–10 wire `cmd/clown`. 11 is the conformance gate. 12 closes docs. Tasks 1–7 have no cross-package deps and can each be a fresh subagent; 8–10 depend on the package API being stable (finish 1–7 first). Run `just test-go` after each package task; run the bats lane after Task 11.
