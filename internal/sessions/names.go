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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code.linenisgreat.com/clown/internal/userpath"
)

// NameRecord is one line of the session-names sidecar.
//
// Profile and ProfileDigest (FDR 0022, clown#295) are the named profile the
// launch applied and its pkg/profilelookup DigestSHA256. Both are omitted
// when no profile was applied, so records written before them stay valid
// and a profileless record reads exactly as it always did. The pair is a
// claim of what the session launched with, not an authorization: `clown
// resume` refuses when the name no longer resolves to the recorded digest.
type NameRecord struct {
	SessionID     string    `json:"session_id"`
	Name          string    `json:"name"`
	Group         string    `json:"group,omitempty"`
	Profile       string    `json:"profile,omitempty"`
	ProfileDigest string    `json:"profile_digest,omitempty"`
	TS            time.Time `json:"ts"`
}

// UnpinnableDigest is the ProfileDigest recorded when a launch applied a
// profile that pkg/profilelookup could not vouch for (for example an
// unstamped registry), so no digest exists. It never equals a real digest,
// so a resume of that session refuses rather than drop the profile.
const UnpinnableDigest = "unpinnable"

// ProfilePin is the profile a session was launched with.
type ProfilePin struct {
	Name   string
	Digest string
}

// namesPath resolves $XDG_STATE_HOME/clown/session-names.jsonl (throwaway
// coordination state, never user-edited) via the shared userpath ladder.
func namesPath() (string, error) {
	return userpath.StatePath("session-names.jsonl")
}

// RecordSessionName appends one {session_id, name, group, ts} record.
// Best-effort by contract: callers treat a failure as non-fatal, since a
// missed record only degrades name-based resume for that conversation.
// Empty id or name is a silent no-op (nothing meaningful to record).
func RecordSessionName(id, name, group string) error {
	if name == "" {
		return nil
	}
	return RecordSession(NameRecord{SessionID: id, Name: name, Group: group})
}

// RecordSession appends rec, stamped with the current time. Same
// best-effort contract as RecordSessionName. A record with no id, or with
// neither a name nor a profile, is a silent no-op.
func RecordSession(rec NameRecord) error {
	if rec.SessionID == "" || (rec.Name == "" && rec.Profile == "") {
		return nil
	}
	path, err := namesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	rec.TS = time.Now().UTC()
	line, err := json.Marshal(rec)
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
//
// Reads with bufio.Reader.ReadString rather than bufio.Scanner: every line
// here is written by RecordSessionName itself (never external input), so
// trading Scanner's fixed max-token-size for guaranteed forward progress is
// the right call. A Scanner permanently stops at the first oversized or
// otherwise-erroring line (e.g. bufio.ErrTooLong), which would silently
// discard every record after it in this never-pruned file; ReadString keeps
// advancing line by line regardless of any single line's length.
func NamesFor(ids []string) map[string]string {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}

	out := map[string]string{}
	err := scanRecords(func(_ string, rec *NameRecord) error {
		if rec != nil && want[rec.SessionID] && rec.Name != "" {
			out[rec.SessionID] = rec.Name
		}
		return nil
	})
	if err != nil {
		return nil
	}
	return out
}

// ProfileOf returns the profile session id was last launched with: the
// LAST record for id that carries a profile. A later record without one
// (a launch that applied no profile) does not clear it, so a session's
// recorded profile never disappears silently. ok is false when no record
// for id ever carried a profile.
//
// Unlike NamesFor, ProfileOf gates `clown resume`, so it fails closed:
// only a nonexistent sidecar means "no pin". Any other open or read failure
// is returned, and so is an unparseable line that mentions id and carries
// a profile key (a torn write of this session's pin). Unparseable lines
// that cannot be this session's pin are skipped, so one bad line elsewhere
// in this never-pruned file does not block every resume.
func ProfileOf(id string) (pin ProfilePin, ok bool, err error) {
	err = scanRecords(func(line string, rec *NameRecord) error {
		if rec == nil {
			if strings.Contains(line, `"profile`) && strings.Contains(line, id) {
				return fmt.Errorf("session-names sidecar holds an unparseable profile record for session %s: %.200s", id, line)
			}
			return nil
		}
		if rec.SessionID == id && rec.Profile != "" {
			pin, ok = ProfilePin{Name: rec.Profile, Digest: rec.ProfileDigest}, true
		}
		return nil
	})
	if err != nil {
		return ProfilePin{}, false, err
	}
	return pin, ok, nil
}

// scanRecords calls fn for every non-empty sidecar line in file order, with
// the parsed record or nil when the line does not parse; the first error fn
// returns stops the scan. A nonexistent sidecar is no records and no error;
// any other failure to open or read it is returned.
func scanRecords(fn func(line string, rec *NameRecord) error) error {
	path, err := namesPath()
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	for {
		line, readErr := r.ReadString('\n')
		if trimmed := strings.TrimRight(line, "\n"); trimmed != "" {
			var rec *NameRecord
			var parsed NameRecord
			if json.Unmarshal([]byte(trimmed), &parsed) == nil {
				rec = &parsed
			}
			if err := fn(trimmed, rec); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
	}
}

// NameOf returns the recorded clown-name for one session id, or "" when
// none was ever recorded.
func NameOf(id string) string {
	return NamesFor([]string{id})[id]
}
