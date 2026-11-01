package tent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDefaultReadOnlyBindCandidates_Shape(t *testing.T) {
	got := DefaultReadOnlyBindCandidates("/home/u")
	want := []string{
		"/nix/var",
		"/etc/nix",
		"/home/u/.nix-profile",
		"/home/u/.local/state/nix/profiles/profile",
		"/home/u/.gitconfig",
		"/home/u/.config/git",
		"/home/u/.config/nix",
		"/home/u/.config/ssh",
	}
	if !slices.Equal(got, want) {
		t.Errorf("DefaultReadOnlyBindCandidates =\n  got:  %v\n  want: %v", got, want)
	}
}

func TestDefaultReadOnlyBinds_FiltersToExisting(t *testing.T) {
	// Use t.TempDir() as a fake $HOME. None of the candidates exist
	// initially; populate a subset and confirm only those come back.
	home := t.TempDir()

	// Plant ~/.gitconfig (file) and ~/.config/nix (dir). Leave the
	// rest absent.
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config", "nix"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := DefaultReadOnlyBinds(home)

	// /nix/var and /etc/nix presence is host-dependent — drop them
	// from the comparison and only assert the $HOME-relative subset.
	var homeRel []string
	for _, p := range got {
		if filepath.HasPrefix(p, home) {
			homeRel = append(homeRel, p)
		}
	}
	want := []string{
		filepath.Join(home, ".gitconfig"),
		filepath.Join(home, ".config/nix"),
	}
	if !slices.Equal(homeRel, want) {
		t.Errorf("home-relative subset =\n  got:  %v\n  want: %v", homeRel, want)
	}
}

func TestDefaultReadOnlyBinds_DropsDanglingSymlinks(t *testing.T) {
	// On nix-darwin installs where the user profile lives at
	// /etc/profiles/per-user/<u>/ rather than ~/.local/state/nix/...,
	// the ~/.nix-profile symlink may exist but point at a nonexistent
	// target. Podman would fail at run time with `statfs <target>:
	// no such file or directory` if we bind-mounted a dangling
	// symlink, so DefaultReadOnlyBinds drops it.
	home := t.TempDir()
	link := filepath.Join(home, ".nix-profile")
	if err := os.Symlink("/does/not/exist/target", link); err != nil {
		t.Fatal(err)
	}

	got := DefaultReadOnlyBinds(home)

	if slices.Contains(got, link) {
		t.Errorf("dangling symlink ~/.nix-profile should be dropped; got %v", got)
	}
}

func TestDefaultReadOnlyBinds_KeepsLiveSymlinks(t *testing.T) {
	// A symlink whose target exists is kept — the in-tent agent
	// expects to see ~/.nix-profile *as* a symlink so PATH entries
	// pointing at it resolve correctly on linux (where the target
	// lives under the /nix/var bind).
	home := t.TempDir()
	target := filepath.Join(home, "real-profile")
	if err := os.MkdirAll(filepath.Join(target, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".nix-profile")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	got := DefaultReadOnlyBinds(home)

	if !slices.Contains(got, link) {
		t.Errorf("live symlink ~/.nix-profile was dropped; got %v", got)
	}
}
