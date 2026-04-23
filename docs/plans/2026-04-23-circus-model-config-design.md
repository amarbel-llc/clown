# Circus Dynamic Model Resolution Design

**Goal:** Let users experiment with GGUF models by dropping files into `~/.local/share/circus/models/` and referencing them by name, without rebuilding.

**Architecture:** Model resolution lives entirely inside `circus start`. Clown passes `--model <name>` through unchanged; circus resolves it to a path before handing it to llama-server. The build-time burn-in default remains as the zero-config fallback.

**Rollback:** Purely additive. Removing the models directory restores build-time default behavior automatically. No flag needed to revert.

---

## Resolution Rules

`circus start [--model <name>]`

1. `--model` omitted → use `buildcfg.DefaultModelPath` (build-time burn-in), no models dir lookup
2. `--model /absolute/path` → use as-is, no resolution
3. `--model <name>` → check `~/.local/share/circus/models/<name>.gguf`; if found use it; if not found **fail hard** with a clear error message

## New Subcommand

`circus models` — lists available model names (filenames without `.gguf` extension) from `~/.local/share/circus/models/`, one per line. Exits silently if the directory doesn't exist. Used by shell completions.

## Files Changed

- `cmd/circus/daemon.go` — add `resolveModel(name string) (string, error)` and `modelsDir() string`; call from `startDaemon`
- `cmd/circus/main.go` — add `models` subcommand; update `start` flag parsing to pass model name to `startDaemon`

## Out of Scope

- Config file (not needed)
- Default model override via config (build-time burn-in is sufficient)
- Non-GGUF formats
