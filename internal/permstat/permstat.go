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
// out, which keeps the counts right even when it cannot say which ran.
//
// The state is best effort: the lock is waited on for lockWait at most, and a
// hook that cannot get it skips the bookkeeping (a prompt still counts) rather
// than stall the agent. Files of sessions killed before their turn ended are
// swept once they are staleAge old.
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
)

// lockWait bounds the wait for a state file's lock.
const lockWait = 200 * time.Millisecond

// maxReopen bounds the retries when the state file is replaced between open
// and lock (a turn boundary removed it).
const maxReopen = 5

// staleAge and maxSweep bound the sweep of abandoned state files.
const (
	staleAge = 24 * time.Hour
	maxSweep = 16
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
			sweepStale(dir, path)
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

// Dir is where the state files live: $XDG_RUNTIME_DIR/clown/permstat, or,
// without a runtime dir, a per-user directory under the temp dir.
func Dir() string {
	if rt := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(rt) {
		return filepath.Join(rt, "clown", "permstat")
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
	for attempt := 0; attempt < maxReopen; attempt++ {
		f, err := os.OpenFile(path, flags, 0o600)
		if err != nil {
			return nil, err
		}
		if err := flockWithin(f, lockWait); err != nil {
			f.Close()
			return nil, err
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

// flockWithin takes an exclusive lock on f, giving up after wait.
func flockWithin(f *os.File, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("state file locked for over %v; skipping", wait)
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

// sweepStale removes up to maxSweep state files in dir older than staleAge
// (sessions killed before their turn ended), other than keep. Best effort.
func sweepStale(dir, keep string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	entries, _ := d.ReadDir(256)
	removed := 0
	for _, e := range entries {
		if removed == maxSweep {
			return
		}
		path := filepath.Join(dir, e.Name())
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") || path == keep {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > staleAge {
			if os.Remove(path) == nil {
				removed++
			}
		}
	}
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

// loadState reads the state at path (tests).
func loadState(path string) state {
	f, err := os.Open(path)
	if err != nil {
		return state{}
	}
	defer f.Close()
	return readState(f)
}
