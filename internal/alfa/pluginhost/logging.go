package pluginhost

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// OpenLog creates the per-invocation log file under $XDG_LOG_HOME/clown/
// and returns a slog text logger bound to it, the underlying file handle,
// and the absolute file path.
//
// The caller owns the returned *os.File and should Close it when done.
//
// If $XDG_LOG_HOME is unset or empty, the XDG Base Directory default of
// $HOME/.local/log is used.
func OpenLog() (*slog.Logger, *os.File, string, error) {
	dir, err := LogDir()
	if err != nil {
		return nil, nil, "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, "", fmt.Errorf("create log dir %s: %w", dir, err)
	}

	name := fmt.Sprintf(
		"clown-plugin-host-%s-%d.log",
		time.Now().UTC().Format("20060102T150405Z"),
		os.Getpid(),
	)
	path := filepath.Join(dir, name)

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, "", fmt.Errorf("open log file %s: %w", path, err)
	}

	logger := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return logger, f, path, nil
}

// LogDir returns the directory in which clown-plugin-host should write its
// log files, per the XDG_LOG_HOME specification. Applications SHOULD organize
// log files into a subdirectory named after the application; the returned
// path is therefore $XDG_LOG_HOME/clown (or $HOME/.local/log/clown if the
// variable is unset or empty).
func LogDir() (string, error) {
	if v := os.Getenv("XDG_LOG_HOME"); v != "" {
		return filepath.Join(v, "clown"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	return filepath.Join(home, ".local", "log", "clown"), nil
}
