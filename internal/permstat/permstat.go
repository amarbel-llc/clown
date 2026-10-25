// Package permstat counts Claude Code permission prompts and their outcomes
// from hook events, as statsd counters (clown#302):
//
//	clown.permission.<key>.prompted:1|c   a PermissionRequest (the ask path)
//	clown.permission.<key>.approved:1|c   the requested call then ran
//	                                       (PostToolUse or PostToolUseFailure)
//	clown.permission.<key>.declined:1|c   the turn ended without it running
//
// <key> is toolcost.ToolKey's "<server>.<tool>[.<subkey>]" (builtin.<Tool>
// for Claude Code's own tools), the same key clown.tool_result.* uses.
//
// The sequences come from claude-code 2.1.308, recorded by
// `just explore-permission-hook-events` (docs/plans/2026-10-25-permission-hook-events.md):
// PreToolUse, PermissionRequest, then PostToolUse when the call is allowed and
// nothing when it is not. There is no event for a refusal and none for an
// interrupt, so "declined" is both: a request still pending when the turn ends
// (Stop, the next UserPromptSubmit, SessionEnd). A later PreToolUse is not
// used to decline, since parallel tool calls interleave.
//
// Hooks run as separate processes, so pending requests live in a small
// per-session state file. PermissionRequest carries no tool_use_id (and
// PreToolUse's cannot be carried over: nothing ties a PreToolUse to the
// PermissionRequest that follows it once calls run in parallel), so a request
// is paired with its PostToolUse by session, tool name and a hash of the
// canonical tool input (decoded and re-encoded, so key order and whitespace do
// not matter); two identical requests in one turn are paired first in, first
// out, which keeps the counts right even when it cannot say which ran. Inputs
// that canonicalise alike (duplicate keys, invalid UTF-8 replaced) pair with
// each other; that is harmless, since the tool name is part of the key and a
// mispairing within one tool still leaves the counts right. Past maxPending
// pending requests the oldest is dropped, counted neither approved nor
// declined.
//
// The state is best effort: up to maxReopen attempts share one lockWait
// deadline, and a hook that cannot get the lock in time skips the bookkeeping
// (a prompt still counts) rather than stall the agent. Files of sessions
// killed before their turn ended are swept: empty ones past staleAge, ones
// still holding a pending request past pendingStaleAge; the state dir (Dir)
// is not cleared on reboot, so the sweep is what bounds it. Only the state
// dir itself is checked (lstat: not a symlink, owned by this user); its
// parent is the user's own $XDG_STATE_HOME/clown/hook-toolcost, or the
// sticky temp dir in the last-resort fallback.
package permstat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"code.linenisgreat.com/clown/internal/toolcost"
	"code.linenisgreat.com/clown/internal/userpath"
)

// lockWait bounds the wait for a state file's lock.
const lockWait = 200 * time.Millisecond

// maxReopen bounds the attempts when the state file is replaced between open
// and lock (a turn boundary removed it); all of them share one lockWait
// deadline.
const maxReopen = 5

// The sweep of abandoned state files: an empty file is stale past staleAge,
// one still holding a pending request past pendingStaleAge; one sweep removes
// at most maxSweep and looks at no more than maxSweepScan entries.
const (
	staleAge        = 24 * time.Hour
	pendingStaleAge = 7 * 24 * time.Hour
	maxSweep        = 16
	maxSweepScan    = 4096
)

// maxPending bounds the pending list; past it the oldest request is dropped
// uncounted (conservative: it is neither approved nor declined).
const maxPending = 32

// maxStateBytes bounds what is read of a state file.
const maxStateBytes = 64 << 10

const stateVersion = 1

type state struct {
	Version int       `json:"version"`
	Pending []pending `json:"pending,omitempty"`
}

// pending is one PermissionRequest awaiting its outcome.
type pending struct {
	// Key is the metric key (toolcost.ToolKey).
	Key string `json:"key"`
	// Match is a hash of tool_name and tool_input, to pair the request with
	// its PostToolUse.
	Match string `json:"match"`
}

type hookInput struct {
	HookEventName string          `json:"hook_event_name"`
	SessionID     string          `json:"session_id"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
}

// Events lists the hook events Handle acts on.
var Events = []string{"PermissionRequest", "PostToolUse", "PostToolUseFailure", "UserPromptSubmit", "Stop", "SessionEnd"}

// Handle processes one hook payload with the per-session state kept under
// dir and returns the counters it produces. It never writes to stdout: a
// PermissionRequest hook that prints a decision would make it.
func Handle(payload []byte, dir string) ([]toolcost.Metric, error) {
	var in hookInput
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, fmt.Errorf("parsing hook input: %w", err)
	}
	if in.SessionID == "" {
		return nil, errors.New("hook input carries no session_id")
	}
	path := statePath(dir, in.SessionID)
	switch in.HookEventName {
	case "PermissionRequest":
		if in.ToolName == "" {
			return nil, errors.New("PermissionRequest carries no tool_name")
		}
		key := toolcost.ToolKey(in.ToolName, in.ToolInput)
		ms := []toolcost.Metric{counter(key, "prompted")}
		err := update(path, true, func(st *state) bool {
			st.Pending = append(st.Pending, pending{Key: key, Match: match(in)})
			if n := len(st.Pending); n > maxPending {
				st.Pending = st.Pending[n-maxPending:]
			}
			return true
		})
		if err == nil {
			err = sweepStale(dir, path)
		}
		// The prompt counts even when the state cannot be kept.
		return ms, err
	case "PostToolUse", "PostToolUseFailure":
		var ms []toolcost.Metric
		m := match(in)
		// No file: nothing is pending (an auto-allowed call), and none is made.
		err := update(path, false, func(st *state) bool {
			for i, p := range st.Pending {
				if p.Match == m {
					ms = append(ms, counter(p.Key, "approved"))
					st.Pending = append(st.Pending[:i], st.Pending[i+1:]...)
					return true
				}
			}
			return false // an auto-allowed call: nothing pending, nothing to write
		})
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		return ms, err
	case "Stop", "UserPromptSubmit", "SessionEnd":
		st, err := take(path)
		var ms []toolcost.Metric
		for _, p := range st.Pending {
			ms = append(ms, counter(p.Key, "declined"))
		}
		return ms, err
	}
	return nil, nil
}

func counter(key, outcome string) toolcost.Metric {
	return toolcost.Metric{Name: "clown.permission." + key + "." + outcome, Kind: toolcost.KindCounter, Value: 1}
}

func match(in hookInput) string {
	sum := sha256.Sum256(append([]byte(in.ToolName+"\x00"), canonicalJSON(in.ToolInput)...))
	return hex.EncodeToString(sum[:16])
}

// canonicalJSON re-encodes raw with sorted keys and no insignificant
// whitespace (numbers kept as written); raw that does not decode is used as
// is.
func canonicalJSON(raw json.RawMessage) []byte {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

// statePath is <dir>/<sha256(session)>.json.
func statePath(dir, session string) string {
	sum := sha256.Sum256([]byte(session))
	return filepath.Join(dir, hex.EncodeToString(sum[:16])+".json")
}

// Dir is where the state files live: $XDG_STATE_HOME/clown/hook-toolcost/
// permstat (~/.local/state without XDG_STATE_HOME), beside the toolcost
// hook's own state and resolved the same way (userpath.StatePath). An
// --agent-scope unit binds that hook-toolcost dir read-write and sets
// XDG_STATE_HOME to the frontend's, so the path is the same inside and out
// and all three counters work there; XDG_RUNTIME_DIR is a read-only tmpfs in
// a unit. Only when neither XDG_STATE_HOME nor a home dir is known does it
// fall back to a per-user directory under the temp dir.
func Dir() string {
	if d, err := userpath.StatePath("hook-toolcost", "permstat"); err == nil {
		return d
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("clown-permstat-%d", os.Getuid()))
}

// update applies fn to the state at path under an exclusive lock (hooks for
// parallel tool calls run concurrently) and writes it back when fn says so.
// Without create, a missing file is os.ErrNotExist and nothing is made.
func update(path string, create bool, fn func(*state) bool) error {
	f, err := lockState(path, create)
	if err != nil {
		return err
	}
	defer f.Close()
	st := readState(f)
	if !fn(&st) {
		return nil
	}
	st.Version = stateVersion
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err = f.WriteAt(data, 0)
	return err
}

// take returns the state at path and removes the file (the turn is over).
func take(path string) (state, error) {
	f, err := lockState(path, false)
	if errors.Is(err, os.ErrNotExist) {
		return state{}, nil
	}
	if err != nil {
		return state{}, err
	}
	defer f.Close()
	st := readState(f)
	// Removed under the lock: a hook that opened this file and is waiting on
	// it sees, once it gets the lock, that the path no longer names it
	// (lockState) and opens afresh, instead of writing to the unlinked file.
	return st, os.Remove(path)
}

// lockState opens the state file 0600 in a private directory (creating both
// when create is set), refusing a symlink at its path, and locks it. It
// returns only a file that the path still names once locked.
func lockState(path string, create bool) (*os.File, error) {
	if err := ensureDir(filepath.Dir(path), create); err != nil {
		return nil, err
	}
	flags := os.O_RDWR | syscall.O_NOFOLLOW
	if create {
		flags |= os.O_CREATE
	}
	// One deadline for all attempts, so a hook never waits much past lockWait.
	deadline := time.Now().Add(lockWait)
	for attempt := 0; attempt < maxReopen; attempt++ {
		f, err := os.OpenFile(path, flags, 0o600)
		if err != nil {
			return nil, err
		}
		if err := flockUntil(f, deadline); err != nil {
			f.Close()
			return nil, err
		}
		if afterLockHook != nil {
			afterLockHook()
		}
		fi, ferr := f.Stat()
		pi, perr := os.Lstat(path)
		if ferr == nil && perr == nil && os.SameFile(fi, pi) {
			return f, nil
		}
		f.Close()
	}
	return nil, fmt.Errorf("state file %s kept being replaced", path)
}

// afterLockHook, when set (tests), runs in lockState between taking the lock
// and checking that the path still names the locked file.
var afterLockHook func()

// flockUntil takes an exclusive lock on f, giving up at deadline.
func flockUntil(f *os.File, deadline time.Time) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("state file locked for over %v; skipping", lockWait)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ensureDir makes sure dir is a real directory (not a symlink) owned by this
// user and closed to others, creating it when create is set. Without create
// a missing dir is os.ErrNotExist.
func ensureDir(dir string, create bool) error {
	if create {
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return err
		}
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("state dir %s is not a plain directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("state dir %s is not owned by this user", dir)
	}
	// Keep it private even if it was made with a wider umask or by an older
	// build. It was just seen as a directory we own, not a symlink.
	if fi.Mode().Perm()&0o077 != 0 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// sweepStale removes up to maxSweep abandoned state files in dir (sessions
// killed before their turn ended), other than keep, looking at no more than
// maxSweepScan entries. mtime is the last write, not liveness (a person can
// sit at a dialog for a day), so a file still holding a pending request is
// removed only past pendingStaleAge; an empty or unreadable one past
// staleAge.
func sweepStale(dir, keep string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	removed, scanned := 0, 0
	for scanned < maxSweepScan && removed < maxSweep {
		entries, err := d.ReadDir(256)
		for _, e := range entries {
			scanned++
			if removed == maxSweep {
				break
			}
			path := filepath.Join(dir, e.Name())
			if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") || path == keep {
				continue
			}
			fi, ierr := e.Info()
			if ierr != nil {
				continue // removed meanwhile
			}
			age := time.Since(fi.ModTime())
			if age <= staleAge || (age <= pendingStaleAge && len(loadState(path).Pending) > 0) {
				continue
			}
			if os.Remove(path) == nil {
				removed++
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("sweeping %s: %w", dir, err)
		}
	}
	return nil
}

// readState reads a bounded state; anything unreadable is the empty state.
func readState(f *os.File) state {
	data, err := io.ReadAll(io.LimitReader(io.NewSectionReader(f, 0, maxStateBytes), maxStateBytes))
	if err != nil {
		return state{}
	}
	var st state
	if json.Unmarshal(data, &st) != nil || st.Version != stateVersion {
		return state{}
	}
	return st
}

// loadState reads the state at path without locking it (the sweep, tests).
func loadState(path string) state {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return state{}
	}
	defer f.Close()
	return readState(f)
}
