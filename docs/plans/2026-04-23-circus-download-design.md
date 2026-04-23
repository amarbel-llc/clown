# circus download Design

**Goal:** Add `circus download <name>` to fetch GGUF models into `~/.local/share/circus/models/` from a baked-in registry with SHA256 validation and a progress bar.

**Architecture:** A new `download.go` file in `cmd/circus` implements the command. The model registry is embedded as `registry.json` via `go:embed`. Downloads stream to a temp file in the same directory, validate SHA256, then atomically rename to the final path. charmbracelet/bubbles provides the progress bar. No new Go dependencies needed — bubbles is already in go.mod.

**Rollback:** Purely additive. Remove by deleting `cmd/circus/download.go` and `cmd/circus/registry.json`. No dual-architecture period needed.

---

## Registry Format

`cmd/circus/registry.json` — embedded in binary at build time:

```json
[
  {
    "name": "qwen3-0.6b",
    "url": "https://huggingface.co/bartowski/Qwen3-0.6B-GGUF/resolve/main/Qwen3-0.6B-Q8_0.gguf",
    "sha256": "<fill in at implementation time>",
    "size": 0,
    "description": "Qwen3 0.6B Q8_0 — fastest, minimal RAM"
  },
  {
    "name": "qwen3-1.7b",
    "url": "https://huggingface.co/bartowski/Qwen3-1.7B-GGUF/resolve/main/Qwen3-1.7B-Q4_K_M.gguf",
    "sha256": "<fill in at implementation time>",
    "size": 0,
    "description": "Qwen3 1.7B Q4_K_M"
  },
  {
    "name": "qwen3-4b",
    "url": "https://huggingface.co/bartowski/Qwen3-4B-GGUF/resolve/main/Qwen3-4B-Q4_K_M.gguf",
    "sha256": "<fill in at implementation time>",
    "size": 0,
    "description": "Qwen3 4B Q4_K_M"
  },
  {
    "name": "gemma3-1b",
    "url": "https://huggingface.co/bartowski/gemma-3-1b-it-GGUF/resolve/main/gemma-3-1b-it-Q8_0.gguf",
    "sha256": "<fill in at implementation time>",
    "size": 0,
    "description": "Gemma3 1B Q8_0"
  },
  {
    "name": "gemma3-4b",
    "url": "https://huggingface.co/bartowski/gemma-3-4b-it-GGUF/resolve/main/gemma-3-4b-it-Q4_K_M.gguf",
    "sha256": "<fill in at implementation time>",
    "size": 0,
    "description": "Gemma3 4B Q4_K_M"
  }
]
```

`sha256` and `size` fields must be filled in from the actual files at implementation time. `size` is used only for progress bar initialization; a value of 0 falls back to indeterminate mode.

---

## CLI Surface

```
circus download <name>          # download named model from registry
circus models                   # list installed models (unchanged)
```

`circus download` with no args prints usage and exits 1.
`circus download --list` is explicitly out of scope (YAGNI) — use `circus models` for installed, and the registry is discoverable via `circus download --help` or documentation.

---

## Download Flow

1. Parse `<name>` arg; look up in embedded registry → error "unknown model %q; available: ..." if not found
2. Compute dest path: `~/.local/share/circus/models/<name>.gguf`
3. If dest already exists: error "model %q already installed at %s" (no `--force` — YAGNI)
4. `os.MkdirAll` the models dir
5. Create temp file in same dir (`os.CreateTemp(modelsDir, name+".*.gguf.tmp")`)
6. Start bubbles progress bar program; stream HTTP GET body through `io.TeeReader` → temp file + progress writer
7. On HTTP error or write error: delete temp, return error
8. On completion: compute SHA256 of temp file; if mismatch delete temp and return error
9. `os.Rename(tmp, dest)` — atomic on same filesystem
10. Print success message: `"circus: downloaded %s to %s\n"`

---

## Progress Bar

Use `github.com/charmbracelet/bubbles/progress` with `progress.WithDefaultGradient()`. If `size > 0` from the registry, show percentage. If `size == 0`, use `progress.WithoutPercentage()` in indeterminate scroll mode.

The bubbles program runs in a goroutine via `tea.NewProgram`. A `ProgressWriter` wraps the download stream, sends `ProgressMsg` updates to the tea program, and sends a `DoneMsg` on completion or error.

---

## Testing

- Unit test registry parsing (embedded JSON loads, all required fields present)
- Unit test SHA256 validation logic (mock temp file)
- Integration test for `cmdDownload` is impractical without network — skip; manual testing via `circus download qwen3-0.6b`
- Existing `daemon_test.go` tests for `resolveModel`/`listModels` are unaffected
