// Package circusmodels exposes shared lookup helpers for the
// per-user circus model directory (~/.local/share/circus/models).
// Both cmd/circus and cmd/clown read this directory; consolidating
// the lookup keeps a single source of truth for the path layout.
package circusmodels

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dir returns the absolute path to the models directory. Returns
// an empty string if the user's home directory cannot be resolved.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "circus", "models")
}

// List returns the names (without the .gguf suffix) of every GGUF
// model file directly under dir, sorted alphabetically. A missing
// directory is reported as an empty list with a nil error.
func List(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".gguf") {
			names = append(names, strings.TrimSuffix(e.Name(), ".gguf"))
		}
	}
	sort.Strings(names)
	return names, nil
}
