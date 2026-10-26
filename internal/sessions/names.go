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
	"bytes"
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
//
// ProfileForm (clown#298) is the redacted form of the pinned profile
// (pkg/profilelookup Resolved.RedactedForm), so a later refusal can show a
// field diff after the definition changed. It is stored only beside a
// profile, only when it is valid JSON of at most MaxProfileFormBytes; when
// one was given but not stored, ProfileFormOmitted says why. Both are
// omitted otherwise, and readers that predate them ignore them.
//
// AgentScope (clown#318) is the --agent-scope spec path of a confined
// launch, so `clown resume` relaunches the session confined (AgentScopeOf).
type NameRecord struct {
	SessionID          string          `json:"session_id"`
	Name               string          `json:"name"`
	Group              string          `json:"group,omitempty"`
	Profile            string          `json:"profile,omitempty"`
	ProfileDigest      string          `json:"profile_digest,omitempty"`
	ProfileForm        json.RawMessage `json:"profile_form,omitempty"`
	ProfileFormOmitted string          `json:"profile_form_omitted,omitempty"`
	AgentScope         string          `json:"agent_scope,omitempty"`
	AgentScopeDigest   string          `json:"agent_scope_digest,omitempty"`
	TS                 time.Time       `json:"ts"`
}

// AgentScopeRecord is a session's confinement record: the spec path and the
// sha256 of the spec bytes its last confined launch ran under.
type AgentScopeRecord struct {
	Spec, Digest string
}

// AgentScopeRecordOf is AgentScopeOf with the digest recorded beside the
// spec: both from the LAST record for id that carries a spec.
func AgentScopeRecordOf(id string) (rec AgentScopeRecord, ok bool, err error) {
	err = scanRecords(func(line string, r *NameRecord) error {
		if r == nil {
			if strings.Contains(line, `"agent_scope`) && strings.Contains(line, id) {
				return fmt.Errorf("session-names sidecar holds an unparseable agent-scope record for session %s: %.200s", id, line)
			}
			return nil
		}
		if r.SessionID == id && r.AgentScope != "" {
			rec, ok = AgentScopeRecord{Spec: r.AgentScope, Digest: r.AgentScopeDigest}, true
		}
		return nil
	})
	if err != nil {
		return AgentScopeRecord{}, false, err
	}
	return rec, ok, nil
}

// AnyAgentScope reports whether any session at all is recorded as confined.
// A launch that names its conversation only by a search term cannot be
// checked against one id, so it is refused while this holds. It fails
// closed: an unparseable line that carries the spec key is an error.
func AnyAgentScope() (bool, error) {
	var found bool
	err := scanRecords(func(line string, rec *NameRecord) error {
		if rec == nil {
			if strings.Contains(line, `"agent_scope`) {
				return fmt.Errorf("session-names sidecar holds an unparseable agent-scope record: %.200s", line)
			}
			return nil
		}
		if rec.AgentScope != "" {
			found = true
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

// AgentScopeOf returns the agent-scope spec path session id was last
// launched confined under (clown#318): the LAST record for id that carries
// one. A later record without one does not clear it, so a confined session
// never silently becomes unconfined. ok is false when no record for id ever
// carried one.
//
// Like ProfileOf it gates `clown resume`, so it fails closed: any failure
// to read the sidecar (other than its absence) is returned, and so is an
// unparseable line that mentions id and carries the spec key.
//
// The sidecar lives under $XDG_STATE_HOME/clown, which an agent unit does
// not see (only hook-toolcost and hook-tee below it are bound in), so a
// confined agent cannot rewrite its own record.
func AgentScopeOf(id string) (spec string, ok bool, err error) {
	rec, ok, err := AgentScopeRecordOf(id)
	return rec.Spec, ok, err
}

// MaxProfileFormBytes caps a stored ProfileForm. A larger form is not
// stored (the pin is), so one record stays one short line.
const MaxProfileFormBytes = 8192

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
// none of a name, a profile or an agent scope, is a silent no-op.
//
// Write shape (not crash durability): the whole record, newline included,
// goes out in ONE write(2) on an O_APPEND descriptor, so it is appended as a
// unit and concurrent writers do not interleave inside a line. There is no
// fsync: a crash can lose the record or, on some filesystems, leave part of
// it. A failed or short write (ENOSPC, say) can also leave a fragment at the
// end of the file. Before writing, endsMidLine checks (with a separate read,
// so another writer can race it) whether the file ends mid-line; if so the
// record is prefixed with a newline in the same write so it is not glued to
// the fragment. A fragment left after that check ran (by a concurrent writer
// or this write failing) is not caught until the next record's check.
// TruncatedTail reports a fragment at the end.
func RecordSession(rec NameRecord) error {
	if rec.SessionID == "" || (rec.Name == "" && rec.Profile == "" && rec.AgentScope == "") {
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
	rec.ProfileForm, rec.ProfileFormOmitted = storableProfileForm(rec)
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	buf := make([]byte, 0, len(line)+2)
	if endsMidLine(path) {
		buf = append(buf, '\n')
	}
	buf = append(append(buf, line...), '\n')
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(buf)
	return err
}

// storableProfileForm returns the form RecordSession writes for rec and,
// when a form was given but is not written, why.
func storableProfileForm(rec NameRecord) (json.RawMessage, string) {
	switch {
	case len(rec.ProfileForm) == 0:
		return nil, ""
	case rec.Profile == "":
		return nil, "" // a form describes a pin; without one there is nothing to describe
	case len(rec.ProfileForm) > MaxProfileFormBytes:
		return nil, fmt.Sprintf("exceeds %d bytes", MaxProfileFormBytes)
	case !json.Valid(rec.ProfileForm):
		return nil, "not valid JSON"
	}
	return rec.ProfileForm, ""
}

// ProfileFormFor returns the redacted form stored with a pin of digest: the
// LAST record for digest that carries one. ok is false when none does. Like
// NamesFor it is an enrichment: unparseable lines are skipped, and only a
// failure to open or read the sidecar (other than its absence) is returned.
// The form is what the writer claimed; nothing ties it to the digest.
func ProfileFormFor(digest string) (form []byte, ok bool, err error) {
	if digest == "" {
		return nil, false, nil
	}
	err = scanRecords(func(_ string, rec *NameRecord) error {
		if rec != nil && rec.ProfileDigest == digest && len(rec.ProfileForm) > 0 {
			form, ok = rec.ProfileForm, true
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return form, ok, nil
}

// endsMidLine reports whether the file at path is non-empty and does not end
// in a newline. Unreadable or missing reads as false.
func endsMidLine(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return false
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, info.Size()-1); err != nil {
		return false
	}
	return last[0] != '\n'
}

// TruncatedTail reports whether the sidecar's last line is incomplete: no
// trailing newline, or a final line that does not parse. That is what a
// crash mid-write leaves. It returns the sidecar path so a caller can name
// it in a warning. A missing sidecar is not truncated.
func TruncatedTail() (path string, truncated bool, err error) {
	path, err = namesPath()
	if err != nil {
		return "", false, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return path, false, nil
		}
		return path, false, err
	}
	if len(b) == 0 {
		return path, false, nil
	}
	if b[len(b)-1] != '\n' {
		return path, true, nil
	}
	body := b[:len(b)-1]
	last := body[bytes.LastIndexByte(body, '\n')+1:]
	var rec NameRecord
	return path, len(last) > 0 && json.Unmarshal(last, &rec) != nil, nil
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
