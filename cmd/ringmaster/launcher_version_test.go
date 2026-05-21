package main

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/amarbel-llc/clown/internal/buildcfg"
)

// TestLlamaServerHelp is a path-shaped smoke against the burned-in
// buildcfg.LlamaServerPath. It runs `llama-server --help` and
// asserts the binary is reachable, executable, and exits cleanly.
//
// What it catches:
//   - A dangling /nix/store/.../bin/llama-server reference after a
//     nixpkgs-llama input bump where the derivation moved.
//   - An arch mismatch (e.g. linux-builder produced aarch64-linux
//     when the host is x86_64-linux). The exec call fails before any
//     ringmaster code runs.
//   - Missing runtime dylib deps that would only surface when the
//     daemon tried to start an instance.
//
// What it does NOT catch:
//   - Anything about ringmaster's daemon-to-llama-server handshake.
//     The bats e2e (zz-tests_bats/ringmaster.bats) covers that against
//     a Go fake — same protocol, no model.
//   - Whether `/v1/messages` actually works. That requires a real
//     GGUF; the developer smoke (just smoke-ringmaster) exists for
//     local verification.
//
// Skipped when LlamaServerPath is empty (dev builds via `go build` /
// `go run` — the nix derivation is the only thing that injects this).
func TestLlamaServerHelp(t *testing.T) {
	if buildcfg.LlamaServerPath == "" {
		t.Skip("LlamaServerPath empty — dev build; only the nix-built binary burns this in")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, buildcfg.LlamaServerPath, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("exec %s --help: %v\n%s", buildcfg.LlamaServerPath, err, out)
	}
}
