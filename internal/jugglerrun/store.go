package jugglerrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"code.linenisgreat.com/ringmaster/pkgs/jobwake"
)

// RecordSchema is the version of the run and child records.
const RecordSchema = 1

// Store is juggler's lifecycle state directory:
//
//	<root>/runs/<run-key>.json                       RunRecord
//	<root>/runs/<run-key>/ledger.json                RunLedger
//	<root>/runs/<run-key>/children/<digest>.json     ChildRecord
//	<root>/runs/<run-key>/children/<digest>.brief.toml   the staged brief
//	<root>/exit-wakes/<job>                          sent-wake marker
//
// The run key and the brief digest are the idempotency keys of `juggler
// spawn --new-run` and `juggler spawn --brief` respectively.
type Store struct {
	Root string
}

// DefaultStore is $XDG_STATE_HOME/juggler, falling back to
// ~/.local/state/juggler.
func DefaultStore() (Store, error) {
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return Store{Root: filepath.Join(base, "juggler")}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Store{}, fmt.Errorf("resolving state dir: %w", err)
	}
	return Store{Root: filepath.Join(home, ".local", "state", "juggler")}, nil
}

func (s Store) runsDir() string                 { return filepath.Join(s.Root, "runs") }
func (s Store) runPath(key string) string       { return filepath.Join(s.runsDir(), key+".json") }
func (s Store) runDir(key string) string        { return filepath.Join(s.runsDir(), key) }
func (s Store) RunLedgerPath(key string) string { return filepath.Join(s.runDir(key), "ledger.json") }
func (s Store) childrenDir(key string) string   { return filepath.Join(s.runDir(key), "children") }
func (s Store) childPath(key, digest string) string {
	return filepath.Join(s.childrenDir(key), digestFileName(digest)+".json")
}

func (s Store) stagedBriefPath(key, digest string) string {
	return filepath.Join(s.childrenDir(key), digestFileName(digest)+".brief.toml")
}
func (s Store) wakeMarkerPath(job string) string { return filepath.Join(s.Root, "exit-wakes", job) }

// CredentialPath is where principal's troupe password is written:
// <run dir>/<principal>.pw, one file per identity, in a 0700 directory it
// creates. The run key is the run's own, so a teardown can delete them all.
func (s Store) CredentialPath(runKey, principal string) (string, error) {
	if err := os.MkdirAll(s.runDir(runKey), 0o700); err != nil {
		return "", err
	}
	return filepath.Join(s.runDir(runKey), principal+".pw"), nil
}

// runChildren lists the run's subagent records.
func (s Store) runChildren(runKey string) ([]*ChildRecord, error) {
	paths, err := filepath.Glob(filepath.Join(s.childrenDir(runKey), "*.json"))
	if err != nil {
		return nil, err
	}
	var out []*ChildRecord
	for _, p := range paths {
		var rec ChildRecord
		if ok, err := readJSONFile(p, &rec); err != nil {
			return nil, err
		} else if ok {
			out = append(out, &rec)
		}
	}
	return out, nil
}

// digestFileName turns "sha256:<hex>" into a filename-safe "sha256-<hex>".
func digestFileName(digest string) string {
	b := []byte(digest)
	for i, c := range b {
		if c == ':' {
			b[i] = '-'
		}
	}
	return string(b)
}

// idPattern is a safe single path segment: run keys and ringmaster job ids.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidRunKey rejects a run key that is not a safe single path segment.
func ValidRunKey(key string) error {
	if !idPattern.MatchString(key) {
		return fmt.Errorf("run key %q must match %s", key, idPattern)
	}
	return nil
}

func validJobID(job string) error {
	if !idPattern.MatchString(job) {
		return fmt.Errorf("job id %q is not a valid ringmaster job id", job)
	}
	return nil
}

// RunRecord is one run (FDR 0019 §2): its root principal, room and run job.
type RunRecord struct {
	Schema            int         `json:"schema"`
	RunKey            string      `json:"run_key"`
	Issuer            string      `json:"issuer"`
	RootPrincipal     string      `json:"root_principal"`
	RootJID           string      `json:"root_jid"`
	RootCredentialRef string      `json:"root_credential_ref"`
	Room              string      `json:"room"`
	RoomCreated       bool        `json:"room_created"`
	RootStanza        string      `json:"root_stanza"`
	RunJob            string      `json:"run_job"`
	Holders           []Holder    `json:"holders"`
	CreatedAt         time.Time   `json:"created_at"`
	Resolved          *Resolution `json:"resolved,omitempty"`
	// TornDown is set by the teardown brief once the run's accounts and
	// credentials are gone; false until then.
	TornDown bool `json:"torn_down"`
}

// ChildRecord is one subagent spawned into a run.
type ChildRecord struct {
	Schema        int       `json:"schema"`
	RunKey        string    `json:"run_key"`
	BriefDigest   string    `json:"brief_digest"`
	Principal     string    `json:"principal"`
	JID           string    `json:"jid"`
	CredentialRef string    `json:"credential_ref"`
	Parent        string    `json:"parent"`
	Room          string    `json:"room"`
	Job           string    `json:"job"`
	BriefStanza   string    `json:"brief_stanza"`
	BriefPath     string    `json:"brief_path"`
	Unit          string    `json:"unit"`
	WallClock     string    `json:"wall_clock"`
	Holders       []Holder  `json:"holders"`
	CreatedAt     time.Time `json:"created_at"`
}

func (s Store) LoadRun(key string) (*RunRecord, error) {
	var rec RunRecord
	ok, err := readJSONFile(s.runPath(key), &rec)
	if err != nil || !ok {
		return nil, err
	}
	return &rec, nil
}

func (s Store) SaveRun(rec *RunRecord) error { return writeJSONAtomic(s.runPath(rec.RunKey), rec) }

func (s Store) LoadChild(key, digest string) (*ChildRecord, error) {
	var rec ChildRecord
	ok, err := readJSONFile(s.childPath(key, digest), &rec)
	if err != nil || !ok {
		return nil, err
	}
	return &rec, nil
}

func (s Store) SaveChild(rec *ChildRecord) error {
	return writeJSONAtomic(s.childPath(rec.RunKey, rec.BriefDigest), rec)
}

func (s Store) runs() ([]*RunRecord, error) {
	paths, err := filepath.Glob(filepath.Join(s.runsDir(), "*.json"))
	if err != nil {
		return nil, err
	}
	var out []*RunRecord
	for _, p := range paths {
		var rec RunRecord
		if ok, err := readJSONFile(p, &rec); err != nil {
			return nil, err
		} else if ok {
			out = append(out, &rec)
		}
	}
	return out, nil
}

// FindRunByRoom returns the run whose room is room, or nil.
func (s Store) FindRunByRoom(room string) (*RunRecord, error) {
	return s.findRun(func(r *RunRecord) bool { return r.Room == room })
}

// FindRunByJob returns the run whose run job is job, or nil.
func (s Store) FindRunByJob(job string) (*RunRecord, error) {
	return s.findRun(func(r *RunRecord) bool { return r.RunJob == job })
}

func (s Store) findRun(match func(*RunRecord) bool) (*RunRecord, error) {
	runs, err := s.runs()
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		if match(r) {
			return r, nil
		}
	}
	return nil, nil
}

// FindChildByJob returns the subagent whose ringmaster job is job, or nil.
func (s Store) FindChildByJob(job string) (*ChildRecord, error) {
	paths, err := filepath.Glob(filepath.Join(s.runsDir(), "*", "children", "*.json"))
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		var rec ChildRecord
		ok, err := readJSONFile(p, &rec)
		if err != nil {
			return nil, err
		}
		if ok && rec.Job == job {
			return &rec, nil
		}
	}
	return nil, nil
}

// lock takes an exclusive flock on path (created if absent) and returns its
// release. It serialises concurrent spawns of the same idempotency key.
func lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func readJSONFile(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("parsing %s: %w", path, err)
	}
	return true, nil
}

func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'))
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// NewPrincipal mints a fresh per-instance key: a random UUIDv4 (FDR 0032 D1),
// from the same generator clown uses for its own session keys.
func NewPrincipal() string { return jobwake.NewUUID() }
