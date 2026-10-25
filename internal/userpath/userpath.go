// Package userpath is the single definition of where clown keeps per-user
// files: the XDG base-directory ladders for config and state. Every caller
// that needs "$XDG_CONFIG_HOME/clown/..." or "$XDG_STATE_HOME/clown/..."
// resolves it here, so the fallback rule cannot drift between hand-rolled
// copies (clown#204 — cmd/clown, internal/clownname, and internal/sessions
// each used to spell this ladder independently).
package userpath

import (
	"fmt"
	"os"
	"path/filepath"
)

// ConfigPath resolves a per-user CONFIG path:
// $XDG_CONFIG_HOME/clown/<parts...>, or ~/.config/clown/<parts...> when
// XDG_CONFIG_HOME is unset, empty or relative. Config is user-edited (profiles,
// provider settings); throwaway machine state belongs in StatePath.
func ConfigPath(parts ...string) (string, error) {
	return resolve("XDG_CONFIG_HOME", []string{".config"}, parts)
}

// ConfigFile resolves the per-user config file `name` for READING: the
// canonical ConfigPath(name) when it exists, else the legacy
// ~/.config/juggler/<name> when that exists (legacy=true so the caller can
// warn once), else the canonical path. Writers must use ConfigPath. Legacy
// fallback removal criterion: one release with the warning, no reports
// (design doc, 2026-07-06).
func ConfigFile(name string) (path string, legacy bool, err error) {
	canonical, err := ConfigPath(name)
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

// StatePath resolves a per-user STATE path:
// $XDG_STATE_HOME/clown/<parts...>, or ~/.local/state/clown/<parts...> when
// XDG_STATE_HOME is unset, empty or relative. State is machine-managed and never
// user-edited (name-allocator lock, session-name journal, hook cursors).
func StatePath(parts ...string) (string, error) {
	return resolve("XDG_STATE_HOME", []string{".local", "state"}, parts)
}

// resolve implements the shared ladder: the XDG env var when set to an
// absolute path, else the home-relative default (the XDG base-directory spec
// says a relative value is invalid and to be ignored), always with the
// "clown" app directory appended.
func resolve(envVar string, homeDefault, parts []string) (string, error) {
	base := os.Getenv(envVar)
	if !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("home dir: %w", err)
		}
		base = filepath.Join(append([]string{home}, homeDefault...)...)
	}
	return filepath.Join(append([]string{base, "clown"}, parts...)...), nil
}
