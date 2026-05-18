package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

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
