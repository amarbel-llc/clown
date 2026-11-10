package catalogsnap

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

// writeTemp leaves a temp file as a Write interrupted before its rename
// would, named for writer pid, last modified at mtime.
func writeTemp(t *testing.T, dir string, pid int, mtime time.Time) string {
	t.Helper()
	name := tempPrefix + strconv.Itoa(pid) + "-123"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return name
}

// A Write interrupted before its rename leaves a temp file (#355): Prune
// removes one older than the retention whose writer is not running, and
// keeps a recent one or one whose writer still runs.
func TestPruneRemovesOldTempFilesOfDeadWriters(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-Retention - time.Hour)
	deadOld := writeTemp(t, dir, 101, old)
	deadRecent := writeTemp(t, dir, 102, now.Add(-time.Hour))
	liveOld := writeTemp(t, dir, 103, old)
	alive := func(s Snapshot) bool { return s.FrontendPID == 103 }

	Prune(dir, now, Retention, alive)

	for name, want := range map[string]bool{deadOld: false, deadRecent: true, liveOld: true} {
		_, err := os.Stat(filepath.Join(dir, name))
		if got := err == nil; got != want {
			t.Errorf("%s on disk = %v, want %v", name, got, want)
		}
	}
}

// Write names its temp file for its own pid, so Prune can tell its writer.
func TestWriteNamesItsTempFileForItsPid(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, Snapshot{Principal: "p"}); err != nil {
		t.Fatal(err)
	}
	pid, ok := tempWriterPID(tempPrefix + strconv.Itoa(os.Getpid()) + "-42")
	if !ok || pid != os.Getpid() {
		t.Errorf("tempWriterPID = %d, %v; want this pid", pid, ok)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{FileName("p")}) {
		t.Errorf("dir after Write = %v, want only the snapshot", names)
	}
}
