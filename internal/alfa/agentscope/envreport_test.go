package agentscope

import (
	"slices"
	"strings"
	"testing"
)

func TestEnvironmentReportIsNamesOnlyAndSplitsBySource(t *testing.T) {
	l := testLaunch()
	l.FrontendEnv = []string{
		"PATH=/nix/store/aaa-go/bin:/usr/bin::/nix/store/bbb-just/bin",
		"LANG=C.UTF-8",
		"stdenv=/nix/store/secretish",
		"NIX_CONFIG=access-tokens = hunter2",
		"BATS_LIB_PATH=/x",
	}
	r := l.EnvironmentReport()

	if !slices.Contains(r.Passed, "PATH") || !slices.Contains(r.Passed, "LANG") {
		t.Errorf("Passed = %v, want PATH and LANG", r.Passed)
	}
	for _, k := range []string{"stdenv", "NIX_CONFIG", "BATS_LIB_PATH"} {
		if !slices.Contains(r.Inherited, k) {
			t.Errorf("Inherited = %v, want %s", r.Inherited, k)
		}
	}
	for _, k := range []string{"HOME", "CLOWN_UNIT", "HTTPS_PROXY"} {
		if !slices.Contains(r.Stated, k) {
			t.Errorf("Stated = %v, want %s", r.Stated, k)
		}
	}
	if r.PathEntries != 3 || r.PathStoreEntries != 2 {
		t.Errorf("PATH = %d entries, %d in store; want 3, 2", r.PathEntries, r.PathStoreEntries)
	}
	if s := r.String(); strings.Contains(s, "hunter2") || strings.Contains(s, "secretish") {
		t.Errorf("report leaks a value: %s", s)
	}
}
