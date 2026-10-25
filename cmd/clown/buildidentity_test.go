package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/buildcfg"
)

func TestBuildIdentifier(t *testing.T) {
	cases := []struct {
		name     string
		version  string
		shortSha string
		want     string
	}{
		{"full", "0.3.10", "a1b2c3d", "0.3.10+a1b2c3d"},
		{"version only", "0.3.10", "", "0.3.10"},
		{"sha only", "", "a1b2c3d", "a1b2c3d"},
		{"neither", "", "", "an unversioned dev build"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildIdentifier(tc.version, tc.shortSha); got != tc.want {
				t.Fatalf("buildIdentifier(%q, %q) = %q, want %q",
					tc.version, tc.shortSha, got, tc.want)
			}
		})
	}
}

// setBuildcfg overwrites the build-time identity vars for the duration of a
// test and restores them afterward. The vars are package globals injected via
// ldflags, so tests must not run in parallel while mutating them.
func setBuildcfg(t *testing.T, version, shortSha, commit string) {
	t.Helper()
	origVersion, origShortSha, origCommit := buildcfg.Version, buildcfg.ShortSha, buildcfg.Commit
	t.Cleanup(func() {
		buildcfg.Version, buildcfg.ShortSha, buildcfg.Commit = origVersion, origShortSha, origCommit
	})
	buildcfg.Version, buildcfg.ShortSha, buildcfg.Commit = version, shortSha, commit
}

func TestBuildIdentityFragment(t *testing.T) {
	const fullSha = "a1b2c3d4e5f60718293a4b5c6d7e8f9001122334"

	t.Run("full build stamps identifier and commit link", func(t *testing.T) {
		setBuildcfg(t, "0.3.10", "a1b2c3d", fullSha)
		got := buildIdentityFragment()
		mustContain(t, got, "clown 0.3.10+a1b2c3d")
		mustContain(t, got, clownRepoURL+"/commit/"+fullSha)
	})

	t.Run("version only degrades without a link", func(t *testing.T) {
		setBuildcfg(t, "0.3.10", "", "")
		got := buildIdentityFragment()
		mustContain(t, got, "clown 0.3.10")
		mustNotContain(t, got, "/commit/")
	})

	t.Run("sha only stamps the sha and links", func(t *testing.T) {
		setBuildcfg(t, "", "a1b2c3d", fullSha)
		got := buildIdentityFragment()
		mustContain(t, got, "clown a1b2c3d")
		mustContain(t, got, "/commit/"+fullSha)
	})

	t.Run("dev build claims nothing and links nothing", func(t *testing.T) {
		setBuildcfg(t, "", "", "")
		got := buildIdentityFragment()
		mustContain(t, got, "unversioned local dev build")
		mustNotContain(t, got, "/commit/")
	})
}

// setPinnedComponents overwrites the build-time-pinned component vars for the
// duration of a test.
func setPinnedComponents(t *testing.T, troupeVersion, troupeRev, ringmasterVersion, ringmasterRev string) {
	t.Helper()
	ov := []string{
		buildcfg.TroupeVersion, buildcfg.TroupeRev, buildcfg.RingmasterVersion, buildcfg.RingmasterRev,
		buildcfg.ClaudeCodeVersion, buildcfg.ClaudeCodeRev, buildcfg.CodexVersion, buildcfg.CodexRev,
	}
	t.Cleanup(func() {
		buildcfg.TroupeVersion, buildcfg.TroupeRev, buildcfg.RingmasterVersion, buildcfg.RingmasterRev = ov[0], ov[1], ov[2], ov[3]
		buildcfg.ClaudeCodeVersion, buildcfg.ClaudeCodeRev, buildcfg.CodexVersion, buildcfg.CodexRev = ov[4], ov[5], ov[6], ov[7]
	})
	buildcfg.TroupeVersion, buildcfg.TroupeRev = troupeVersion, troupeRev
	buildcfg.RingmasterVersion, buildcfg.RingmasterRev = ringmasterVersion, ringmasterRev
	buildcfg.ClaudeCodeVersion, buildcfg.ClaudeCodeRev, buildcfg.CodexVersion, buildcfg.CodexRev = "", "", "", ""
}

// clown#227: the fragment carries the `clown version` table's pinned
// components (not clown's own row, which the first line already states),
// labelled as build-time pins rather than the worktree's source HEAD. The
// build-identifier and sign-off sentences stay exactly as they were.
func TestBuildIdentityFragmentListsPinnedComponents(t *testing.T) {
	const fullSha = "a1b2c3d4e5f60718293a4b5c6d7e8f9001122334"
	setBuildcfg(t, "0.3.10", "a1b2c3d", fullSha)
	setPinnedComponents(t, "0.2.7", "deadbee", "0.5.1", "cafef00")
	t.Setenv("CLOWN_PLUGIN_META", "")
	got := buildIdentityFragment()

	signOff := "You are running clown 0.3.10+a1b2c3d.\nWhen you sign off as Clown on git commits, pull " +
		"requests, or comments, include this build identifier (0.3.10+a1b2c3d) and link " +
		"to the exact commit it was built from: " + clownRepoURL + "/commit/" + fullSha + "."
	if !strings.HasPrefix(got, signOff) {
		t.Fatalf("the identity and sign-off sentences changed:\n%s", got)
	}
	mustContain(t, got, "troupe 0.2.7 (deadbee)")
	mustContain(t, got, "ringmaster 0.5.1 (cafef00)")
	mustContain(t, got, "`clown version`")
	mustContain(t, got, "source HEAD")
	// Unpinned components (no version) are left out.
	mustNotContain(t, got, "claude-code")
	mustNotContain(t, got, "codex")
}

// clown#227: plugin rows from CLOWN_PLUGIN_META/version-info (preformatted
// "name version rev" lines) are listed too, whitespace collapsed; with
// nothing pinned at all there is no section.
func TestBuildIdentityFragmentListsPluginVersions(t *testing.T) {
	setBuildcfg(t, "0.3.10", "a1b2c3d", "")
	setPinnedComponents(t, "", "", "", "")
	t.Setenv("CLOWN_PLUGIN_META", "")
	if got := buildIdentityFragment(); strings.Contains(got, "`clown version`") {
		t.Fatalf("nothing pinned, yet a components section:\n%s", got)
	}

	meta := t.TempDir()
	line := "moxy/moxy            1.2.3        abc1234\n"
	if err := os.WriteFile(filepath.Join(meta, "version-info"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOWN_PLUGIN_META", meta)
	got := buildIdentityFragment()
	mustContain(t, got, "- moxy/moxy 1.2.3 abc1234")
	mustContain(t, got, "`clown version`")
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("fragment missing %q\n--- fragment ---\n%s", needle, haystack)
	}
}

func mustNotContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Fatalf("fragment unexpectedly contains %q\n--- fragment ---\n%s", needle, haystack)
	}
}
