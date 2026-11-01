package juggler

import (
	"fmt"
	"os"
	"path/filepath"
)

// SocketPath returns the canonical control-socket location. The
// JUGGLER_SOCKET env var overrides it (useful for tests and
// non-default deployments).
func SocketPath() (string, error) {
	if v := os.Getenv("JUGGLER_SOCKET"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".local", "state", "juggler", "control.sock"), nil
}

// LogPath returns juggler's log file location. Respects
// XDG_LOG_HOME if set (the eng convention; see ~/eng/home/xdg.nix),
// else falls back to $HOME/.local/log.
func LogPath() (string, error) {
	if v := os.Getenv("XDG_LOG_HOME"); v != "" {
		return filepath.Join(v, "juggler.log"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".local", "log", "juggler.log"), nil
}

// RemoteModelsPath returns the remote-model registry file location:
// ~/.local/share/juggler/models.toml (sibling to the GGUF models/ dir
// jugglermodels.Dir() resolves). JUGGLER_MODELS_PATH overrides it (tests).
func RemoteModelsPath() (string, error) {
	if v := os.Getenv("JUGGLER_MODELS_PATH"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".local", "share", "juggler", "models.toml"), nil
}
