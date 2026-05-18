package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amarbel-llc/clown/internal/circusmodels"
	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

// shortTempDir returns a short-path tmpdir. macOS imposes a ~104-char
// limit on Unix domain socket paths (sun_path), and the project's
// devshell sets TMPDIR inside the worktree, which can exceed it. Use
// /tmp explicitly and clean up via t.Cleanup. Mirrors the helper in
// internal/ringmaster/client_test.go; test packages are independent
// so the helper is copied rather than imported.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ringmaster-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestServer_ListInstances_Empty(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(rm.NewRegistry(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go srv.Serve(ctx, ln)

	cli, err := rm.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	res, err := cli.ListInstances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Instances) != 0 {
		t.Errorf("expected empty, got %+v", res.Instances)
	}
}

func TestServer_ListAvailableModels(t *testing.T) {
	// Create a fake models dir with two .gguf files of known sizes.
	modelsDir := shortTempDir(t)
	for _, name := range []string{"alpha", "beta"} {
		path := filepath.Join(modelsDir, name+".gguf")
		if err := os.WriteFile(path, []byte("not-a-real-gguf"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Also a non-gguf file that should be skipped.
	_ = os.WriteFile(filepath.Join(modelsDir, "notes.txt"), []byte("ignore me"), 0o644)

	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(rm.NewRegistry(), nil)
	srv.modelsDir = modelsDir // see Step 3 — server needs this field
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go srv.Serve(ctx, ln)

	cli, err := rm.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	res, err := cli.ListAvailableModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Models) != 2 {
		t.Fatalf("expected 2 models, got %d: %+v", len(res.Models), res.Models)
	}
	// listAvailableModels uses circusmodels.List which sorts.
	if res.Models[0].Name != "alpha" || res.Models[1].Name != "beta" {
		t.Errorf("model order wrong: %+v", res.Models)
	}
	for _, m := range res.Models {
		if m.Path == "" || m.Size == 0 {
			t.Errorf("model missing path/size: %+v", m)
		}
	}
}

func TestServer_DownloadModel(t *testing.T) {
	payload := []byte("this is a fake gguf file for testing")
	sum := sha256.Sum256(payload)
	expectedSHA := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/fake.gguf", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()

	modelsDir := shortTempDir(t)
	models := []circusmodels.RegistryEntry{
		{
			Name:   "fake",
			URL:    httpSrv.URL + "/fake.gguf",
			SHA256: expectedSHA,
			Size:   int64(len(payload)),
		},
	}

	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(rm.NewRegistry(), nil)
	srv.modelsDir = modelsDir
	srv.models = models
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go srv.Serve(ctx, ln)

	cli, err := rm.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	res, err := cli.DownloadModel(ctx, rm.DownloadModelParams{Name: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Model.Name != "fake" {
		t.Errorf("name: %q", res.Model.Name)
	}
	if res.Model.Size != int64(len(payload)) {
		t.Errorf("size: got %d, want %d", res.Model.Size, len(payload))
	}
	want := filepath.Join(modelsDir, "fake.gguf")
	if res.Model.Path != want {
		t.Errorf("path: got %q, want %q", res.Model.Path, want)
	}
	if _, err := os.Stat(res.Model.Path); err != nil {
		t.Errorf("file not present at %s: %v", res.Model.Path, err)
	}
}

func TestServer_DownloadModel_UnknownName(t *testing.T) {
	modelsDir := shortTempDir(t)
	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(rm.NewRegistry(), nil)
	srv.modelsDir = modelsDir
	srv.models = []circusmodels.RegistryEntry{} // explicit empty (non-nil) override
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go srv.Serve(ctx, ln)

	cli, err := rm.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	if _, err := cli.DownloadModel(ctx, rm.DownloadModelParams{Name: "nope"}); err == nil {
		t.Fatal("expected error for unknown model")
	}
}

func TestServer_StopAll(t *testing.T) {
	bin := buildFakeLlamaServer(t)
	reg := rm.NewRegistry()
	l := newLauncher(bin, reg, shortTempDir(t))

	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(reg, l)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go srv.Serve(ctx, ln)

	cli, err := rm.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	// Start two instances.
	for _, alias := range []string{"a", "b"} {
		if _, err := cli.StartInstance(ctx, rm.StartInstanceParams{
			Alias: alias,
			Model: alias,
		}); err != nil {
			t.Fatalf("start %s: %v", alias, err)
		}
	}

	// StopAll should return both aliases and leave the registry empty.
	res, err := cli.StopAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stopped) != 2 {
		t.Errorf("expected 2 stopped, got %d: %+v", len(res.Stopped), res.Stopped)
	}
	list, _ := cli.ListInstances(ctx)
	if len(list.Instances) != 0 {
		t.Errorf("registry should be empty after StopAll, got %+v", list)
	}
}

func TestServer_StartListStop(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}

	bin := buildFakeLlamaServer(t)
	reg := rm.NewRegistry()
	l := newLauncher(bin, reg, shortTempDir(t))
	srv := newServer(reg, l)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go srv.Serve(ctx, ln)

	cli, err := rm.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	startRes, err := cli.StartInstance(ctx, rm.StartInstanceParams{
		Alias: "a",
		Model: "a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if startRes.Instance.Alias != "a" || startRes.Instance.Port == 0 {
		t.Errorf("start result: %+v", startRes)
	}

	list, err := cli.ListInstances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Instances) != 1 || list.Instances[0].Alias != "a" {
		t.Errorf("list=%+v", list)
	}

	getRes, err := cli.GetInstance(ctx, rm.GetInstanceParams{Alias: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if getRes.Instance.Alias != "a" {
		t.Errorf("get=%+v", getRes)
	}

	if err := cli.StopInstance(ctx, rm.StopInstanceParams{Alias: "a"}); err != nil {
		t.Fatal(err)
	}
	list2, err := cli.ListInstances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list2.Instances) != 0 {
		t.Errorf("after stop list=%+v", list2)
	}
}
