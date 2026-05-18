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
