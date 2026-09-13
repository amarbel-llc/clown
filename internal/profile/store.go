package profile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"code.linenisgreat.com/tommy/pkg/marshal"
)

// Save writes profiles to path as a `[[profile]]` TOML file, atomically
// (temp file + rename), with a 0600 file in a 0700 directory. The encode goes
// through tommy (clown#238), editing the existing file's syntax tree in place
// so hand-written comments and layout survive a save. Entries are matched to
// existing [[profile]] tables by position: removing a middle profile shifts
// later values up, and a comment stays with its table position.
func Save(path string, profiles []Profile) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var f file
	handle, err := marshal.UnmarshalDocument(existing, &f)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	f.Profile = profiles
	body, err := marshal.MarshalDocument(handle, &f)
	if err != nil {
		return fmt.Errorf("encode profiles: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".profiles-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename over %s: %w", path, err)
	}
	return nil
}

// Upsert replaces the profile whose Name matches p, or appends p when absent.
func Upsert(profiles []Profile, p Profile) []Profile {
	for i := range profiles {
		if profiles[i].Name == p.Name {
			out := make([]Profile, len(profiles))
			copy(out, profiles)
			out[i] = p
			return out
		}
	}
	return append(append([]Profile(nil), profiles...), p)
}

// Remove filters out the profile named name, reporting whether it was present.
func Remove(profiles []Profile, name string) ([]Profile, bool) {
	out := make([]Profile, 0, len(profiles))
	found := false
	for _, p := range profiles {
		if p.Name == name {
			found = true
			continue
		}
		out = append(out, p)
	}
	return out, found
}
