package pluginhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogDirUsesXDGLogHomeWhenSet(t *testing.T) {
	t.Setenv("XDG_LOG_HOME", "/var/tmp/custom")
	got, err := LogDir()
	if err != nil {
		t.Fatalf("LogDir: %v", err)
	}
	want := filepath.Join("/var/tmp/custom", "clown")
	if got != want {
		t.Errorf("LogDir = %q, want %q", got, want)
	}
}

func TestLogDirFallsBackToHomeLocalLog(t *testing.T) {
	t.Setenv("XDG_LOG_HOME", "")
	t.Setenv("HOME", "/home/fakeuser")
	got, err := LogDir()
	if err != nil {
		t.Fatalf("LogDir: %v", err)
	}
	want := filepath.Join("/home/fakeuser", ".local", "log", "clown")
	if got != want {
		t.Errorf("LogDir = %q, want %q", got, want)
	}
}

func TestOpenLogCreatesDirAndFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_LOG_HOME", tmp)

	logger, f, path, err := OpenLog()
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	t.Cleanup(func() { f.Close() })

	wantDir := filepath.Join(tmp, "clown")
	if !strings.HasPrefix(path, wantDir+string(os.PathSeparator)) {
		t.Errorf("log path %q not under %q", path, wantDir)
	}

	base := filepath.Base(path)
	if !strings.HasPrefix(base, "clown-plugin-host-") || !strings.HasSuffix(base, ".log") {
		t.Errorf("unexpected log filename %q", base)
	}

	logger.Info("smoke", "hello", "world")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "msg=smoke") || !strings.Contains(string(data), "hello=world") {
		t.Errorf("log file does not contain expected record:\n%s", data)
	}
}
