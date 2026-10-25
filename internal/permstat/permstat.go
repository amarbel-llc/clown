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
	"regexp"
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
	// maxSweepLoads and maxSweepLoadBytes bound the state files one sweep
	// reads (to tell a pending file from an empty one).
	maxSweepLoads     = 64
	maxSweepLoadBytes = 1 << 20
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
	name := stateFileName(in.SessionID)
	switch in.HookEventName {
	case "PermissionRequest":
		if in.ToolName == "" {
			return nil, errors.New("PermissionRequest carries no tool_name")
		}
		key := toolcost.ToolKey(in.ToolName, in.ToolInput)
		ms := []toolcost.Metric{counter(key, "prompted")}
		// The prompt counts even when the state cannot be kept.
		root, err := openStateDir(dir, true)
		if err != nil {
			return ms, err
		}
		defer root.Close()
		err = update(root, name, true, func(st *state) bool {
			st.Pending = append(st.Pending, pending{Key: key, Match: match(in)})
			if n := len(st.Pending); n > maxPending {
				st.Pending = st.Pending[n-maxPending:]
			}
			return true
		})
		if err == nil {
			err = sweepStale(root, name)
		}
		return ms, err
	case "PostToolUse", "PostToolUseFailure":
		// No dir or no file: nothing is pending (an auto-allowed call), and
		// none is made.
		root, err := openStateDir(dir, false)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		defer root.Close()
		var ms []toolcost.Metric
		m := match(in)
		err = update(root, name, false, func(st *state) bool {
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
		root, err := openStateDir(dir, false)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		defer root.Close()
		st, err := take(root, name)
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

// stateFileName is a session's state file name, <sha256(session)[:16] hex>.json:
// the only shape stateFileShape (and so the sweep) accepts.
func stateFileName(session string) string {
	sum := sha256.Sum256([]byte(session))
	return hex.EncodeToString(sum[:16]) + ".json"
}

// stateFileShape matches exactly the names stateFileName makes.
var stateFileShape = regexp.MustCompile(`^[0-9a-f]{32}\.json$`)

// statePath is <dir>/<stateFileName(session)> (tests).
func statePath(dir, session string) string {
	return filepath.Join(dir, stateFileName(session))
}

// Dir is where the state files live: $XDG_STATE_HOME/clown/hook-toolcost/
// permstat (~/.local/state without XDG_STATE_HOME), beside the toolcost
// hook's own state and resolved the same way (userpath.StatePath). An
// --agent-scope unit binds that hook-toolcost dir read-write and sets
// XDG_STATE_HOME to the frontend's (absolute) value, so the path is the same
// inside and out and, inferred rather than seen live, all three counters work
// there; XDG_RUNTIME_DIR is a read-only tmpfs in a unit. Under --tent the
// variable is forwarded but the dir is not bound, so the state is
// container-local there. Only when neither an absolute XDG_STATE_HOME nor a
// home dir is known does it fall back to a per-user directory under the temp
// dir.
//
// The dir is writable by the agent (inside a unit it runs as the same uid)
// and used by the host-side hook too, so nothing in it is trusted: it is
// opened as an os.Root (openStateDir) and every operation stays inside it.
func Dir() string {
	if d, err := userpath.StatePath("hook-toolcost", "permstat"); err == nil {
		return d
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("clown-permstat-%d", os.Getuid()))
}

// update applies fn to the state file name in root under an exclusive lock
// (hooks for parallel tool calls run concurrently) and writes it back when fn
// says so. Without create, a missing file is os.ErrNotExist and nothing is
// made.
func update(root *os.Root, name string, create bool, fn func(*state) bool) error {
	f, err := lockState(root, name, create)
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

// take returns the state file name in root and removes it (the turn is
// over).
func take(root *os.Root, name string) (state, error) {
	f, err := lockState(root, name, false)
	if errors.Is(err, os.ErrNotExist) {
		return state{}, nil
	}
	if err != nil {
		return state{}, err
	}
	defer f.Close()
	st := readState(f)
	// Removed under the lock: a hook that opened this file and is waiting on
	// it sees, once it gets the lock, that the name no longer names it
	// (lockState) and opens afresh, instead of writing to the unlinked file.
	return st, root.Remove(name)
}

// lockState opens the state file name in root, 0600 (creating it when create
// is set), and locks it. It returns only a regular file that the name, not
// followed if it is a symlink, still names once locked.
func lockState(root *os.Root, name string, create bool) (*os.File, error) {
	// O_NONBLOCK: a FIFO planted at the name must not hang the open.
	flags := os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if create {
		flags |= os.O_CREATE
	}
	// One deadline for all attempts, so a hook never waits much past lockWait.
	deadline := time.Now().Add(lockWait)
	for attempt := 0; attempt < maxReopen; attempt++ {
		f, err := root.OpenFile(name, flags, 0o600)
		if err != nil {
			return nil, err
		}
		// A state file is only ever regular; anything else at its name was
		// planted, and reopening would find it again, so refuse at once.
		if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
			f.Close()
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("state file %s is not a regular file (%s)", name, fi.Mode().Type())
		}
		if err := flockUntil(f, deadline); err != nil {
			f.Close()
			return nil, err
		}
		if afterLockHook != nil {
			afterLockHook()
		}
		fi, ferr := f.Stat()
		pi, perr := root.Lstat(name)
		if ferr == nil && perr == nil && fi.Mode().IsRegular() && os.SameFile(fi, pi) {
			return f, nil
		}
		f.Close()
	}
	return nil, fmt.Errorf("state file %s kept being replaced", name)
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

// beforeRootOpenHook, when set (tests), runs in openStateDir between the
// lstat of the dir and opening it as a root.
var beforeRootOpenHook func()

// openStateDir opens dir as an os.Root, so that every later operation is
// relative to that directory and cannot leave it, creating it (0700) when
// create is set; without create a missing dir is os.ErrNotExist. dir must be
// a plain directory (a symlink is refused), and the root must be the very
// directory that was lstat'ed: a dir swapped for a symlink in between is
// refused before anything is done in it. It must be owned by this user,
// which protects only against other users; the agent, which can write the
// dir, is the same uid. Its mode is narrowed to 0700 through the opened
// directory (fchmod) when wider.
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
		return nil, fmt.Errorf("state dir %s is not a plain directory", dir)
	}
	if beforeRootOpenHook != nil {
		beforeRootOpenHook()
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
		return nil, fmt.Errorf("state dir %s was replaced while it was opened", dir)
	}
	if st, ok := ri.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		root.Close()
		return nil, fmt.Errorf("state dir %s is not owned by this user", dir)
	}
	// Keep it private even if it was made with a wider umask or by an older
	// build: fchmod on the opened directory, never a path.
	if ri.Mode().Perm()&0o077 != 0 {
		if err := d.Chmod(0o700); err != nil {
			root.Close()
			return nil, err
		}
	}
	return root, nil
}

// sweepLoadHook, when set (tests), is called for each state file the sweep
// reads.
var sweepLoadHook func()

// sweepStale removes up to maxSweep abandoned state files in root (sessions
// killed before their turn ended), other than keep, looking at no more than
// maxSweepScan entries and reading at most maxSweepLoads files
// (maxSweepLoadBytes in all), so one sweep does bounded work however the
// agent fills the dir. Only names of the state-file shape are ever removed,
// never other files however old. Under such a name:
//   - a directory is left alone;
//   - anything else that is not a regular file (a symlink, a FIFO) was
//     planted, a state file being only ever regular: the entry itself is
//     removed through the root, never what it points at;
//   - a regular file larger than maxStateBytes was planted too (a state file
//     is a few KiB): removed unread;
//   - otherwise mtime decides. It is the last write, not liveness (a person
//     can sit at a dialog for a day), so a file still holding a pending
//     request is removed only past pendingStaleAge, an empty or unreadable
//     one past staleAge. Telling the two apart needs a read; once the load
//     budget is spent, such files are left for a later sweep.
func sweepStale(root *os.Root, keep string) error {
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	removed, scanned, loads := 0, 0, 0
	var loadBytes int64
	remove := func(name string) {
		if root.Remove(name) == nil {
			removed++
		}
	}
	for {
		entries, err := d.ReadDir(256)
		for _, e := range entries {
			if scanned == maxSweepScan || removed == maxSweep {
				return nil
			}
			scanned++
			name := e.Name()
			if name == keep || !stateFileShape.MatchString(name) || e.IsDir() {
				continue
			}
			if !e.Type().IsRegular() {
				remove(name)
				continue
			}
			fi, ierr := root.Lstat(name)
			if ierr != nil {
				continue // removed meanwhile
			}
			if !fi.Mode().IsRegular() {
				if !fi.IsDir() { // replaced by a special file meanwhile
					remove(name)
				}
				continue
			}
			if fi.Size() > maxStateBytes {
				remove(name)
				continue
			}
			age := time.Since(fi.ModTime())
			if age <= staleAge {
				continue
			}
			if age <= pendingStaleAge {
				if loads == maxSweepLoads || loadBytes+fi.Size() > maxSweepLoadBytes {
					continue // budget spent: left for a later sweep
				}
				loads++
				loadBytes += fi.Size()
				if sweepLoadHook != nil {
					sweepLoadHook()
				}
				if len(loadStateIn(root, name).Pending) > 0 {
					continue
				}
			}
			remove(name)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("sweeping the state dir: %w", err)
		}
	}
}

// readState reads a bounded state; anything unreadable is the empty state.
// The file is agent-writable, so only entries whose key is a metric key of
// the toolcost alphabet are kept (a key reaches statsd), at most maxPending
// of them (the most recent).
func readState(f *os.File) state {
	data, err := io.ReadAll(io.LimitReader(io.NewSectionReader(f, 0, maxStateBytes), maxStateBytes))
	if err != nil {
		return state{}
	}
	var st state
	if json.Unmarshal(data, &st) != nil || st.Version != stateVersion {
		return state{}
	}
	valid := st.Pending[:0]
	for _, p := range st.Pending {
		if toolcost.ValidKey(p.Key) {
			valid = append(valid, p)
		}
	}
	if n := len(valid); n > maxPending {
		valid = valid[n-maxPending:]
	}
	st.Pending = valid
	return st
}

// loadStateIn reads the state file name in root without locking it (the
// sweep), refusing anything but the regular file the name itself is.
func loadStateIn(root *os.Root, name string) state {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return state{}
	}
	defer f.Close()
	fi, ferr := f.Stat()
	li, lerr := root.Lstat(name)
	if ferr != nil || lerr != nil || !fi.Mode().IsRegular() || !os.SameFile(fi, li) {
		return state{}
	}
	return readState(f)
}

// loadState reads the state at path without locking it (tests).
func loadState(path string) state {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return state{}
	}
	defer f.Close()
	return readState(f)
}
