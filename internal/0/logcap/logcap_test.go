package logcap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func size(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func TestRotateIfLargeKeepsOneGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	RotateIfLarge(path, 100) // at the cap: not over it
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Fatal("rotated at exactly the cap")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("b", 101)), 0o600); err != nil {
		t.Fatal(err)
	}
	RotateIfLarge(path, 100)
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the over-cap log was not moved")
	}
	if size(t, path+".1") != 101 {
		t.Fatal("generation .1 missing")
	}
	// A second rotation replaces .1, never a .2.
	if err := os.WriteFile(path, []byte(strings.Repeat("c", 150)), 0o600); err != nil {
		t.Fatal(err)
	}
	RotateIfLarge(path, 100)
	if size(t, path+".1") != 150 {
		t.Fatal(".1 not replaced by the newer generation")
	}
	if _, err := os.Stat(path + ".2"); err == nil {
		t.Fatal("a second generation was kept")
	}
}

func TestRotateIfLargeIgnoresMissingAndNonRegular(t *testing.T) {
	dir := t.TempDir()
	RotateIfLarge(filepath.Join(dir, "absent.log"), 1)
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(strings.Repeat("z", 50)), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	RotateIfLarge(link, 1)
	if _, err := os.Lstat(link); err != nil {
		t.Fatal("a symlink was rotated")
	}
}

// A long-lived File never grows past about twice the cap however much is
// written, and loses no write.
func TestFileStaysBoundedUnderALongRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.log")
	l, err := Open(path, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	line := []byte(strings.Repeat("x", 99) + "\n")
	for i := 0; i < 1000; i++ {
		if n, err := l.Write(line); err != nil || n != len(line) {
			t.Fatalf("write %d: %d, %v", i, n, err)
		}
		if got := size(t, path); got > 1000+int64(len(line)) {
			t.Fatalf("log grew to %d after %d writes", got, i+1)
		}
	}
	if got := size(t, path+".1"); got > 1000+int64(len(line)) {
		t.Fatalf("rotated generation is %d bytes", got)
	}
}

func TestOpenRotatesAnOverCapLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("o", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if size(t, path) != 0 || size(t, path+".1") != 500 {
		t.Fatalf("open did not rotate: %d / %d", size(t, path), size(t, path+".1"))
	}
}
