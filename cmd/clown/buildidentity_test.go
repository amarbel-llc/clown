package main

import (
	"strings"
	"testing"

	"github.com/amarbel-llc/clown/internal/buildcfg"
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
		mustContain(t, got, "https://github.com/amarbel-llc/clown/commit/"+fullSha)
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
