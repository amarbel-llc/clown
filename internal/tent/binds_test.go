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

func TestDefaultReadOnlyBinds_KeepsSymlinks(t *testing.T) {
	// ~/.nix-profile on a real host is a symlink into /nix/var/.../profile.
	// DefaultReadOnlyBinds must keep it (the in-tent agent expects the
	// symlink itself, so PATH entries pointing at it resolve correctly).
	// Verify Lstat-not-Stat semantics by planting a symlink to a
	// nonexistent target — Stat would return ENOENT and drop it; Lstat
	// reports the symlink itself.
	home := t.TempDir()
	link := filepath.Join(home, ".nix-profile")
	if err := os.Symlink("/does/not/exist/target", link); err != nil {
		t.Fatal(err)
	}

	got := DefaultReadOnlyBinds(home)

	if !slices.Contains(got, link) {
		t.Errorf("dangling symlink ~/.nix-profile was dropped; got %v", got)
	}
}
