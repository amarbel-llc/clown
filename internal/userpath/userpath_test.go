package userpath

import (
	"path/filepath"
	"testing"
)

func TestStatePathXDG(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	got, err := StatePath("hook-tee", "cursor")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/xdg/state", "clown", "hook-tee", "cursor"); got != want {
		t.Fatalf("StatePath = %q, want %q", got, want)
	}
}

func TestStatePathHomeFallback(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/test")
	got, err := StatePath("names.lock")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/test", ".local", "state", "clown", "names.lock"); got != want {
		t.Fatalf("StatePath = %q, want %q", got, want)
	}
}

func TestConfigPathXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	got, err := ConfigPath("profiles.toml")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/xdg/config", "clown", "profiles.toml"); got != want {
		t.Fatalf("ConfigPath = %q, want %q", got, want)
	}
}

func TestConfigPathHomeFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/test")
	got, err := ConfigPath("profiles.toml")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/test", ".config", "clown", "profiles.toml"); got != want {
		t.Fatalf("ConfigPath = %q, want %q", got, want)
	}
}
