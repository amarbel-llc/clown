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
// per-session state file. PermissionRequest carries no tool_use_id, so a
// request is paired with its PostToolUse by session, tool name and a hash of
// the tool input; two identical requests in one turn are paired first in,
// first out, which keeps the counts right even when it cannot say which ran.
package permstat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"code.linenisgreat.com/clown/internal/toolcost"
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
		err := update(path, func(st *state) bool {
			st.Pending = append(st.Pending, pending{Key: key, Match: match(in)})
			if n := len(st.Pending); n > maxPending {
				st.Pending = st.Pending[n-maxPending:]
			}
			return true
		})
		// The prompt counts even when the state cannot be kept.
		return ms, err
	case "PostToolUse", "PostToolUseFailure":
		var ms []toolcost.Metric
		m := match(in)
		err := update(path, func(st *state) bool {
			for i, p := range st.Pending {
				if p.Match == m {
					ms = append(ms, counter(p.Key, "approved"))
					st.Pending = append(st.Pending[:i], st.Pending[i+1:]...)
					return true
				}
			}
			return false // an auto-allowed call: nothing pending, nothing to write
		})
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
	sum := sha256.Sum256(append([]byte(in.ToolName+"\x00"), in.ToolInput...))
	return hex.EncodeToString(sum[:16])
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
func update(path string, fn func(*state) bool) error {
	f, err := openState(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
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
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return state{}, nil
	}
	if err != nil {
		return state{}, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return state{}, err
	}
	st := readState(f)
	return st, os.Remove(path)
}

// openState opens (creating) the state file 0600 in a 0700 directory,
// refusing a symlink at its path.
func openState(path string) (*os.File, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// The directory is this package's own (Dir): keep it private even if it
	// was created with a wider umask or by an older build.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
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
