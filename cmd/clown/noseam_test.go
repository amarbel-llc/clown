package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The two-real-halves seam (the real cgroup-file and unit-record writers) is its
// own binary, .#bridge-seam (cmd/clown-bridge-seam), and must not come back as a
// verb of the production binary: a hidden test seam inside every unit is surface
// nobody needs (clown bridge review r6, delta read). Red if any non-test source
// of this package names it.
func TestTheSeamIsNotAVerbOfTheProductionBinary(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	sawMain := false
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		if f == "main.go" {
			sawMain = true
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		if s := string(b); strings.Contains(s, "bridge-seam") || strings.Contains(s, "BridgeSeam") {
			t.Errorf("%s names the bridge seam: it belongs in cmd/clown-bridge-seam, not in the production binary", f)
		}
	}
	if !sawMain || checked < 10 {
		t.Fatalf("this test looked at %d sources and main.go=%v: it is not seeing the package, so it proves nothing", checked, sawMain)
	}
}
