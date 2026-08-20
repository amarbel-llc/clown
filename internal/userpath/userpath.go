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
// XDG_CONFIG_HOME is unset or empty. Config is user-edited (profiles,
// provider settings); throwaway machine state belongs in StatePath.
func ConfigPath(parts ...string) (string, error) {
	return resolve("XDG_CONFIG_HOME", []string{".config"}, parts)
}

// StatePath resolves a per-user STATE path:
// $XDG_STATE_HOME/clown/<parts...>, or ~/.local/state/clown/<parts...> when
// XDG_STATE_HOME is unset or empty. State is machine-managed and never
// user-edited (name-allocator lock, session-name journal, hook cursors).
func StatePath(parts ...string) (string, error) {
	return resolve("XDG_STATE_HOME", []string{".local", "state"}, parts)
}

// resolve implements the shared ladder: the XDG env var when set, else the
// home-relative default, always with the "clown" app directory appended.
func resolve(envVar string, homeDefault, parts []string) (string, error) {
	base := os.Getenv(envVar)
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("home dir: %w", err)
		}
		base = filepath.Join(append([]string{home}, homeDefault...)...)
	}
	return filepath.Join(append([]string{base, "clown"}, parts...)...), nil
}
