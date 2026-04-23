# circus download Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Add `circus download <name>` to fetch GGUF models from a baked-in registry into `~/.local/share/circus/models/` with SHA256 validation and a charmbracelet/bubbles progress bar.

**Architecture:** A new `cmd/circus/download.go` file implements `cmdDownload`. The model registry is `cmd/circus/registry.json` embedded via `go:embed`. Downloads stream to a temp file in the models dir, SHA256-validate, then atomically rename. A bubbles `progress.Model` renders download progress. No new Go dependencies — charmbracelet/bubbles is already in go.mod/go.sum.

**Tech Stack:** Go, `go:embed`, `crypto/sha256`, `charmbracelet/bubbles/progress`, `charmbracelet/bubbletea`

**Rollback:** Purely additive. Delete `cmd/circus/download.go` and `cmd/circus/registry.json` to revert. No existing code changes.

---

### Task 1: Registry JSON + parsing

**Promotion criteria:** N/A

**Files:**
- Create: `cmd/circus/registry.json`
- Create: `cmd/circus/download.go` (registry types + load function only)
- Test: `cmd/circus/download_test.go`

**Step 1: Write the failing test**

Create `cmd/circus/download_test.go`:

```go
package main

import (
	"testing"
)

func TestLoadRegistry_ParsesAllFields(t *testing.T) {
	entries, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one registry entry")
	}
	for _, e := range entries {
		if e.Name == "" {
			t.Errorf("entry has empty name: %+v", e)
		}
		if e.URL == "" {
			t.Errorf("entry %q has empty url", e.Name)
		}
		if e.SHA256 == "" {
			t.Errorf("entry %q has empty sha256", e.Name)
		}
		if e.Description == "" {
			t.Errorf("entry %q has empty description", e.Name)
		}
	}
}

func TestLoadRegistry_ContainsExpectedModels(t *testing.T) {
	entries, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
	}
	for _, want := range []string{"qwen3-0.6b", "qwen3-1.7b", "gemma3-1b"} {
		if !names[want] {
			t.Errorf("expected model %q in registry", want)
		}
	}
}
```

**Step 2: Run tests to verify they fail**

```bash
just test-go
```
Expected: compile error or FAIL — `loadRegistry` not defined.

**Step 3: Create registry.json**

To get the actual SHA256 and size for each model:
1. Download each file manually (or check the HuggingFace model page "Files and versions" tab — each file has a SHA256 shown)
2. For HuggingFace GGUF files from `bartowski`, the SHA256 is listed on the model card under "Files and versions"
3. Alternatively: `curl -L <url> -o /tmp/model.gguf && sha256sum /tmp/model.gguf && stat -f%z /tmp/model.gguf`

URLs follow the pattern: `https://huggingface.co/<owner>/<repo>/resolve/main/<filename>`

Create `cmd/circus/registry.json` with these entries (fill in sha256 and size from HuggingFace):

```json
[
  {
    "name": "qwen3-0.6b",
    "url": "https://huggingface.co/bartowski/Qwen3-0.6B-GGUF/resolve/main/Qwen3-0.6B-Q8_0.gguf",
    "sha256": "FILL_IN",
    "size": 0,
    "description": "Qwen3 0.6B Q8_0 — fastest, minimal RAM (~700MB)"
  },
  {
    "name": "qwen3-1.7b",
    "url": "https://huggingface.co/bartowski/Qwen3-1.7B-GGUF/resolve/main/Qwen3-1.7B-Q4_K_M.gguf",
    "sha256": "FILL_IN",
    "size": 0,
    "description": "Qwen3 1.7B Q4_K_M (~1.1GB)"
  },
  {
    "name": "qwen3-4b",
    "url": "https://huggingface.co/bartowski/Qwen3-4B-GGUF/resolve/main/Qwen3-4B-Q4_K_M.gguf",
    "sha256": "FILL_IN",
    "size": 0,
    "description": "Qwen3 4B Q4_K_M (~2.6GB)"
  },
  {
    "name": "gemma3-1b",
    "url": "https://huggingface.co/bartowski/gemma-3-1b-it-GGUF/resolve/main/gemma-3-1b-it-Q8_0.gguf",
    "sha256": "FILL_IN",
    "size": 0,
    "description": "Gemma3 1B Q8_0 (~1.1GB)"
  },
  {
    "name": "gemma3-4b",
    "url": "https://huggingface.co/bartowski/gemma-3-4b-it-GGUF/resolve/main/gemma-3-4b-it-Q4_K_M.gguf",
    "sha256": "FILL_IN",
    "size": 0,
    "description": "Gemma3 4B Q4_K_M (~2.5GB)"
  }
]
```

**Step 4: Create download.go with registry types and loadRegistry**

Create `cmd/circus/download.go`:

```go
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed registry.json
var registryJSON []byte

type registryEntry struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	Description string `json:"description"`
}

func loadRegistry() ([]registryEntry, error) {
	var entries []registryEntry
	if err := json.Unmarshal(registryJSON, &entries); err != nil {
		return nil, fmt.Errorf("parse registry: %w", err)
	}
	return entries, nil
}

func findInRegistry(name string, entries []registryEntry) (registryEntry, bool) {
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return registryEntry{}, false
}
```

**Step 5: Run tests to verify they pass**

```bash
just test-go
```
Expected: PASS (both TestLoadRegistry_* tests pass).

**Step 6: Commit**

```bash
git add cmd/circus/download.go cmd/circus/download_test.go cmd/circus/registry.json
git commit -m "feat: add circus model registry (go:embed JSON)"
```

---

### Task 2: SHA256 validation helper

**Promotion criteria:** N/A

**Files:**
- Modify: `cmd/circus/download.go`
- Modify: `cmd/circus/download_test.go`

**Step 1: Write the failing test**

Add to `cmd/circus/download_test.go`:

```go
import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifySHA256_Match(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	content := []byte("hello circus")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(content)
	hex := hex.EncodeToString(h[:])
	if err := verifySHA256(path, hex); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifySHA256_Mismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	if err := os.WriteFile(path, []byte("hello circus"), 0644); err != nil {
		t.Fatal(err)
	}
	err := verifySHA256(path, "000000")
	if err == nil {
		t.Fatal("expected error for mismatch")
	}
}
```

**Step 2: Run to verify fail**

```bash
just test-go
```
Expected: compile error — `verifySHA256` not defined.

**Step 3: Implement verifySHA256**

Add to `cmd/circus/download.go`:

```go
import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	// ... existing imports
)

func verifySHA256(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expected {
		return fmt.Errorf("sha256 mismatch: got %s, want %s", got, expected)
	}
	return nil
}
```

**Step 4: Run tests to verify pass**

```bash
just test-go
```
Expected: PASS.

**Step 5: Commit**

```bash
git add cmd/circus/download.go cmd/circus/download_test.go
git commit -m "feat: add SHA256 validation helper for circus download"
```

---

### Task 3: cmdDownload with progress bar

**Promotion criteria:** N/A

**Files:**
- Modify: `cmd/circus/download.go`
- Modify: `cmd/circus/main.go`

**Step 1: No unit test for cmdDownload** — it requires network and the filesystem; manual testing is the gate. Skip to implementation.

**Step 2: Implement the progress bar program**

Add to `cmd/circus/download.go`. The bubbles `progress` component renders a bar; a `progressWriter` feeds bytes-written updates to the tea program via `tea.Program.Send`.

```go
import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
)

type progressMsg float64
type doneMsg struct{ err error }

type progressModel struct {
	bar      progress.Model
	total    int64
	received int64
}

func (m progressModel) Init() tea.Cmd { return nil }

func (m progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case progressMsg:
		m.received = int64(float64(m.total) * float64(msg))
		return m, m.bar.SetPercent(float64(msg))
	case doneMsg:
		return m, tea.Quit
	case progress.FrameMsg:
		updated, cmd := m.bar.Update(msg)
		m.bar = updated.(progress.Model)
		return m, cmd
	}
	return m, nil
}

func (m progressModel) View() string {
	return "\n" + m.bar.View() + "\n"
}

type progressWriter struct {
	total    int64
	written  int64
	program  *tea.Program
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.written += int64(n)
	if pw.total > 0 {
		pct := float64(pw.written) / float64(pw.total)
		pw.program.Send(progressMsg(pct))
	}
	return n, nil
}
```

**Step 3: Implement cmdDownload**

Add to `cmd/circus/download.go`:

```go
func cmdDownload(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: circus download <name>")
		return 1
	}
	name := args[0]

	entries, err := loadRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: %v\n", err)
		return 1
	}
	entry, ok := findInRegistry(name, entries)
	if !ok {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name)
		}
		fmt.Fprintf(os.Stderr, "circus: unknown model %q; available: %v\n", name, names)
		return 1
	}

	dir := modelsDir()
	dest := filepath.Join(dir, name+".gguf")
	if _, err := os.Stat(dest); err == nil {
		fmt.Fprintf(os.Stderr, "circus: model %q already installed at %s\n", name, dest)
		return 1
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "circus: mkdir: %v\n", err)
		return 1
	}

	tmp, err := os.CreateTemp(dir, name+".*.gguf.tmp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: create temp: %v\n", err)
		return 1
	}
	tmpPath := tmp.Name()
	cleanup := func() { os.Remove(tmpPath) }

	resp, err := http.Get(entry.URL)
	if err != nil {
		tmp.Close()
		cleanup()
		fmt.Fprintf(os.Stderr, "circus: download: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		tmp.Close()
		cleanup()
		fmt.Fprintf(os.Stderr, "circus: download: HTTP %d\n", resp.StatusCode)
		return 1
	}

	var bar progress.Model
	if entry.Size > 0 {
		bar = progress.New(progress.WithDefaultGradient())
	} else {
		bar = progress.New(progress.WithDefaultGradient(), progress.WithoutPercentage())
	}
	m := progressModel{bar: bar, total: entry.Size}
	p := tea.NewProgram(m)

	pw := &progressWriter{total: entry.Size, program: p}
	reader := io.TeeReader(resp.Body, pw)

	var writeErr error
	go func() {
		_, writeErr = io.Copy(tmp, reader)
		tmp.Close()
		p.Send(doneMsg{err: writeErr})
	}()

	if _, err := p.Run(); err != nil {
		cleanup()
		fmt.Fprintf(os.Stderr, "circus: progress: %v\n", err)
		return 1
	}
	if writeErr != nil {
		cleanup()
		fmt.Fprintf(os.Stderr, "circus: write: %v\n", writeErr)
		return 1
	}

	if err := verifySHA256(tmpPath, entry.SHA256); err != nil {
		cleanup()
		fmt.Fprintf(os.Stderr, "circus: %v\n", err)
		return 1
	}

	if err := os.Rename(tmpPath, dest); err != nil {
		cleanup()
		fmt.Fprintf(os.Stderr, "circus: rename: %v\n", err)
		return 1
	}

	fmt.Printf("circus: downloaded %s to %s\n", name, dest)
	return 0
}
```

**Step 4: Wire into main.go**

In `cmd/circus/main.go`, add `"download"` to the switch in `run()`:

```go
// In the usage string:
fmt.Fprintln(os.Stderr, "usage: circus <start|stop|status|models|download> [args]")

// In the switch:
case "download":
    return cmdDownload(args[1:])
```

**Step 5: Build to verify compile**

```bash
just build-go
```
Expected: builds with no errors.

**Step 6: Manual smoke test**

```bash
# List available models in registry
./result/bin/circus download nonexistent
# Expected: "circus: unknown model "nonexistent"; available: [qwen3-0.6b qwen3-1.7b ...]"

# Download the smallest model (fill in sha256 in registry.json first)
./result/bin/circus download qwen3-0.6b
# Expected: progress bar appears, file lands at ~/.local/share/circus/models/qwen3-0.6b.gguf

# Verify it's now listed
./result/bin/circus models
# Expected: qwen3-0.6b appears in output

# Verify duplicate download is rejected
./result/bin/circus download qwen3-0.6b
# Expected: "circus: model "qwen3-0.6b" already installed at ..."
```

**Step 7: Commit**

```bash
git add cmd/circus/download.go cmd/circus/main.go
git commit -m "feat: add circus download command with progress bar"
```

---

### Task 4: Full nix build

**Promotion criteria:** N/A

**Files:**
- No changes — just validate the nix build picks up the new embedded file.

**Note:** `registry.json` is a new untracked file. Before running `nix build`, you MUST stage it:

```bash
git add cmd/circus/registry.json
```

(It should already be staged from Task 1 commit, but verify with `git status`.)

**Step 1: Run nix build**

```bash
just build
```
Expected: succeeds. If it fails with "cannot find package" or missing file errors, check `git status` — untracked files are invisible to `nix build`.

**Step 2: Smoke test the nix-built binary**

```bash
./result/bin/circus download nonexistent
# Expected: usage/error message listing known models
```

**Step 3: Commit** (only if any files changed)

If `just build` required a fix, commit the fix. Otherwise no commit needed.

---

## Notes for implementer

- **SHA256 values**: HuggingFace shows SHA256 on each model's "Files and versions" tab. Alternatively download the file and run `sha256sum <file>`. The SHA256 in the registry must match exactly — lowercase hex, no prefix.
- **Size values**: `stat -f%z <file>` on macOS or `stat --format=%s <file>` on Linux. Used only for progress bar initialization; `0` falls back gracefully to indeterminate mode.
- **HuggingFace redirect**: HuggingFace URLs redirect to CDN. `net/http` follows redirects automatically — no special handling needed.
- **`modelsDir()`**: already defined in `cmd/circus/daemon.go` — do not redefine.
- **`go:embed`**: the embed directive must be in the same package as the embedded file. `registry.json` lives in `cmd/circus/` alongside `download.go`.
- **Test file imports**: the test file is in `package main` (same as rest of `cmd/circus`). Existing tests in `daemon_test.go` show the pattern.
