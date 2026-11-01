// Package logcap bounds the append-only logs an unattended clown session
// writes (clown#321). A log keeps at most one rotated generation, so a file
// and its ".1" together stay near twice the cap.
package logcap

import (
	"os"
	"sync"
)

// MaxBytes is the default cap of one log file before it rotates.
const MaxBytes = 1 << 20

// RotateIfLarge renames path to path+".1" (replacing an older one) when path
// is a regular file over max bytes. Anything else, and every error, leaves
// the log as it is: a log that cannot rotate must still be writable, and a
// concurrent rotation by another process is harmless.
func RotateIfLarge(path string, max int64) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() <= max {
		return
	}
	_ = os.Rename(path, path+".1")
}

// File is an append-only log file that rotates itself once a write takes it
// over max bytes, for a log held open for the life of a process.
type File struct {
	path string
	max  int64
	mu   sync.Mutex
	f    *os.File
	size int64
}

// Open opens path for appending (0600), rotating first when it is already
// over max.
func Open(path string, max int64) (*File, error) {
	RotateIfLarge(path, max)
	l := &File{path: path, max: max}
	if err := l.reopen(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *File) reopen() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, fi.Size()
	return nil
}

// Write appends p, rotating beforehand when the file is already over the cap.
// A rotation that fails to reopen keeps writing to the old file.
func (l *File) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size > l.max {
		old := l.f
		RotateIfLarge(l.path, l.max)
		if err := l.reopen(); err != nil {
			l.f = old
		} else {
			old.Close()
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

// Close closes the file.
func (l *File) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
