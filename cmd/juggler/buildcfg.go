package main

// LlamaServerPath is the absolute path to the llama-server binary, baked at
// build time via `-ldflags -X` on the juggler-go derivation. The daemon
// (`juggler daemon`) exec's it when StartInstance fires; --llama-server
// overrides it (tests point at the fake fixture). Empty in dev builds
// (go build / go run), which the daemon and the launcher tests treat as
// "not configured" / skip.
//
// This is deliberately juggler-owned rather than clown's internal/buildcfg:
// cmd/juggler + internal/juggler + internal/jugglermodels form a
// self-contained subsystem that imports nothing from clown, so a future
// extraction into a standalone repo is a clean cut (the perforation line).
var LlamaServerPath string

// RingmasterPath, TroupePath, SystemdRunPath, SystemctlPath and MoxyPath are
// the absolute paths of the binaries the FDR 0019 agent-substrate verbs
// consume, baked at build time via `-ldflags -X` on the juggler-go
// derivation (flake.nix). They are the defaults behind
// --ringmaster/--troupe/--systemd-run/--systemctl/--moxy and
// JUGGLER_{RINGMASTER,TROUPE,SYSTEMD_RUN,SYSTEMCTL,MOXY}_BIN, which still
// override them (flag, then env, then the burned-in path, then the bare
// name on PATH). Empty in dev builds. SystemctlPath is also empty on
// non-Linux (systemd has no systemctl there, same as SystemdRunPath).
// MoxyPath is also empty in the standalone nix package (moxy is not an
// input of this flake, so it stays PATH-resolved); mkJuggler sets it
// per-juggler from the consumer's moxy.
var (
	RingmasterPath string
	TroupePath     string
	SystemdRunPath string
	SystemctlPath  string
	MoxyPath       string
)
