package jugglermodels

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ProgressFunc is called periodically during a download with the number
// of bytes written so far and (when known) the total expected size. It
// must be non-blocking; the download loop runs on the same goroutine.
// Pass nil to disable progress reporting.
type ProgressFunc func(written, total int64)

// ErrAlreadyInstalled is the error Download returns, with the model's
// path, when the model is already installed.
var ErrAlreadyInstalled = errors.New("already installed")

// The filesystem calls a download's publish makes, swappable in tests.
var (
	linkFile = os.Link
	syncFile = (*os.File).Sync
	syncDir  = syncDirectory
)

// syncDirectory fsyncs dir so an entry just linked or renamed into it
// survives a crash.
func syncDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Download fetches entry.URL into modelsDir, verifies the SHA-256 if
// entry.SHA256 is set, and publishes the file into place. The
// destination filename is "<entry.Name>.gguf" under modelsDir. Returns the
// final destination path.
//
// It never replaces an installed model (clown#314). A model installed
// before the download starts, or while it runs (by another download, by
// hand, by another process), is left as it is: Download returns its path
// with an error wrapping ErrAlreadyInstalled and removes its own copy. A
// model is a regular file, or a symlink to one; anything else at the
// destination (a directory, a dangling symlink) fails the download and is
// left alone. Downloads of one model in this process run one at a time,
// however modelsDir is spelled (relative, through a symlink), so the later
// ones find it installed without fetching it again; waiting for a turn
// honours ctx. The model's bytes are synced before it is published, and
// its directory after, so a crash cannot leave a truncated model in place.
// The temporary file is removed on every path, a cancel included.
//
// progress is invoked synchronously on each chunk read; pass nil to skip.
// When modelsDir is empty, falls back to Dir().
func Download(ctx context.Context, entry RegistryEntry, modelsDir string, progress ProgressFunc) (string, error) {
	if modelsDir == "" {
		modelsDir = Dir()
	}
	if modelsDir == "" {
		return "", fmt.Errorf("models dir unavailable: cannot resolve $HOME")
	}
	dest := filepath.Join(modelsDir, entry.Name+".gguf")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", modelsDir, err)
	}
	release, err := acquireDownloadSlot(ctx, downloadSlotKey(modelsDir, entry.Name))
	if err != nil {
		return "", fmt.Errorf("wait for another download of %q: %w", entry.Name, err)
	}
	defer release()
	if err := whatIsAt(entry.Name, dest); err != nil {
		if errors.Is(err, ErrAlreadyInstalled) {
			return dest, err
		}
		return "", err
	}

	tmp, err := os.CreateTemp(modelsDir, entry.Name+".*.gguf.tmp")
	if err != nil {
		return "", fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	// Always removed: once published it is a second link to the model (or,
	// after the rename fallback, already gone).
	defer os.Remove(tmpPath)
	defer tmp.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, entry.URL, nil)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	client := &http.Client{Timeout: 2 * time.Hour}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Stream with optional progress + SHA-256 verification.
	h := sha256.New()
	var written int64
	buf := make([]byte, 64*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				return "", fmt.Errorf("write: %w", werr)
			}
			h.Write(buf[:n])
			written += int64(n)
			if progress != nil {
				progress(written, entry.Size)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", fmt.Errorf("read: %w", rerr)
		}
	}
	if err := syncFile(tmp); err != nil {
		return "", fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp: %w", err)
	}

	if entry.SHA256 != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if got != entry.SHA256 {
			return "", fmt.Errorf("sha256 mismatch: got %s, want %s", got, entry.SHA256)
		}
	}

	if err := publish(tmpPath, dest, entry.Name); err != nil {
		if errors.Is(err, ErrAlreadyInstalled) {
			return dest, err
		}
		return "", err
	}
	return dest, nil
}

func alreadyInstalled(name, dest string) error {
	return fmt.Errorf("model %q %w at %s", name, ErrAlreadyInstalled, dest)
}

// whatIsAt says what is at dest: nil for nothing, an error wrapping
// ErrAlreadyInstalled for a model (a regular file, or a symlink to one),
// and otherwise an error naming what is in the way.
func whatIsAt(name, dest string) error {
	if _, err := os.Lstat(dest); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("model %q: check %s: %w", name, dest, err)
	}
	st, err := os.Stat(dest)
	if err != nil {
		return fmt.Errorf("model %q: %s is not a model: %w", name, dest, err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("model %q: %s is not a model (%s)", name, dest, st.Mode().Type())
	}
	return alreadyInstalled(name, dest)
}

// downloadSlotKey names a model's download turn by its canonical path, so
// every spelling of modelsDir shares one turn. modelsDir exists by now;
// if its symlinks cannot be resolved, the cleaned absolute path is used.
func downloadSlotKey(modelsDir, name string) string {
	dir, err := filepath.Abs(modelsDir)
	if err != nil {
		dir = filepath.Clean(modelsDir)
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return filepath.Join(dir, name+".gguf")
}

// downloadSlots holds one slot per model (by downloadSlotKey): a
// 1-buffered channel, so a download waiting for its turn can give up when
// its ctx ends.
var downloadSlots sync.Map // slot key → chan struct{}

func acquireDownloadSlot(ctx context.Context, key string) (release func(), err error) {
	v, _ := downloadSlots.LoadOrStore(key, make(chan struct{}, 1))
	slot := v.(chan struct{})
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// publish puts tmpPath in place at dest without replacing anything there:
// os.Link fails if dest exists, whoever created it. On a filesystem
// without hard links it falls back to a fresh check then os.Rename. That
// fallback is race-free only against downloads in this process, which
// hold dest's slot; another process installing the same model at the
// same moment could still be replaced.
//
// Once published, the directory is synced so the new entry survives a
// crash. That is best effort: some filesystems refuse to sync a directory
// (EINVAL), and the model is in place either way, so its error is ignored.
func publish(tmpPath, dest, name string) error {
	err := linkFile(tmpPath, dest)
	switch {
	case err == nil:
		_ = syncDir(filepath.Dir(dest))
		return nil
	case errors.Is(err, fs.ErrExist):
		if inTheWay := whatIsAt(name, dest); inTheWay != nil {
			return inTheWay
		}
		return fmt.Errorf("publish: %w", err)
	case !hardLinksUnsupported(err):
		return fmt.Errorf("publish: %w", err)
	}
	if err := whatIsAt(name, dest); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	_ = syncDir(filepath.Dir(dest))
	return nil
}

func hardLinksUnsupported(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP)
}
