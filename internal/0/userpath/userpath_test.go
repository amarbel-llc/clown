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

// The XDG base-directory spec: a relative value is invalid and ignored, so
// the home-relative default applies (clown#302 review: a relative
// XDG_STATE_HOME would put the permission state under the cwd).
func TestRelativeXDGValuesAreIgnored(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_STATE_HOME", "rel/state")
	got, err := StatePath("hook-toolcost", "permstat")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/test", ".local", "state", "clown", "hook-toolcost", "permstat"); got != want {
		t.Errorf("StatePath with a relative XDG_STATE_HOME = %q, want %q", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", "rel/config")
	got, err = ConfigPath("profiles.toml")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/test", ".config", "clown", "profiles.toml"); got != want {
		t.Errorf("ConfigPath with a relative XDG_CONFIG_HOME = %q, want %q", got, want)
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
