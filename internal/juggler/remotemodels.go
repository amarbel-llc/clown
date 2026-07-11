package juggler

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// RemoteModel is a user-registered, process-less gateway endpoint — the
// "remote" half of juggler's model registry (the "local" half is GGUF
// files, auto-discovered by internal/jugglermodels; see Task 4's
// ListModels union). Token may be a literal secret or a "${VAR}"
// reference, resolved with os.ExpandEnv at ResolveModel time (Task 4) —
// not resolved here, so the on-disk file never needs the ambient env.
type RemoteModel struct {
	Name  string `toml:"name"`
	Style string `toml:"style"` // "anthropic" | "openai-compat"
	URL   string `toml:"url"`
	Token string `toml:"token"`
}

type remoteModelsFile struct {
	Model []RemoteModel `toml:"model"`
}

// LoadRemoteModels reads path, returning (nil, nil) if it doesn't exist —
// an empty registry is not an error (mirrors internal/profile's Load
// posture for an absent user profiles.toml).
func LoadRemoteModels(path string) ([]RemoteModel, error) {
	var f remoteModelsFile
	if _, err := toml.DecodeFile(path, &f); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("load remote models %s: %w", path, err)
	}
	return f.Model, nil
}

// SaveRemoteModels writes models to path atomically (temp file + rename),
// 0600 file in a 0700 directory. Mirrors internal/profile/store.go's
// Save — this file is juggler-managed, hand-edited comments are not
// preserved across a save.
func SaveRemoteModels(path string, models []RemoteModel) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(remoteModelsFile{Model: models}); err != nil {
		return fmt.Errorf("encode remote models: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".models-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
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

// UpsertRemoteModel replaces the entry whose Name matches m, or appends m.
func UpsertRemoteModel(models []RemoteModel, m RemoteModel) []RemoteModel {
	for i := range models {
		if models[i].Name == m.Name {
			out := make([]RemoteModel, len(models))
			copy(out, models)
			out[i] = m
			return out
		}
	}
	return append(append([]RemoteModel(nil), models...), m)
}

// RemoveRemoteModel filters out the entry named name, reporting presence.
func RemoveRemoteModel(models []RemoteModel, name string) ([]RemoteModel, bool) {
	out := make([]RemoteModel, 0, len(models))
	found := false
	for _, m := range models {
		if m.Name == name {
			found = true
			continue
		}
		out = append(out, m)
	}
	return out, found
}
