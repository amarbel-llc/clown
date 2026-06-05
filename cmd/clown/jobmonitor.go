package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// jobMonitorPlugin is the synthesized built-in plugin manifest that registers
// the clown job-watch monitor as a Claude Code experimental monitor. Each
// stdout line the monitor emits becomes an agent notification (RFC-0009 §9).
type jobMonitorPlugin struct {
	Name         string                 `json:"name"`
	Version      string                 `json:"version"`
	Experimental jobMonitorExperimental `json:"experimental"`
}

type jobMonitorExperimental struct {
	Monitors []jobMonitorEntry `json:"monitors"`
}

type jobMonitorEntry struct {
	Name        string `json:"name"`
	Command     string `json:"command"`
	Description string `json:"description"`
}

// jobWatchCommand returns the monitor command string Claude Code spawns.
// Claude Code spawns monitors with the session PATH, on which `clown` may not
// appear; resolving os.Executable() yields an absolute path so the monitor
// runs regardless of PATH. When os.Executable() fails we fall back to the bare
// `clown job-watch`, which still works wherever clown is on PATH.
func jobWatchCommand() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		return exe + " job-watch"
	}
	return "clown job-watch"
}

// synthJobMonitorPluginDir writes a temporary built-in plugin directory whose
// .claude-plugin/plugin.json declares the clown job-watch monitor, and returns
// its path. The caller appends the path to the --plugin-dir set passed to
// Claude and removes the directory on shutdown. When CLOWN_DISABLE_JOB_WAKEUP=1
// it returns ("", nil) so the monitor is not registered (RFC-0009 §8).
func synthJobMonitorPluginDir() (string, error) {
	if jobWakeupDisabled() {
		return "", nil
	}
	dir, err := os.MkdirTemp("", "clown-jobwake-plugin-")
	if err != nil {
		return "", err
	}
	manifestDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	manifest := jobMonitorPlugin{
		Name:    "clown-builtin-jobwake",
		Version: "1",
		Experimental: jobMonitorExperimental{
			Monitors: []jobMonitorEntry{{
				Name:        "clown-job-watch",
				Command:     jobWatchCommand(),
				Description: "clown job-wakeup channel: wakes this session when a background job finishes",
			}},
		},
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), b, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}
