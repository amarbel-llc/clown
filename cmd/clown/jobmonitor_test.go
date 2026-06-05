package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJobMonitorPluginDirSynthesized(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
	dir, err := synthJobMonitorPluginDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir == "" {
		t.Fatal("expected a synthesized plugin dir when job-wakeup is enabled")
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	manifestPath := filepath.Join(dir, ".claude-plugin", "plugin.json")
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading synthesized manifest: %v", err)
	}
	var m struct {
		Name         string `json:"name"`
		Version      string `json:"version"`
		Experimental struct {
			Monitors []struct {
				Name        string `json:"name"`
				Command     string `json:"command"`
				Description string `json:"description"`
			} `json:"monitors"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v\n%s", err, b)
	}
	if len(m.Experimental.Monitors) != 1 {
		t.Fatalf("want exactly one monitor, got %d", len(m.Experimental.Monitors))
	}
	mon := m.Experimental.Monitors[0]
	if mon.Name != "clown-job-watch" {
		t.Fatalf("monitor name = %q, want clown-job-watch", mon.Name)
	}
	if mon.Description == "" {
		t.Fatal("monitor description must be non-empty")
	}
	if !strings.HasSuffix(mon.Command, "job-watch") {
		t.Fatalf("monitor command = %q, want it to end in job-watch", mon.Command)
	}
}

func TestJobMonitorCommandIsAbsoluteWhenResolvable(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
	// os.Executable() resolves to the test binary in `go test`, so the
	// command must be absolute in this environment.
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	cmd := jobWatchCommand()
	if !filepath.IsAbs(strings.Fields(cmd)[0]) {
		t.Fatalf("monitor command %q should start with an absolute path (os.Executable=%q)", cmd, exe)
	}
	if !strings.HasSuffix(cmd, " job-watch") {
		t.Fatalf("monitor command %q should end with the job-watch subcommand", cmd)
	}
}

func TestJobMonitorDisabledReturnsEmpty(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "1")
	dir, err := synthJobMonitorPluginDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != "" {
		_ = os.RemoveAll(dir)
		t.Fatalf("expected no plugin dir when disabled, got %q", dir)
	}
}
