// Package sessionstatus writes the hung-session status file (clown#322): the
// facts a clown frontend knows about its session, for spinclass to classify a
// session as hung or dead. clown only reports; it never acts on the file.
//
// The file is $XDG_STATE_HOME/clown/status/<hex sha256(principal)>.json, 0600
// in a 0700 dir, replaced atomically (a temp file in the same dir, then a
// rename), so a reader sees a whole old or a whole new file. The contract is
// FDR 0023's "Hung-session status file"; this package and spinclass both cite
// it. A field that is empty or 0 is unknown, never a claim of health.
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
	"syscall"
	"time"

	"code.linenisgreat.com/clown/internal/userpath"
)

// Version is the file's schema version.
const Version = 1

// MaxPayload bounds a turn-start payload (Payload as JSON), and so the busy
// datagram that carries it.
const MaxPayload = 4096

// maxSessionID bounds a claude session id; real ones are UUIDs.
const maxSessionID = 256

// Status is the file's content. Times are RFC 3339 UTC with nine fraction
// digits (timeFormat); an empty time, an empty string or a 0 is unknown.
type Status struct {
	Version   int    `json:"version"`
	Principal string `json:"principal"`
	// SpinclassSessionKey is SPINCLASS_SESSION_ID in the frontend's env.
	SpinclassSessionKey string `json:"spinclass_session_key"`
	// ClaudeSessionID is the session id the last turn-start hook reported.
	ClaudeSessionID string `json:"claude_session_id"`
	// TurnRunning is set by a turn-start hook and cleared by the Stop hook.
	TurnRunning bool `json:"turn_running"`
	// TurnStartedAt is the last turn start; kept after the turn ends.
	TurnStartedAt string `json:"turn_started_at"`
	// LastHookAt is the last hook datagram of either kind.
	LastHookAt  string `json:"last_hook_at"`
	FrontendPID int    `json:"frontend_pid"`
	// FrontendStartTicks is field 22 of /proc/<frontend_pid>/stat (start
	// time in clock ticks since boot), which tells a live frontend from a
	// reused pid. 0 off Linux or when unreadable.
	FrontendStartTicks uint64 `json:"frontend_start_ticks"`
	// Unit is the agent-scope unit when confined; empty unconfined.
	Unit string `json:"unit"`
	// MainPID is the provider's pid unconfined, the unit's MainPID (the
	// in-unit init) confined; 0 until known.
	MainPID int `json:"main_pid"`
	// TranscriptPath is what the last turn-start hook reported.
	TranscriptPath string `json:"transcript_path"`
	// RestartRefusal is set when a restart spinclass consumed was dropped
	// (refusal.go); absent otherwise. Additive: the version stays 1.
	RestartRefusal *RestartRefusal `json:"restart_refusal,omitempty"`
	WrittenAt      string          `json:"written_at"`
}

// Payload is what a turn-start hook reports, from its stdin.
type Payload struct {
	TranscriptPath string `json:"transcript_path"`
	SessionID      string `json:"session_id"`
}

// ParsePayload decodes a turn-start payload, refusing one it cannot vouch for
// the shape of: over MaxPayload, not JSON, a transcript path that is not
// absolute or holds a NUL, an over-long session id. The content is still only
// what something inside the agent's reach sent.
func ParsePayload(b []byte) (Payload, bool) {
	var p Payload
	if len(b) == 0 || len(b) > MaxPayload || json.Unmarshal(b, &p) != nil {
		return Payload{}, false
	}
	if p.TranscriptPath != "" && (!filepath.IsAbs(p.TranscriptPath) || strings.ContainsRune(p.TranscriptPath, 0)) {
		return Payload{}, false
	}
	if len(p.SessionID) > maxSessionID || strings.ContainsRune(p.SessionID, 0) {
		return Payload{}, false
	}
	return p, true
}

// fileName is the status file's name for principal.
func fileName(principal string) string {
	sum := sha256.Sum256([]byte(principal))
	return hex.EncodeToString(sum[:]) + ".json"
}

// Path is the status file for principal.
func Path(principal string) (string, error) {
	return userpath.StatePath("status", fileName(principal))
}

// StartTicks is field 22 (starttime) of /proc/<pid>/stat, 0 when it cannot
// be read (no such process, not Linux).
func StartTicks(pid int) uint64 {
	if pid <= 0 {
		return 0
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	return parseStartTicks(string(b))
}

// parseStartTicks is field 22 of one /proc/<pid>/stat line, 0 when malformed.
func parseStartTicks(s string) uint64 {
	// comm (field 2) may hold spaces and parens: count from the last ')'.
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	fields := strings.Fields(s[i+1:])
	// fields[0] is field 3 (state), so field 22 is fields[19].
	if len(fields) < 20 {
		return 0
	}
	n, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// Writer is one frontend's status file. It is safe for concurrent use; every
// method on a nil Writer does nothing. A write that fails is logged once
// through logf and otherwise ignored: the file is a report, and the session
// never depends on it.
type Writer struct {
	mu      sync.Mutex
	dir     string
	name    string
	st      Status
	removed bool
	// lastWrite and timer coalesce turn-hook writes (update).
	lastWrite time.Time
	timer     *time.Timer
	onFail    func()
	logf      func(format string, args ...any)
	logOnce   sync.Once
}

// Open returns the writer for principal's status file. It writes nothing; Start
// does the first write.
func Open(principal, spinclassKey string, logf func(format string, args ...any)) (*Writer, error) {
	path, err := Path(principal)
	if err != nil {
		return nil, err
	}
	pid := os.Getpid()
	return &Writer{
		dir:  filepath.Dir(path),
		name: filepath.Base(path),
		logf: logf,
		st: Status{
			Version:             Version,
			Principal:           principal,
			SpinclassSessionKey: spinclassKey,
			FrontendPID:         pid,
			FrontendStartTicks:  StartTicks(pid),
		},
	}, nil
}

// timeFormat is RFC 3339 in UTC with all nine fraction digits, so times sort
// as strings (time.RFC3339Nano drops trailing zeros).
const timeFormat = "2006-01-02T15:04:05.000000000Z07:00"

func now() string { return time.Now().UTC().Format(timeFormat) }

// Start writes the status as it stands: at the frontend's start, no hook yet.
func (w *Writer) Start() { w.update(func(*Status) {}, false) }

// TurnStarted records a turn start and the payload's fields that are set; an
// unset field keeps the last value reported.
func (w *Writer) TurnStarted(p Payload) {
	w.update(func(st *Status) {
		t := now()
		st.TurnRunning, st.TurnStartedAt, st.LastHookAt = true, t, t
		if p.TranscriptPath != "" {
			st.TranscriptPath = p.TranscriptPath
		}
		if p.SessionID != "" {
			st.ClaudeSessionID = p.SessionID
		}
	}, true)
}

// TurnEnded records the Stop hook: no turn running, turn_started_at kept.
func (w *Writer) TurnEnded() {
	w.update(func(st *Status) { st.TurnRunning, st.LastHookAt = false, now() }, true)
}

// SetUnit records the agent-scope unit name.
func (w *Writer) SetUnit(unit string) { w.update(func(st *Status) { st.Unit = unit }, false) }

// SetMainPID records the provider's pid (unconfined) or the unit's MainPID.
func (w *Writer) SetMainPID(pid int) { w.update(func(st *Status) { st.MainPID = pid }, false) }

// Remove deletes the file at a clean exit; nothing is written after it.
func (w *Writer) Remove() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.removed = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	root, err := openStateDir(w.dir, false)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			w.fail(err)
		}
		return
	}
	defer root.Close()
	if err := root.Remove(w.name); err != nil && !errors.Is(err, os.ErrNotExist) {
		w.fail(err)
	}
}

// update applies change and writes the file. A turn hook's datagram is
// coalesced (coalesce): the idle socket is unauthenticated, so a flood of
// datagrams must cost the single reader goroutine at most one file write per
// flushInterval, not one per datagram (a slow reader drops legitimate Stop
// datagrams). A change inside the interval of the last write is only
// recorded, and one trailing write follows when the interval is over.
func (w *Writer) update(change func(*Status), coalesce bool) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.removed {
		return
	}
	change(&w.st)
	if coalesce {
		if wait := flushInterval - time.Since(w.lastWrite); wait > 0 {
			if w.timer == nil {
				w.timer = time.AfterFunc(wait, w.flushPending)
			}
			return
		}
	}
	w.flush()
}

// flushInterval is the shortest time between two coalesced writes (a var so
// tests can pick it).
var flushInterval = 100 * time.Millisecond

// flushPending is the trailing write of coalesced changes.
func (w *Writer) flushPending() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.timer = nil
	if w.removed {
		return
	}
	w.flush()
}

// flush writes the current status. Called with mu held.
func (w *Writer) flush() {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.st.WrittenAt = now()
	w.lastWrite = time.Now()
	if err := w.write(); err != nil {
		w.fail(err)
	}
}

// OnFail sets a function called on every failed write or removal (the log
// line is only the first), for a counter. Set it before the first write.
func (w *Writer) OnFail(f func()) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onFail = f
}

func (w *Writer) fail(err error) {
	if w.onFail != nil {
		w.onFail()
	}
	w.logOnce.Do(func() {
		if w.logf != nil {
			w.logf("status file %s: %v (further status-file failures are not logged)", filepath.Join(w.dir, w.name), err)
		}
	})
}

// write replaces the file with w.st through a temp file in the same dir,
// opened inside the dir's os.Root so nothing outside it is touched.
func (w *Writer) write() error {
	b, err := json.Marshal(w.st)
	if err != nil {
		return err
	}
	root, err := openStateDir(w.dir, true)
	if err != nil {
		return err
	}
	defer root.Close()
	tmp := w.name + ".tmp"
	// A leftover from a frontend killed mid-write, or anything planted
	// under the name, goes first: O_EXCL then creates a fresh regular file.
	_ = root.Remove(tmp)
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	if err := root.Rename(tmp, w.name); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}

// openStateDir opens dir as an os.Root, creating it (0700) when create is
// set. It is internal/permstat's openStateDir: dir must be a plain directory
// (a symlink is refused), the opened root must be the directory that was
// lstat'ed, it must be owned by this user, and a wider mode is narrowed to
// 0700 through the opened directory.
func openStateDir(dir string, create bool) (*os.Root, error) {
	if create {
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return nil, err
		}
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	li, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if li.Mode()&os.ModeSymlink != 0 || !li.IsDir() {
		return nil, fmt.Errorf("status dir %s is not a plain directory", dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	d, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	defer d.Close()
	ri, err := d.Stat()
	if err != nil || !os.SameFile(li, ri) {
		root.Close()
		return nil, fmt.Errorf("status dir %s was replaced while it was opened", dir)
	}
	if st, ok := ri.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		root.Close()
		return nil, fmt.Errorf("status dir %s is not owned by this user", dir)
	}
	if ri.Mode().Perm()&0o077 != 0 {
		if err := d.Chmod(0o700); err != nil {
			root.Close()
			return nil, err
		}
	}
	return root, nil
}
