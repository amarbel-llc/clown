package toolcost

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StateMaxAge is how long a transcript's state file (and its lock) may go
// untouched before PruneState removes it (clown#321). Every Stop of a live
// transcript rewrites its state, so only a transcript nobody has touched for
// this long loses it; a resumed one is processed again from the start.
const StateMaxAge = 30 * 24 * time.Hour

// PruneState removes the "*.json" state files in dir not modified within
// maxAge of now, except keep (a state path the caller is about to use), and
// the "*.json.lock" files left without a state file and as old. A lock is
// never removed while its state file exists: a lock is never rewritten, so
// its mtime is its creation time, and a live transcript's lock is old while
// a run may hold it. It reports how many files it removed. Errors are
// ignored: pruning is housekeeping and must never fail a hook.
func PruneState(dir string, maxAge time.Duration, now time.Time, keep string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	stale := func(e os.DirEntry) bool {
		fi, err := e.Info()
		return err == nil && now.Sub(fi.ModTime()) > maxAge
	}
	states := map[string]bool{}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		if path != keep && stale(e) && os.Remove(path) == nil {
			removed++
			continue
		}
		states[name] = true
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".json.lock") || states[strings.TrimSuffix(name, ".lock")] {
			continue
		}
		if path := filepath.Join(dir, name); path != keep+".lock" && stale(e) && os.Remove(path) == nil {
			removed++
		}
	}
	return removed
}
