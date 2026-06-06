package pluginhost

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

var fakeServerBin string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "pluginhost-test-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)

	bin := filepath.Join(tmp, "fakeserver")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/fakeserver.go")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("building fakeserver: " + err.Error())
	}
	fakeServerBin = bin

	os.Exit(m.Run())
}

func newTestServer(t *testing.T, mode string) *ManagedServer {
	t.Helper()
	args := []string{}
	if mode != "" {
		args = []string{mode}
	}
	return &ManagedServer{
		Name:      "test/fakeserver",
		PluginDir: ".",
		Def: ServerDef{
			Command: fakeServerBin,
			Args:    args,
			Env:     map[string]string{},
			Healthcheck: HealthcheckDef{
				Path:     "/healthz",
				Interval: JSONDuration{Duration: 50 * time.Millisecond},
				Timeout:  JSONDuration{Duration: 5 * time.Second},
			},
		},
	}
}

func TestMergeEnvKeepsAllEntries(t *testing.T) {
	base := []string{"A=1"}
	got := mergeEnv(base, map[string]string{"B": "2", "C": "3"})

	want := map[string]bool{"A=1": true, "B=2": true, "C=3": true}
	for _, e := range got {
		delete(want, e)
	}
	if len(want) != 0 {
		t.Fatalf("missing env entries %v; got %v", want, got)
	}
	if len(base) != 1 || base[0] != "A=1" {
		t.Fatalf("mergeEnv mutated base: %v", base)
	}
}

func TestMergeEnvEmptyExtraReturnsBase(t *testing.T) {
	base := []string{"A=1"}
	got := mergeEnv(base, nil)
	if len(got) != 1 || got[0] != "A=1" {
		t.Fatalf("want base unchanged, got %v", got)
	}
}

func TestManagedServer_CleanStop(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	srv := newTestServer(t, "sleep")
	srv.Logger = logger

	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case <-srv.Done():
		t.Fatal("server exited before Stop was called")
	case <-time.After(200 * time.Millisecond):
	}

	srv.Stop()

	output := buf.String()
	if !strings.Contains(output, "plugin server exited cleanly") {
		t.Errorf("expected 'plugin server exited cleanly' in log, got:\n%s", output)
	}
	if strings.Contains(output, "plugin server died unexpectedly") {
		t.Errorf("unexpected death log in clean stop:\n%s", output)
	}
}

func TestManagedServer_UnexpectedDeath(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	srv := newTestServer(t, "exit-immediate")
	srv.Logger = logger

	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case <-srv.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server to die")
	}

	srv.Stop()

	output := buf.String()
	if !strings.Contains(output, "plugin server died unexpectedly") {
		t.Errorf("expected 'plugin server died unexpectedly' in log, got:\n%s", output)
	}
	if !strings.Contains(output, "level=ERROR") {
		t.Errorf("expected ERROR level in log, got:\n%s", output)
	}
	if !strings.Contains(output, "exit_code=0") {
		t.Errorf("expected exit_code=0 in log, got:\n%s", output)
	}
	if strings.Contains(output, "plugin server exited cleanly") {
		t.Errorf("unexpected clean exit log:\n%s", output)
	}
}

func TestManagedServer_UnexpectedDeathNonZero(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	srv := newTestServer(t, "exit-code")
	srv.Logger = logger

	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case <-srv.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server to die")
	}

	srv.Stop()

	output := buf.String()
	if !strings.Contains(output, "plugin server died unexpectedly") {
		t.Errorf("expected 'plugin server died unexpectedly' in log, got:\n%s", output)
	}
	if !strings.Contains(output, "exit_code=42") {
		t.Errorf("expected exit_code=42 in log, got:\n%s", output)
	}
}

func TestManagedServer_CrashBeforeHandshakeCapturesFinalStderr(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	srv := newTestServer(t, "crash-before-handshake")
	srv.Logger = logger

	if err := srv.Start(context.Background()); err == nil {
		t.Fatal("expected Start to fail when the plugin crashes before handshake")
	}

	output := buf.String()
	if !strings.Contains(output, "fatal: fakeserver crash diagnostic") {
		t.Errorf("expected final stderr diagnostic in log, got:\n%s", output)
	}
}

func TestManagedServer_SignalDeath(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	srv := newTestServer(t, "sleep")
	srv.Logger = logger

	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case <-srv.Done():
		t.Fatal("server exited before kill")
	case <-time.After(200 * time.Millisecond):
	}

	// Kill the server with SIGKILL (not SIGTERM which it handles gracefully).
	srv.cmd.Process.Signal(syscall.SIGKILL)

	select {
	case <-srv.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server to die after SIGKILL")
	}

	srv.Stop()

	output := buf.String()
	if !strings.Contains(output, "plugin server died unexpectedly") {
		t.Errorf("expected 'plugin server died unexpectedly' in log, got:\n%s", output)
	}
	if !strings.Contains(output, "signal=killed") {
		t.Errorf("expected signal=killed in log, got:\n%s", output)
	}
}
