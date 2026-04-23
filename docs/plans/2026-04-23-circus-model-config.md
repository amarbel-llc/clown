# Circus Dynamic Model Resolution Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Let users drop GGUF files into `~/.local/share/circus/models/` and reference them by stem name (without `.gguf`) via `--model`.

**Architecture:** All resolution logic lives in `cmd/circus/daemon.go`. `resolveModel(name string) (string, error)` handles the three cases: absolute path (pass through), name (look up in models dir, fail hard if missing), omitted (use `buildcfg.DefaultModelPath`). A new `circus models` subcommand lists available names. Clown passes `--model` through unchanged — no changes needed in `cmd/clown/main.go`.

**Tech Stack:** Go stdlib (`os`, `path/filepath`). No new dependencies.

**Rollback:** N/A — purely additive. Removing `~/.local/share/circus/models/` restores build-time default behavior.

---

### Task 1: Add `modelsDir` and `resolveModel` to daemon.go

**Promotion criteria:** N/A

**Files:**
- Modify: `cmd/circus/daemon.go`
- Test: `cmd/circus/daemon_test.go` (create)

**Step 1: Write the failing test**

Create `cmd/circus/daemon_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveModel_AbsolutePath(t *testing.T) {
	got, err := resolveModel("/some/absolute/model.gguf", "/unused")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/some/absolute/model.gguf" {
		t.Fatalf("got %q, want %q", got, "/some/absolute/model.gguf")
	}
}

func TestResolveModel_FoundInDir(t *testing.T) {
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "my-model.gguf")
	if err := os.WriteFile(modelPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveModel("my-model", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != modelPath {
		t.Fatalf("got %q, want %q", got, modelPath)
	}
}

func TestResolveModel_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := resolveModel("missing-model", dir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
```

**Step 2: Run to verify it fails**

```
go test ./cmd/circus/... -run TestResolveModel -v
```
Expected: FAIL — `resolveModel` undefined.

**Step 3: Implement `modelsDir` and `resolveModel` in daemon.go**

Add after the existing `logfilePath` function:

```go
func modelsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "circus", "models")
}

// resolveModel resolves a model name or path to an absolute GGUF path.
// An absolute path is returned as-is. A bare name is looked up as
// <dir>/<name>.gguf and fails hard if not found.
func resolveModel(name, dir string) (string, error) {
	if filepath.IsAbs(name) {
		return name, nil
	}
	candidate := filepath.Join(dir, name+".gguf")
	if _, err := os.Stat(candidate); err != nil {
		return "", fmt.Errorf("model %q not found in %s (looked for %s)", name, dir, candidate)
	}
	return candidate, nil
}
```

**Step 4: Run tests**

```
go test ./cmd/circus/... -run TestResolveModel -v
```
Expected: PASS (3 tests).

**Step 5: Commit**

```
git add cmd/circus/daemon.go cmd/circus/daemon_test.go
git commit -m "feat: add resolveModel for dynamic GGUF lookup in models dir"
```

---

### Task 2: Wire resolveModel into startDaemon

**Promotion criteria:** N/A

**Files:**
- Modify: `cmd/circus/daemon.go:87-113` (the `startDaemon` function)

**Step 1: Write the failing test**

Add to `cmd/circus/daemon_test.go`:

```go
func TestStartDaemonResolvesModel(t *testing.T) {
	// resolveModel is already unit-tested; this just checks the integration
	// point: if CIRCUS_MODEL is set to a bare name and models dir has the file,
	// resolveModel returns the full path.
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "test-model.gguf")
	if err := os.WriteFile(modelPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveModel("test-model", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != modelPath {
		t.Fatalf("got %q, want %q", got, modelPath)
	}
}
```

**Step 2: Run to verify it passes already** (it will, since resolveModel is already correct)

```
go test ./cmd/circus/... -run TestStartDaemonResolvesModel -v
```
Expected: PASS.

**Step 3: Update startDaemon to call resolveModel**

Replace the model resolution block in `startDaemon` (currently lines ~107-113 in `cmd/circus/daemon.go`):

```go
	model := os.Getenv("CIRCUS_MODEL")
	if model == "" {
		model = buildcfg.DefaultModelPath
	}
	if model != "" {
		args = append(args, "--model", model)
	}
```

With:

```go
	modelName := os.Getenv("CIRCUS_MODEL")
	if modelName == "" {
		modelName = buildcfg.DefaultModelPath
	}
	if modelName != "" {
		// Bare names (no leading /) are resolved against the models dir.
		if !filepath.IsAbs(modelName) {
			resolved, err := resolveModel(modelName, modelsDir())
			if err != nil {
				return 0, err
			}
			modelName = resolved
		}
		args = append(args, "--model", modelName)
	}
```

**Step 4: Build to verify it compiles**

```
go build ./cmd/circus/...
```
Expected: no errors.

**Step 5: Commit**

```
git add cmd/circus/daemon.go
git commit -m "feat: wire resolveModel into startDaemon"
```

---

### Task 3: Add `circus models` subcommand

**Promotion criteria:** N/A

**Files:**
- Modify: `cmd/circus/main.go`

**Step 1: Write the failing test**

Add to `cmd/circus/daemon_test.go`:

```go
func TestListModels_Empty(t *testing.T) {
	dir := t.TempDir()
	names, err := listModels(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected empty, got %v", names)
	}
}

func TestListModels_SomeFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha.gguf", "beta.gguf", "notes.txt"} {
		os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644)
	}
	names, err := listModels(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %v", names)
	}
	// Should be sorted, extensions stripped
	if names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("unexpected names: %v", names)
	}
}
```

**Step 2: Run to verify it fails**

```
go test ./cmd/circus/... -run TestListModels -v
```
Expected: FAIL — `listModels` undefined.

**Step 3: Implement `listModels` in daemon.go and `models` subcommand in main.go**

Add to `cmd/circus/daemon.go`:

```go
func listModels(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".gguf") {
			names = append(names, strings.TrimSuffix(e.Name(), ".gguf"))
		}
	}
	return names, nil
}
```

Note: add `"strings"` to the import block in `daemon.go` if not already present.

Add `models` case to the switch in `cmd/circus/main.go`:

```go
	case "models":
		return cmdModels()
```

And the function:

```go
func cmdModels() int {
	names, err := listModels(modelsDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: %v\n", err)
		return 1
	}
	for _, name := range names {
		fmt.Println(name)
	}
	return 0
}
```

Also update the usage string in `run`:

```go
	fmt.Fprintln(os.Stderr, "usage: circus <start|stop|status|models> [--model <name-or-path>]")
```

**Step 4: Run tests**

```
go test ./cmd/circus/... -run TestListModels -v
```
Expected: PASS (2 tests).

**Step 5: Build**

```
go build ./cmd/...
```
Expected: no errors.

**Step 6: Commit**

```
git add cmd/circus/daemon.go cmd/circus/main.go cmd/circus/daemon_test.go
git commit -m "feat: add circus models subcommand, listModels helper"
```

---

### Task 4: Manual end-to-end test

**Step 1: Create models dir and symlink a model**

```bash
mkdir -p ~/.local/share/circus/models
ln -s /nix/store/v42a2r2si08givmkhsxrl6bxpffcd3y8-gemma-3-270m-it-Q8_0.gguf \
    ~/.local/share/circus/models/gemma-3-270m-it-Q8_0.gguf
```

**Step 2: List models**

```bash
./result-circus/bin/circus models
```
Expected output: `gemma-3-270m-it-Q8_0`

**Step 3: Start with name**

```bash
./result-circus/bin/circus stop 2>/dev/null || true
./result-circus/bin/circus start --model gemma-3-270m-it-Q8_0
```
Expected: `circus: started llama-server at http://127.0.0.1:8080`

**Step 4: Verify via clown**

```bash
clown --provider circus --model gemma-3-270m-it-Q8_0
```
Expected: Claude Code session backed by local llama-server.

**Step 5: Test missing model error**

```bash
./result-circus/bin/circus start --model nonexistent
```
Expected: `circus: model "nonexistent" not found in ~/.local/share/circus/models (looked for ...)`

**Step 6: Commit nothing** — this is a manual test step only.
