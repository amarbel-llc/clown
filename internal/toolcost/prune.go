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

// PruneState removes the "*.json" and "*.json.lock" files in dir not modified
// within maxAge of now, except keep (a state path whose lock the caller
// holds). It reports how many it removed. Errors are ignored: pruning is
// housekeeping and must never fail a hook.
func PruneState(dir string, maxAge time.Duration, now time.Time, keep string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !(strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".json.lock")) {
			continue
		}
		path := filepath.Join(dir, name)
		if path == keep || path == keep+".lock" {
			continue
		}
		fi, err := e.Info()
		if err != nil || now.Sub(fi.ModTime()) <= maxAge {
			continue
		}
		if os.Remove(path) == nil {
			removed++
		}
	}
	return removed
}
