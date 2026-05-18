package circusmodels

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ProgressFunc is called periodically during a download with the number
// of bytes written so far and (when known) the total expected size. It
// must be non-blocking; the download loop runs on the same goroutine.
// Pass nil to disable progress reporting.
type ProgressFunc func(written, total int64)

// Download fetches entry.URL into modelsDir, verifies the SHA-256 if
// entry.SHA256 is set, and atomically renames the file into place. The
// destination filename is "<entry.Name>.gguf" under modelsDir. Refuses
// to overwrite an existing model. Returns the final destination path.
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
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("model %q already installed at %s", entry.Name, dest)
	}
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", modelsDir, err)
	}

	tmp, err := os.CreateTemp(modelsDir, entry.Name+".*.gguf.tmp")
	if err != nil {
		return "", fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, entry.URL, nil)
	if err != nil {
		_ = tmp.Close()
		cleanup()
		return "", fmt.Errorf("request: %w", err)
	}
	client := &http.Client{Timeout: 2 * time.Hour}
	resp, err := client.Do(req)
	if err != nil {
		_ = tmp.Close()
		cleanup()
		return "", fmt.Errorf("get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = tmp.Close()
		cleanup()
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
				_ = tmp.Close()
				cleanup()
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
			_ = tmp.Close()
			cleanup()
			return "", fmt.Errorf("read: %w", rerr)
		}
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", fmt.Errorf("close temp: %w", err)
	}

	if entry.SHA256 != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if got != entry.SHA256 {
			cleanup()
			return "", fmt.Errorf("sha256 mismatch: got %s, want %s", got, entry.SHA256)
		}
	}

	if err := os.Rename(tmpPath, dest); err != nil {
		cleanup()
		return "", fmt.Errorf("rename: %w", err)
	}
	return dest, nil
}
