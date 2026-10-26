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

// Download fetches entry.URL into modelsDir, verifies the SHA-256 if
// entry.SHA256 is set, and publishes the file into place. The
// destination filename is "<entry.Name>.gguf" under modelsDir. Returns the
// final destination path.
//
// It never replaces an installed model (clown#314). A model installed
// before the download starts, or while it runs (by another download, by
// hand, by another process), is left as it is: Download returns its path
// with an error wrapping ErrAlreadyInstalled and removes its own copy.
// Downloads of one model in this process run one at a time, so the later
// ones find it installed without fetching it again; waiting for a turn
// honours ctx. The temporary file is removed on every path, a cancel
// included.
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
	release, err := acquireDownloadSlot(ctx, dest)
	if err != nil {
		return "", fmt.Errorf("wait for another download of %q: %w", entry.Name, err)
	}
	defer release()
	if _, err := os.Lstat(dest); err == nil {
		return dest, alreadyInstalled(entry.Name, dest)
	}
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", modelsDir, err)
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

// downloadSlots holds one slot per destination path: a 1-buffered channel,
// so a download waiting for its turn can give up when its ctx ends.
var downloadSlots sync.Map // dest path → chan struct{}

func acquireDownloadSlot(ctx context.Context, dest string) (release func(), err error) {
	v, _ := downloadSlots.LoadOrStore(dest, make(chan struct{}, 1))
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
func publish(tmpPath, dest, name string) error {
	err := os.Link(tmpPath, dest)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrExist):
		return alreadyInstalled(name, dest)
	case !hardLinksUnsupported(err):
		return fmt.Errorf("publish: %w", err)
	}
	if _, err := os.Lstat(dest); err == nil {
		return alreadyInstalled(name, dest)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func hardLinksUnsupported(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP)
}
