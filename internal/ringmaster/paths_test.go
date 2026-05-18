package ringmaster

import (
	"path/filepath"
	"testing"
)

func TestSocketPath_Default(t *testing.T) {
	t.Setenv("RINGMASTER_SOCKET", "")
	t.Setenv("HOME", "/tmp/ringmaster-test")
	got, err := SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp/ringmaster-test", ".local", "state", "circus", "control.sock")
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestSocketPath_EnvOverride(t *testing.T) {
	t.Setenv("RINGMASTER_SOCKET", "/tmp/x.sock")
	got, err := SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/x.sock" {
		t.Errorf("got %q", got)
	}
}

func TestLogPath_XDGLogHome(t *testing.T) {
	t.Setenv("XDG_LOG_HOME", "/tmp/log")
	got, err := LogPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/log/ringmaster.log" {
		t.Errorf("got %q", got)
	}
	// fallback
	t.Setenv("XDG_LOG_HOME", "")
	t.Setenv("HOME", "/tmp/h")
	got, err = LogPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/h/.local/log/ringmaster.log" {
		t.Errorf("got %q", got)
	}
}
