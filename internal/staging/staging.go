// Package staging owns the single per-launch directory that every artifact
// clown generates for a provider lives under.
//
// The point is not tidiness. Before this package, seven call sites each made
// their own os.MkdirTemp(""), which meant "make clown's artifacts visible to
// the provider" was an unbounded problem — and it is exactly why the clownbox
// path resorts to overwriting $TMPDIR globally and why a container locus has
// no answer at all (clown#205). With one root, exposure is ONE mount.
//
// Two consequences of that goal shape the API.
//
// Close is terminal: Dir and File report ErrClosed afterwards, and Path goes
// empty. An artifact created under a closed root would sit outside both the
// root's cleanup and any mount a locus derived from Path — reintroducing at a
// new site the exact failure this package exists to remove. A stale Path
// invites the same thing by way of a mount or an os.MkdirAll.
//
// Root is safe for concurrent use: one Root is shared by every artifact writer
// in a launch, so an unsynchronised closed flag is a race waiting for the first
// writer that moves onto a goroutine. The lock also makes Dir and File atomic
// with respect to Close, so neither can half-create into a directory that
// RemoveAll is already walking.
//
// Design record: docs/plans/2026-07-28-containment-primitive-design.md
// (part 1b).
package staging

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

// ErrClosed is returned by Dir and File after Close. See the package doc for
// why a use-after-close is refused rather than quietly satisfied.
var ErrClosed = errors.New("staging root is closed")

// Root is a launch's staging directory. Safe for concurrent use.
type Root struct {
	mu     sync.Mutex
	dir    string
	closed bool
}

// New creates a launch staging root under base. An empty base uses $TMPDIR
// (os.MkdirTemp's default), preserving today's placement. A non-empty base is
// created if missing: base names a directory this package owns, and the
// clownbox root (<repo>/.tmp) is gitignored, so it is absent on a fresh clone.
func New(base string) (*Root, error) {
	if base != "" {
		if err := os.MkdirAll(base, 0o700); err != nil {
			return nil, fmt.Errorf("create staging base %q: %w", base, err)
		}
	}
	dir, err := os.MkdirTemp(base, "clown-launch-*")
	if err != nil {
		return nil, fmt.Errorf("create staging root: %w", err)
	}
	return &Root{dir: dir}, nil
}

// Path is the root directory — the one path a locus has to expose. It is empty
// after Close; see the package doc.
func (r *Root) Path() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ""
	}
	return r.dir
}

// Dir creates a subdirectory for one artifact group. prefix follows
// os.MkdirTemp. Returns ErrClosed after Close.
func (r *Root) Dir(prefix string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", ErrClosed
	}
	dir, err := os.MkdirTemp(r.dir, prefix)
	if err != nil {
		return "", fmt.Errorf("create staging dir %q: %w", prefix, err)
	}
	return dir, nil
}

// File creates a file under the root. pattern follows os.CreateTemp. Returns
// ErrClosed after Close.
func (r *Root) File(pattern string) (*os.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	f, err := os.CreateTemp(r.dir, pattern)
	if err != nil {
		return nil, fmt.Errorf("create staging file %q: %w", pattern, err)
	}
	return f, nil
}

// Close removes the root and everything beneath it. Idempotent, so callers can
// both `defer Close()` and close early on an error path.
//
// Outstanding handles from File are the caller's to close first. The lock makes
// creation atomic with respect to Close, but not writing: Close unlinks the
// tree, and on Unix a write through a surviving handle lands in an unlinked
// inode and is silently lost.
//
// The root is marked closed even if removal fails: Close states that the launch
// is over, and continuing to write artifacts into a directory nobody will clean
// up again is worse than the leak the error already reports.
func (r *Root) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if err := os.RemoveAll(r.dir); err != nil {
		return fmt.Errorf("remove staging root %q: %w", r.dir, err)
	}
	return nil
}
