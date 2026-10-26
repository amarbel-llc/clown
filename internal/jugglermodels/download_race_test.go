package jugglermodels

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func isAlreadyInstalled(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already installed")
}

// Concurrent downloads of one model (clown#314): one publishes, the rest
// report it already installed; the file is the payload, nothing is left
// behind, and the model is fetched once.
func TestDownloadConcurrentDownloadsOfOneModel(t *testing.T) {
	const n = 8
	payload := []byte(strings.Repeat("gguf bytes ", 4096))
	var requests atomic.Int64
	arrived := make(chan struct{}, n)
	mux := http.NewServeMux()
	mux.HandleFunc("/m.gguf", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		arrived <- struct{}{}
		// Hold every request until all n are in flight (or a while has
		// passed), so downloads that can overlap do.
		deadline := time.After(500 * time.Millisecond)
		for len(arrived) < n {
			select {
			case <-deadline:
				_, _ = w.Write(payload)
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
		_, _ = w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	entry := RegistryEntry{Name: "m", URL: srv.URL + "/m.gguf", SHA256: shaHex(payload), Size: int64(len(payload))}
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dest, err := Download(context.Background(), entry, dir, nil)
			if dest != filepath.Join(dir, "m.gguf") && err == nil {
				t.Errorf("dest = %q", dest)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	published := 0
	for err := range errs {
		switch {
		case err == nil:
			published++
		case isAlreadyInstalled(err):
		default:
			t.Errorf("download: %v", err)
		}
	}
	if published != 1 {
		t.Errorf("%d downloads published the model, want exactly 1", published)
	}
	got, err := os.ReadFile(filepath.Join(dir, "m.gguf"))
	if err != nil {
		t.Fatal(err)
	}
	if shaHex(got) != entry.SHA256 {
		t.Errorf("published file: %d bytes, sha %s, want the payload", len(got), shaHex(got))
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("model fetched %d times, want once", got)
	}
	assertNoTempFiles(t, dir)
}

// A model that appears while it is being downloaded (installed by hand, or
// by another process) is not overwritten.
func TestDownloadDoesNotOverwriteAModelInstalledMeanwhile(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "m.gguf")
	payload := []byte("downloaded payload")
	mux := http.NewServeMux()
	mux.HandleFunc("/m.gguf", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload[:4])
		w.(http.Flusher).Flush()
		if err := os.WriteFile(dest, []byte("installed by hand"), 0o644); err != nil {
			t.Error(err)
		}
		_, _ = w.Write(payload[4:])
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := Download(context.Background(), RegistryEntry{Name: "m", URL: srv.URL + "/m.gguf"}, dir, nil)
	if !isAlreadyInstalled(err) {
		t.Fatalf("Download over a model installed meanwhile: err = %v, want already installed", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "installed by hand" {
		t.Errorf("model installed meanwhile was overwritten: %q", got)
	}
	assertNoTempFiles(t, dir)
}

// A model already installed is reported as such, with its path, and left
// untouched.
func TestDownloadLeavesAnInstalledModelAlone(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "m.gguf")
	if err := os.WriteFile(dest, []byte("already here"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Download(context.Background(), RegistryEntry{Name: "m", URL: "http://example.invalid/"}, dir, nil)
	if !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("err = %v, want ErrAlreadyInstalled", err)
	}
	if got != dest {
		t.Errorf("dest = %q, want %q", got, dest)
	}
	if b, _ := os.ReadFile(dest); string(b) != "already here" {
		t.Errorf("installed model changed: %q", b)
	}
}

// A download cancelled midway leaves neither a temp file nor the model.
func TestDownloadCancelledMidwayLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	mux := http.NewServeMux()
	mux.HandleFunc("/m.gguf", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := Download(ctx, RegistryEntry{Name: "m", URL: srv.URL + "/m.gguf"}, dir, func(int64, int64) { cancel() })
	if err == nil {
		t.Fatal("cancelled download succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, "m.gguf")); err == nil {
		t.Error("cancelled download published the model")
	}
	assertNoTempFiles(t, dir)
}

// A download waiting behind another of the same model gives up when its
// ctx is cancelled.
func TestDownloadWaitingBehindAnotherHonoursCancel(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	inFlight := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/m.gguf", func(w http.ResponseWriter, r *http.Request) {
		select {
		case inFlight <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("payload"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	entry := RegistryEntry{Name: "m", URL: srv.URL + "/m.gguf"}

	first := make(chan error, 1)
	go func() {
		_, err := Download(context.Background(), entry, dir, nil)
		first <- err
	}()
	<-inFlight

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() {
		_, err := Download(ctx, entry, dir, nil)
		second <- err
	}()
	select {
	case err := <-second:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("waiting download: err = %v, want its ctx's deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("a waiting download ignored its cancelled ctx")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first download: %v", err)
	}
	assertNoTempFiles(t, dir)
}
