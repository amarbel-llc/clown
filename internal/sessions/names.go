// Session-name sidecar for `clown resume repo/worktree/<clown-name>`
// (clown#192 step 3).
//
// $XDG_STATE_HOME/clown/session-names.jsonl is an append-only journal of
// {session_id, name, group, ts} records, written once per proceeding
// launch at the point where both the claude session id and the CLOWN_NAME
// are known (cmd/clown's runWithFlags, after decideClaudeSession fixes the
// id and any [attach] re-exec has decided which process carries on). The
// clown-name itself is an ephemeral live-session handle (internal/clownname
// recycles it); this journal is what lets a DEAD conversation still be
// addressed by the name it wore. Never pruned by design: records are tiny,
// and a resume simply appends a fresh record for the same id, so the LAST
// record per id wins.
package sessions

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// NameRecord is one line of the session-names sidecar.
type NameRecord struct {
	SessionID string    `json:"session_id"`
	Name      string    `json:"name"`
	Group     string    `json:"group,omitempty"`
	TS        time.Time `json:"ts"`
}

// namesPath resolves $XDG_STATE_HOME/clown/session-names.jsonl, or
// ~/.local/state/clown/session-names.jsonl when XDG_STATE_HOME is unset —
// mirroring internal/clownname's lockPath (throwaway coordination state,
// never user-edited).
func namesPath() (string, error) {
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return filepath.Join(base, "clown", "session-names.jsonl"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "clown", "session-names.jsonl"), nil
}

// RecordSessionName appends one {session_id, name, group, ts} record.
// Best-effort by contract: callers treat a failure as non-fatal, since a
// missed record only degrades name-based resume for that conversation.
// Empty id or name is a silent no-op (nothing meaningful to record).
func RecordSessionName(id, name, group string) error {
	if id == "" || name == "" {
		return nil
	}
	path, err := namesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(NameRecord{SessionID: id, Name: name, Group: group, TS: time.Now().UTC()})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// NamesFor returns the recorded clown-name for each of ids that has one.
// The LAST record per id wins — a resumed conversation re-records under
// the name its new live process claimed. A missing sidecar or unreadable
// lines degrade to an empty map / skipped lines: the sidecar is an
// enrichment, never a gate.
func NamesFor(ids []string) map[string]string {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}

	path, err := namesPath()
	if err != nil {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	out := map[string]string{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var rec NameRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		if want[rec.SessionID] {
			out[rec.SessionID] = rec.Name
		}
	}
	return out
}

// NameOf returns the recorded clown-name for one session id, or "" when
// none was ever recorded.
func NameOf(id string) string {
	return NamesFor([]string{id})[id]
}
