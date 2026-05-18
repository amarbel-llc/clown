package ringmaster

import (
	"fmt"
	"os"
	"path/filepath"
)

// SocketPath returns the canonical control-socket location. The
// RINGMASTER_SOCKET env var overrides it (useful for tests and
// non-default deployments).
func SocketPath() (string, error) {
	if v := os.Getenv("RINGMASTER_SOCKET"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".local", "state", "circus", "control.sock"), nil
}

// LogPath returns ringmaster's log file location. Respects
// XDG_LOG_HOME if set (the eng convention; see ~/eng/home/xdg.nix),
// else falls back to $HOME/.local/log.
func LogPath() (string, error) {
	if v := os.Getenv("XDG_LOG_HOME"); v != "" {
		return filepath.Join(v, "ringmaster.log"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".local", "log", "ringmaster.log"), nil
}
