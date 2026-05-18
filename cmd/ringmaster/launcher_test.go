package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

// buildFakeLlamaServer compiles cmd/ringmaster/testdata/fake-llama-server
// into a tempfile and returns its absolute path. The binary is reused
// across tests in the package via TestMain (kept simple here — rebuilt per test).
func buildFakeLlamaServer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fake-llama-server")
	src, _ := filepath.Abs("./testdata/fake-llama-server")
	cmd := exec.Command("go", "build", "-o", bin, src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fake-llama-server: %v\n%s", err, out)
	}
	return bin
}

func TestLauncher_StartAndStop(t *testing.T) {
	bin := buildFakeLlamaServer(t)
	reg := rm.NewRegistry()
	l := newLauncher(bin, reg, t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	in, err := l.Start(ctx, rm.StartInstanceParams{
		Alias: "test",
		Model: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if in.Port == 0 || in.PID == 0 {
		t.Errorf("expected non-zero port/pid: %+v", in)
	}
	if got, ok := reg.Get("test"); !ok || got.Alias != "test" {
		t.Errorf("registry missing after Start: got=%+v ok=%v", got, ok)
	}

	if err := l.Stop(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("test"); ok {
		t.Errorf("registry should be empty after Stop")
	}
}
