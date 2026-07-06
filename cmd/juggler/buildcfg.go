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
