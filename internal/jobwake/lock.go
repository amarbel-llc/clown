package jobwake

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrAlreadyWatching is returned by Watch when another live monitor already
// holds the per-channel singleton lock (clown#132). Callers treat it as a clean
// no-op (a second job-watch over a channel that is already watched), not a
// failure.
var ErrAlreadyWatching = errors.New("another job-watch is already watching this channel")

// acquireWatchLock takes the per-channel job-watch singleton lock via a
// non-blocking exclusive flock on WatchLockFile(cid). On success it returns a
// release func (call on monitor shutdown). It returns ErrAlreadyWatching when
// another monitor holds the lock, or a wrapped error on a real failure. flock is
// advisory and tied to the open file description, so a second monitor — in this
// process or another — fails LOCK_NB, and process death frees the lock.
func acquireWatchLock(cid string) (func(), error) {
	path := WatchLockFile(cid)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyWatching
		}
		return nil, fmt.Errorf("watch lock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
