package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/amarbel-llc/clown/internal/buildcfg"
	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

// TestLauncher_RealLlamaServerNoModel exercises the full launcher
// path against the actual burned-in llama-server binary, without a
// model file on disk.
//
// llama-server, when invoked with --port/--host but no --model,
// enters "router mode": it serves /health (200) but cannot serve
// /v1/messages. We don't care — we only want to prove the
// daemon → launcher → exec → waitHealthy chain works against the
// real binary, with the real OS, not the Go fake from
// launcher_test.go.
//
// What this catches that the Go-fake tests don't:
//   - Burned-in /nix/store/.../llama-server path is reachable from
//     the test process.
//   - The binary launches and survives long enough to bind a port.
//   - The launcher's argv (no --model when Model="") is accepted by
//     real llama-server's argparser.
//
// What it does NOT catch:
//   - /v1/messages on a real model — that needs a GGUF and is left
//     to the developer smoke (justfile `smoke-ringmaster`).
//
// Skipped when LlamaServerPath is empty (dev builds via `go build`
// / `go run`). Skipped pre-Task-19 by checking the smoke flag.
//
// Empirical baseline: on Apple M2 Pro, /health came up in ~350 ms
// after launch. The 20-second context is conservative.
func TestLauncher_RealLlamaServerNoModel(t *testing.T) {
	if buildcfg.LlamaServerPath == "" {
		t.Skip("LlamaServerPath empty — dev build; only the nix-built binary burns this in")
	}
	// Metal shader compilation hangs for 20s+ in the nix sandbox on pre-M5
	// hardware. See https://github.com/amarbel-llc/clown/issues/106.
	if os.Getenv("NIX_BUILD_TOP") != "" {
		t.Skip("skipping real llama-server test in nix sandbox: Metal init hangs on pre-M5 hardware")
	}

	reg := rm.NewRegistry()
	l := newLauncher(buildcfg.LlamaServerPath, reg, t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	in, err := l.Start(ctx, rm.StartInstanceParams{
		Alias: "real-test",
		Model: "", // router mode: no model, /health still comes up
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if in.PID == 0 || in.Port == 0 {
		t.Errorf("expected non-zero pid/port, got %+v", in)
	}
	if got, ok := reg.Get("real-test"); !ok {
		t.Errorf("registry missing real-test after Start")
	} else if got.PID != in.PID {
		t.Errorf("registry PID mismatch: %d vs %d", got.PID, in.PID)
	}

	if err := l.Stop(ctx, "real-test"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, ok := reg.Get("real-test"); ok {
		t.Errorf("registry should be empty after Stop")
	}
}
