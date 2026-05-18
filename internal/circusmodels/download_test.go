package circusmodels

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shaHex returns the hex SHA-256 of b.
func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestDownload_Success(t *testing.T) {
	payload := []byte("a small fake gguf payload")
	mux := http.NewServeMux()
	mux.HandleFunc("/fake.gguf", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	entry := RegistryEntry{
		Name:   "fake",
		URL:    srv.URL + "/fake.gguf",
		SHA256: shaHex(payload),
		Size:   int64(len(payload)),
	}

	var lastWritten, lastTotal int64
	progress := func(written, total int64) {
		lastWritten = written
		lastTotal = total
	}

	dest, err := Download(context.Background(), entry, dir, progress)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	want := filepath.Join(dir, "fake.gguf")
	if dest != want {
		t.Errorf("dest = %q, want %q", dest, want)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("payload mismatch: got %q, want %q", got, payload)
	}

	if lastWritten != int64(len(payload)) {
		t.Errorf("progress: lastWritten = %d, want %d", lastWritten, len(payload))
	}
	if lastTotal != int64(len(payload)) {
		t.Errorf("progress: lastTotal = %d, want %d", lastTotal, len(payload))
	}
}

func TestDownload_SHA256Mismatch(t *testing.T) {
	payload := []byte("the real payload")
	mux := http.NewServeMux()
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	entry := RegistryEntry{
		Name:   "fake",
		URL:    srv.URL + "/file",
		SHA256: strings.Repeat("0", 64),
		Size:   int64(len(payload)),
	}

	_, err := Download(context.Background(), entry, dir, nil)
	if err == nil {
		t.Fatal("expected sha256 mismatch error")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Errorf("unexpected error: %v", err)
	}
	// Destination file must not exist.
	if _, err := os.Stat(filepath.Join(dir, "fake.gguf")); err == nil {
		t.Errorf("destination file should not exist after sha mismatch")
	}
	// No leftover temp files.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestDownload_HTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	entry := RegistryEntry{
		Name: "fake",
		URL:  srv.URL + "/file",
	}
	_, err := Download(context.Background(), entry, dir, nil)
	if err == nil {
		t.Fatal("expected HTTP error")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDownload_AlreadyInstalled(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "fake.gguf")
	if err := os.WriteFile(dest, []byte("already here"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := RegistryEntry{Name: "fake", URL: "http://example.invalid/"}
	_, err := Download(context.Background(), entry, dir, nil)
	if err == nil {
		t.Fatal("expected already-installed error")
	}
	if !strings.Contains(err.Error(), "already installed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDownload_SkipsSHA256WhenEmpty(t *testing.T) {
	payload := []byte("contents without checksum")
	mux := http.NewServeMux()
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	entry := RegistryEntry{
		Name: "fake",
		URL:  srv.URL + "/file",
		// SHA256 intentionally empty.
	}
	dest, err := Download(context.Background(), entry, dir, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("dest missing: %v", err)
	}
}
