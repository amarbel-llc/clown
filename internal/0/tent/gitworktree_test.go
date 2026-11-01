package tent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestParseGitdirLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "gitdir: /a/b/c", "/a/b/c"},
		{"trailing newline", "gitdir: /a/b/c\n", "/a/b/c"},
		{"trailing whitespace", "gitdir: /a/b/c   \n", "/a/b/c"},
		{"no prefix", "/a/b/c", ""},
		{"comment-only", "# not a gitdir line\n", ""},
		{"second line wins is wrong — only first counts", "noise\ngitdir: /a/b/c\n", "/a/b/c"},
		{"relative path", "gitdir: ../parent/.git/worktrees/x", "../parent/.git/worktrees/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseGitdirLine(tc.in)
			if got != tc.want {
				t.Errorf("parseGitdirLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// makeWorktreeFixture builds a synthetic git-worktree layout under root:
//
//	<root>/parent/.git/                          (common dir, real directory)
//	<root>/parent/.git/worktrees/wt/             (worktree-specific dir)
//	<root>/parent/.git/worktrees/wt/commondir    (contents: "../..")
//	<root>/parent/.worktrees/wt/                 (the working tree itself)
//	<root>/parent/.worktrees/wt/.git             (file with gitdir: <abs path>)
//
// Returns the worktree workdir (the `.worktrees/wt` path), the gitdir,
// and the common dir for the test to assert against.
func makeWorktreeFixture(t *testing.T) (workdir, gitdir, commonDir string) {
	t.Helper()
	root := t.TempDir()
	commonDir = filepath.Join(root, "parent", ".git")
	gitdir = filepath.Join(commonDir, "worktrees", "wt")
	workdir = filepath.Join(root, "parent", ".worktrees", "wt")

	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return workdir, gitdir, commonDir
}

func TestDiscoverGitWorktreeBinds_NoGitMarker(t *testing.T) {
	dir := t.TempDir()
	got, err := DiscoverGitWorktreeBinds(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for non-git workdir, got %q", got)
	}
}

func TestDiscoverGitWorktreeBinds_RegularRepo(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverGitWorktreeBinds(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for regular (in-tree) repo, got %q", got)
	}
}

func TestDiscoverGitWorktreeBinds_Worktree(t *testing.T) {
	workdir, _, commonDir := makeWorktreeFixture(t)

	// EvalSymlinks normalizes /private/var on darwin and resolves any
	// other tempdir symlinks the OS inserted; do the same on the
	// expectation so we compare apples to apples regardless of host.
	wantCommon, err := filepath.EvalSymlinks(commonDir)
	if err != nil {
		t.Fatal(err)
	}

	got, err := DiscoverGitWorktreeBinds(workdir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly one bind (the common dir), got %q", got)
	}
	gotCommon, err := filepath.EvalSymlinks(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if gotCommon != wantCommon {
		t.Errorf("bind = %q, want %q", gotCommon, wantCommon)
	}
}

func TestDiscoverGitWorktreeBinds_NoCommondir(t *testing.T) {
	workdir, gitdir, _ := makeWorktreeFixture(t)
	// Remove commondir to simulate a malformed/legacy worktree.
	if err := os.Remove(filepath.Join(gitdir, "commondir")); err != nil {
		t.Fatal(err)
	}

	wantGitdir, err := filepath.EvalSymlinks(gitdir)
	if err != nil {
		t.Fatal(err)
	}

	got, err := DiscoverGitWorktreeBinds(workdir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one bind (gitdir only), got %q", got)
	}
	gotResolved, err := filepath.EvalSymlinks(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if gotResolved != wantGitdir {
		t.Errorf("bind = %q, want %q", gotResolved, wantGitdir)
	}
}

func TestDiscoverGitWorktreeBinds_AbsoluteCommondir(t *testing.T) {
	workdir, gitdir, _ := makeWorktreeFixture(t)
	// Replace commondir with an absolute path to a different real dir.
	absCommon := t.TempDir()
	if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte(absCommon+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wantGitdir, _ := filepath.EvalSymlinks(gitdir)
	wantCommon, _ := filepath.EvalSymlinks(absCommon)

	got, err := DiscoverGitWorktreeBinds(workdir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// gitdir is NOT inside absCommon here, so we expect both binds.
	if len(got) != 2 {
		t.Fatalf("expected two binds (gitdir + disjoint commondir), got %q", got)
	}
	for i, p := range got {
		resolved, _ := filepath.EvalSymlinks(p)
		got[i] = resolved
	}
	if !slices.Contains(got, wantGitdir) || !slices.Contains(got, wantCommon) {
		t.Errorf("binds = %q, want both %q and %q", got, wantGitdir, wantCommon)
	}
}

func TestDiscoverGitWorktreeBinds_BindInsideWorkdirDropped(t *testing.T) {
	// Synthesize a workdir where the resolved gitdir is *inside* the
	// workdir tree. That's not a layout git actually produces, but
	// the filter logic must handle it gracefully — already covered by
	// the workdir bind, so it must not be emitted as a duplicate.
	workdir := t.TempDir()
	gitdir := filepath.Join(workdir, "embedded-gitdir")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := DiscoverGitWorktreeBinds(workdir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil (gitdir is inside workdir), got %q", got)
	}
}
