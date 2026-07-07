package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// userConfigPath resolves the per-user config file `name` (e.g.
// "profiles.toml", "opencode.toml"). Canonical location is
// $XDG_CONFIG_HOME/clown/<name> (defaulting to ~/.config/clown/<name>); when
// the canonical file is absent but the legacy ~/.config/juggler/<name> exists,
// the legacy path is returned with legacy=true so the caller can warn once.
// Writers must always use userConfigWritePath. Legacy fallback removal
// criterion: one release with the warning, no reports (design doc, 2026-07-06).
func userConfigPath(name string) (path string, legacy bool, err error) {
	canonical, err := userConfigWritePath(name)
	if err != nil {
		return "", false, err
	}
	if _, statErr := os.Stat(canonical); statErr == nil {
		return canonical, false, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return canonical, false, nil
	}
	legacyPath := filepath.Join(home, ".config", "juggler", name)
	if _, statErr := os.Stat(legacyPath); statErr == nil {
		return legacyPath, true, nil
	}
	return canonical, false, nil
}

// userConfigWritePath is the canonical (always-write) location for name:
// $XDG_CONFIG_HOME/clown/<name>, ~/.config/clown/<name> when XDG is unset.
func userConfigWritePath(name string) (string, error) {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "clown", name), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".config", "clown", name), nil
}

// legacyConfigWarned dedupes warnLegacyConfig per path — several read sites
// (profiles, opencode, crush) may resolve to legacy files in one launch.
var legacyConfigWarned = map[string]bool{}

// warnLegacyConfig emits the one-line legacy-path warning, once per file.
func warnLegacyConfig(path string) {
	if legacyConfigWarned[path] {
		return
	}
	legacyConfigWarned[path] = true
	fmt.Fprintf(os.Stderr, "clown: reading legacy config %s; move it to ~/.config/clown/ (the TUI writes there)\n", path)
}
