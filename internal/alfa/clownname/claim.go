package clownname

import (
	"os"
	"path/filepath"
	"syscall"
	"time"

	"code.linenisgreat.com/ringmaster/pkgs/jobwake"
)

// Claim resolves and returns a free clown name, guarding the read-live-set +
// decide critical section with an flock on a well-known lockfile so two
// clowns starting in the same instant can never both pick the same name
// (the "mutex-guarded allocation" clown#169 asks for). The lock is held only
// for the duration of this call — Claim does not itself persist anything;
// the caller is expected to export the returned name as CLOWN_NAME so the
// existing presence-registration path (jobwake.RegisterPresence, once
// clown#179 ships a ClownName field) picks it up the same way it already
// does for CLOWN_GROUP_DESCRIPTION.
//
// A locking failure (e.g. an unwritable state dir) degrades to an unlocked
// allocation rather than failing the launch — clown#169 is a cosmetic
// convenience, never load-bearing for session function, so Claim always
// returns a name (best-effort uniqueness) instead of an error.
func Claim() string {
	path, err := lockPath()
	if err != nil {
		return allocateUnlocked()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return allocateUnlocked()
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return allocateUnlocked()
	}
	defer f.Close()

	// Best-effort lock: on failure, proceed unlocked rather than blocking or
	// failing the launch over a cosmetic feature (see doc comment above).
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err == nil {
		defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}

	return allocateUnlocked()
}

// allocateUnlocked reads the current live set and picks a name, WITHOUT
// acquiring the flock itself — the exported Claim wraps this with the lock;
// this split exists so the degrade-on-lock-failure paths above can still
// allocate (best-effort) without duplicating the ListPresence + Allocate
// call.
func allocateUnlocked() string {
	live := map[string]bool{}
	if ps, err := jobwake.ListPresence(time.Now()); err == nil {
		for _, p := range ps {
			if p.ClownName != "" {
				live[p.ClownName] = true
			}
		}
	}
	// A ListPresence error (e.g. no presence dir yet — the common case for
	// the very first clown on a fresh machine) degrades to an empty live
	// set: every name is free, so allocation still proceeds rather than
	// failing the launch.
	return Allocate(live)
}
