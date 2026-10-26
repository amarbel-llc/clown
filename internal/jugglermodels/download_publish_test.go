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
	"syscall"
	"testing"
	"time"
)

func payloadServer(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/m.gguf", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// swapLink replaces the publish link call for one test.
func swapLink(t *testing.T, f func(oldname, newname string) error) {
	t.Helper()
	orig := linkFile
	linkFile = f
	t.Cleanup(func() { linkFile = orig })
}

// The model's bytes are synced before it is published, and its directory
// after, so a crash cannot leave a truncated model that reads installed.
func TestDownloadSyncsBeforePublishing(t *testing.T) {
	srv := payloadServer(t, []byte("payload"))
	var mu sync.Mutex
	var order []string
	record := func(step string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, step)
	}
	origSync, origDir := syncFile, syncDir
	syncFile = func(f *os.File) error { record("sync file"); return origSync(f) }
	syncDir = func(dir string) error { record("sync dir"); return origDir(dir) }
	t.Cleanup(func() { syncFile, syncDir = origSync, origDir })
	swapLink(t, func(oldname, newname string) error { record("link"); return os.Link(oldname, newname) })

	if _, err := Download(context.Background(), RegistryEntry{Name: "m", URL: srv.URL + "/m.gguf"}, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(order, ", "), "sync file, link, sync dir"; got != want {
		t.Errorf("publish order: %s, want %s", got, want)
	}
}

// Without hard links, publish falls back to rename, which still installs
// the model and leaves no temp file.
func TestDownloadPublishFallbackWithoutHardLinks(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EPERM, syscall.ENOTSUP} {
		t.Run(errno.Error(), func(t *testing.T) {
			swapLink(t, func(oldname, newname string) error {
				return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: errno}
			})
			dir := t.TempDir()
			srv := payloadServer(t, []byte("payload"))
			dest, err := Download(context.Background(), RegistryEntry{Name: "m", URL: srv.URL + "/m.gguf"}, dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(dest); string(b) != "payload" {
				t.Errorf("published %q, want payload", b)
			}
			assertNoTempFiles(t, dir)
		})
	}
}

// The rename fallback replaces nothing: a model that appeared before it
// is reported installed and kept.
func TestDownloadPublishFallbackKeepsAModelInstalledMeanwhile(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "m.gguf")
	swapLink(t, func(oldname, newname string) error {
		if err := os.WriteFile(newname, []byte("installed meanwhile"), 0o644); err != nil {
			t.Error(err)
		}
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: syscall.EPERM}
	})
	srv := payloadServer(t, []byte("payload"))
	_, err := Download(context.Background(), RegistryEntry{Name: "m", URL: srv.URL + "/m.gguf"}, dir, nil)
	if !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("err = %v, want ErrAlreadyInstalled", err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "installed meanwhile" {
		t.Errorf("model installed meanwhile was replaced: %q", b)
	}
	assertNoTempFiles(t, dir)
}

// Any other link failure fails the download and publishes nothing.
func TestDownloadPublishLinkFailure(t *testing.T) {
	swapLink(t, func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: syscall.EIO}
	})
	dir := t.TempDir()
	srv := payloadServer(t, []byte("payload"))
	_, err := Download(context.Background(), RegistryEntry{Name: "m", URL: srv.URL + "/m.gguf"}, dir, nil)
	if err == nil || errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("err = %v, want a publish failure", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "m.gguf")); err == nil {
		t.Error("a failed publish left a model")
	}
	assertNoTempFiles(t, dir)
}

// Something other than a model at the destination is not an installed
// model: a dangling symlink or a directory fails the download, untouched.
func TestDownloadRefusesANonModelInTheWay(t *testing.T) {
	for name, place := range map[string]func(dest string) error{
		"dangling symlink": func(dest string) error { return os.Symlink(dest+".missing", dest) },
		"directory":        func(dest string) error { return os.Mkdir(dest, 0o755) },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "m.gguf")
			if err := place(dest); err != nil {
				t.Fatal(err)
			}
			srv := payloadServer(t, []byte("payload"))
			_, err := Download(context.Background(), RegistryEntry{Name: "m", URL: srv.URL + "/m.gguf"}, dir, nil)
			if err == nil || errors.Is(err, ErrAlreadyInstalled) || !strings.Contains(err.Error(), "not a model") {
				t.Fatalf("err = %v, want a not-a-model error", err)
			}
			if _, err := os.Lstat(dest); err != nil {
				t.Errorf("the %s in the way was removed: %v", name, err)
			}
			assertNoTempFiles(t, dir)
		})
	}
}

// A symlink to a model file is an installed model.
func TestDownloadSymlinkToAModelIsInstalled(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.gguf")
	if err := os.WriteFile(target, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "m.gguf")); err != nil {
		t.Fatal(err)
	}
	_, err := Download(context.Background(), RegistryEntry{Name: "m", URL: "http://example.invalid/"}, dir, nil)
	if !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("err = %v, want ErrAlreadyInstalled", err)
	}
}

// Spellings of one models dir (a symlink to it, a relative path) share a
// download turn: the model is fetched once.
func TestDownloadEquivalentDirSpellingsShareATurn(t *testing.T) {
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	spellings := []string{realDir, filepath.Join(base, "alias"), "real", "./alias/"}

	var requests atomic.Int64
	arrived := make(chan struct{}, len(spellings))
	mux := http.NewServeMux()
	mux.HandleFunc("/m.gguf", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		arrived <- struct{}{}
		// Give downloads that do not share a turn time to overlap here.
		deadline := time.After(500 * time.Millisecond)
		for len(arrived) < len(spellings) {
			select {
			case <-deadline:
				_, _ = w.Write([]byte("payload"))
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
		_, _ = w.Write([]byte("payload"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	entry := RegistryEntry{Name: "m", URL: srv.URL + "/m.gguf"}
	var wg sync.WaitGroup
	for _, dir := range spellings {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Download(context.Background(), entry, dir, nil); err != nil && !errors.Is(err, ErrAlreadyInstalled) {
				t.Errorf("download into %s: %v", dir, err)
			}
		}()
	}
	wg.Wait()
	if got := requests.Load(); got != 1 {
		t.Errorf("model fetched %d times across spellings of one dir, want once", got)
	}
}

// A failed download gives its turn back: the next one proceeds.
func TestDownloadFailedDownloadReleasesItsTurn(t *testing.T) {
	dir := t.TempDir()
	mux := http.NewServeMux()
	mux.HandleFunc("/bad", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	mux.HandleFunc("/m.gguf", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("payload")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, err := Download(context.Background(), RegistryEntry{Name: "m", URL: srv.URL + "/bad"}, dir, nil); err == nil {
		t.Fatal("download of a failing URL succeeded")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Download(ctx, RegistryEntry{Name: "m", URL: srv.URL + "/m.gguf"}, dir, nil); err != nil {
		t.Fatalf("download after a failed one: %v", err)
	}
}

// A published model has the mode downloads always gave it (0600, from
// os.CreateTemp, before and after clown#314).
func TestDownloadPublishedModelMode(t *testing.T) {
	srv := payloadServer(t, []byte("payload"))
	dest, err := Download(context.Background(), RegistryEntry{Name: "m", URL: srv.URL + "/m.gguf"}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Errorf("published mode %o, want 600", got)
	}
}
