# circus: Local Model Provider — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Add `--provider circus` to clown, backed by a new `circus` binary that manages a long-lived `llama-server` daemon and wires `ANTHROPIC_BASE_URL` into Claude Code via the clown-protocol handshake.

**Architecture:** `circus` is a standalone Go binary (`cmd/circus`) that owns llama-server daemon lifecycle (pidfile, health check, start/stop/status). When launched by clown as a managed child, circus performs the clown-protocol stdout handshake (same wire format as clown-plugin-host) announcing its URL, then stays alive until clown shuts it down. `ANTHROPIC_BASE_URL` points directly at llama-server's native Anthropic Messages API endpoint — no proxy needed. The existing `internal/circus` package (prompt fragment walker) is renamed `internal/promptwalk` before any new code is added.

**Tech Stack:** Go stdlib only — no new dependencies. llama-server speaks the Anthropic Messages API natively. Clown-protocol handshake format: `<core_ver>|<app_ver>|tcp|<host:port>|streamable-http\n` (see `internal/pluginhost/handshake.go`).

**Rollback:** `--provider circus` is opt-in; `--provider claude` (default) is untouched. Stop using `--provider circus` to roll back. No revert needed.

---

### Task 1: Rename `internal/circus` → `internal/promptwalk`

**Promotion criteria:** N/A — purely mechanical rename.

**Files:**
- Rename: `internal/circus/` → `internal/promptwalk/`
- Modify: `internal/circus/walk.go` (package declaration)
- Modify: `internal/circus/walk_test.go` (package declaration)
- Modify: `cmd/clown/main.go:18` (import path)
- Modify: `cmd/clown/main.go` (all `circus.WalkPrompts` calls — search for `circus.`)

**Step 1: Rename the directory**

```bash
mv internal/circus internal/promptwalk
```

**Step 2: Update the package declaration in both files**

In `internal/promptwalk/walk.go`, line 1:
```go
package promptwalk
```

In `internal/promptwalk/walk_test.go`, line 1:
```go
package promptwalk
```

**Step 3: Update the import in `cmd/clown/main.go`**

Replace:
```go
"github.com/amarbel-llc/clown/internal/circus"
```
With:
```go
"github.com/amarbel-llc/clown/internal/promptwalk"
```

Replace all uses of `circus.WalkPrompts` and `circus.PromptResult` with `promptwalk.WalkPrompts` and `promptwalk.PromptResult`.

**Step 4: Build and test**

```bash
go build ./...
go test ./...
```
Expected: clean build, all tests pass.

**Step 5: Commit**

```bash
git add -A
git commit -m "refactor: rename internal/circus to internal/promptwalk"
```

---

### Task 2: `internal/daemon` — pidfile and process lifecycle

**Promotion criteria:** N/A — new package.

**Files:**
- Create: `internal/daemon/pidfile.go`
- Create: `internal/daemon/pidfile_test.go`

**Step 1: Write the failing tests**

Create `internal/daemon/pidfile_test.go`:

```go
package daemon_test

import (
	"os"
	"testing"

	"github.com/amarbel-llc/clown/internal/daemon"
)

func TestWriteAndReadPID(t *testing.T) {
	path := t.TempDir() + "/test.pid"
	if err := daemon.WritePID(path, 12345); err != nil {
		t.Fatal(err)
	}
	pid, err := daemon.ReadPID(path)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 12345 {
		t.Fatalf("want 12345, got %d", pid)
	}
}

func TestReadPIDMissing(t *testing.T) {
	_, err := daemon.ReadPID(t.TempDir() + "/nope.pid")
	if !os.IsNotExist(err) {
		t.Fatalf("want os.IsNotExist, got %v", err)
	}
}

func TestRemovePID(t *testing.T) {
	path := t.TempDir() + "/test.pid"
	_ = daemon.WritePID(path, 1)
	if err := daemon.RemovePID(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("pidfile should be gone")
	}
}

func TestIsRunning(t *testing.T) {
	// Our own PID is definitely running.
	if !daemon.IsRunning(os.Getpid()) {
		t.Fatal("own process should be running")
	}
	// PID 0 is never a user process.
	if daemon.IsRunning(0) {
		t.Fatal("pid 0 should not be running")
	}
}
```

**Step 2: Run tests to verify they fail**

```bash
go test ./internal/daemon/...
```
Expected: compile error — package does not exist yet.

**Step 3: Write minimal implementation**

Create `internal/daemon/pidfile.go`:

```go
package daemon

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func WritePID(path string, pid int) error {
	if err := os.MkdirAll(parentDir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644)
}

func ReadPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("pidfile %s: invalid contents: %w", path, err)
	}
	return pid, nil
}

func RemovePID(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// IsRunning returns true if a process with the given PID exists and is
// reachable via signal 0 (does not actually send a signal).
func IsRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}

func parentDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}
```

**Step 4: Run tests to verify they pass**

```bash
go test ./internal/daemon/...
```
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/daemon/
git commit -m "feat: internal/daemon — pidfile helpers"
```

---

### Task 3: `cmd/circus` — daemon lifecycle commands

**Promotion criteria:** N/A — new binary.

**Files:**
- Create: `cmd/circus/main.go`
- Create: `cmd/circus/daemon.go`

The binary has three subcommands: `start`, `stop`, `status`. For the PoC `start` also performs the clown-protocol handshake when stdout is not a terminal (i.e., when launched by clown).

llama-server is located via `CIRCUS_LLAMA_SERVER` env var or falls back to `llama-server` on PATH. Model is `CIRCUS_MODEL` env var or defaults to the first model llama-server finds.

The clown-protocol handshake line format (from `internal/pluginhost/handshake.go`):
```
1|1|tcp|127.0.0.1:<port>|streamable-http
```
Core version is always `1`. App version is always `1` for the PoC.

llama-server health endpoint: `GET /health` → 200 OK when ready.
llama-server default port: 8080 (can be overridden via `CIRCUS_PORT`).
Pidfile path: `~/.local/state/circus/llama-server.pid`.
Port file path: `~/.local/state/circus/llama-server.port` (written alongside pidfile so `status` can report the URL without re-probing).

**Step 1: Write `cmd/circus/daemon.go`**

```go
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/amarbel-llc/clown/internal/daemon"
)

const (
	healthPath      = "/health"
	healthTimeout   = 60 * time.Second
	healthInterval  = 500 * time.Millisecond
	stopGracePeriod = 5 * time.Second
)

func stateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "circus"), nil
}

func pidfilePath() (string, error) {
	d, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "llama-server.pid"), nil
}

func portfilePath() (string, error) {
	d, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "llama-server.port"), nil
}

// attachOrStart returns the port of a running llama-server, starting one
// if none is alive. spawned is true when this call launched the process.
func attachOrStart() (port int, spawned bool, err error) {
	pidPath, err := pidfilePath()
	if err != nil {
		return 0, false, err
	}
	portPath, err := portfilePath()
	if err != nil {
		return 0, false, err
	}

	if pid, err := daemon.ReadPID(pidPath); err == nil && daemon.IsRunning(pid) {
		if data, err := os.ReadFile(portPath); err == nil {
			if p, err := strconv.Atoi(string(data)); err == nil {
				return p, false, nil
			}
		}
	}

	// Clean up stale pidfile.
	_ = daemon.RemovePID(pidPath)

	port, err = startDaemon(pidPath, portPath)
	if err != nil {
		return 0, false, err
	}
	return port, true, nil
}

func startDaemon(pidPath, portPath string) (int, error) {
	port := 8080
	if v := os.Getenv("CIRCUS_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			port = p
		}
	}

	binary := os.Getenv("CIRCUS_LLAMA_SERVER")
	if binary == "" {
		binary = "llama-server"
	}

	args := []string{
		"--port", strconv.Itoa(port),
		"--host", "127.0.0.1",
	}
	if model := os.Getenv("CIRCUS_MODEL"); model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.Command(binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting llama-server: %w", err)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
	defer cancel()
	if err := waitHealthy(ctx, addr); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return 0, fmt.Errorf("llama-server health check: %w", err)
	}

	if err := daemon.WritePID(pidPath, cmd.Process.Pid); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return 0, err
	}
	if err := os.WriteFile(portPath, []byte(strconv.Itoa(port)), 0o644); err != nil {
		return 0, err
	}

	// Detach: let llama-server outlive this process.
	_ = cmd.Process.Release()

	return port, nil
}

func stopDaemon() error {
	pidPath, err := pidfilePath()
	if err != nil {
		return err
	}
	portPath, err := portfilePath()
	if err != nil {
		return err
	}

	pid, err := daemon.ReadPID(pidPath)
	if os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "circus: llama-server is not running")
		return nil
	}
	if err != nil {
		return err
	}

	if !daemon.IsRunning(pid) {
		fmt.Fprintln(os.Stderr, "circus: llama-server is not running (stale pidfile)")
		_ = daemon.RemovePID(pidPath)
		_ = daemon.RemovePID(portPath)
		return nil
	}

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("sending SIGTERM to %d: %w", pid, err)
	}

	deadline := time.Now().Add(stopGracePeriod)
	for time.Now().Before(deadline) {
		if !daemon.IsRunning(pid) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if daemon.IsRunning(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}

	_ = daemon.RemovePID(pidPath)
	_ = daemon.RemovePID(portPath)
	fmt.Fprintln(os.Stderr, "circus: llama-server stopped")
	return nil
}

func statusDaemon() error {
	pidPath, err := pidfilePath()
	if err != nil {
		return err
	}
	portPath, err := portfilePath()
	if err != nil {
		return err
	}

	pid, err := daemon.ReadPID(pidPath)
	if os.IsNotExist(err) {
		fmt.Println("not running")
		return nil
	}
	if err != nil {
		return err
	}

	if !daemon.IsRunning(pid) {
		fmt.Println("not running (stale pidfile)")
		return nil
	}

	port := 8080
	if data, err := os.ReadFile(portPath); err == nil {
		if p, err := strconv.Atoi(string(data)); err == nil {
			port = p
		}
	}

	fmt.Printf("running  pid=%d  url=http://127.0.0.1:%d\n", pid, port)
	return nil
}

func waitHealthy(ctx context.Context, addr string) error {
	url := "http://" + addr + healthPath
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(healthInterval)
	defer ticker.Stop()
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
```

**Step 2: Write `cmd/circus/main.go`**

```go
package main

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: circus <start|stop|status>")
		return 1
	}

	switch args[0] {
	case "start":
		return cmdStart()
	case "stop":
		if err := stopDaemon(); err != nil {
			fmt.Fprintf(os.Stderr, "circus: %v\n", err)
			return 1
		}
		return 0
	case "status":
		if err := statusDaemon(); err != nil {
			fmt.Fprintf(os.Stderr, "circus: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "circus: unknown command %q\n", args[0])
		return 1
	}
}

func cmdStart() int {
	port, spawned, err := attachOrStart()
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: %v\n", err)
		return 1
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// If stdout is not a terminal, we were launched by clown: emit handshake
	// and block until stdin closes (clown shutting down).
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		// Clown-protocol handshake: 1|1|tcp|<addr>|streamable-http
		fmt.Printf("1|1|tcp|%s|streamable-http\n", addr)
		os.Stdout.Sync()

		// Block until clown closes our stdin.
		buf := make([]byte, 1)
		for {
			_, err := os.Stdin.Read(buf)
			if err != nil {
				break
			}
		}

		if !spawned {
			// Attached to existing daemon — leave it running.
			return 0
		}
		if err := stopDaemon(); err != nil {
			fmt.Fprintf(os.Stderr, "circus: stop on exit: %v\n", err)
		}
		return 0
	}

	// Interactive: just print status.
	action := "attached to existing"
	if spawned {
		action = "started"
	}
	fmt.Printf("circus: %s llama-server at http://%s\n", action, addr)
	return 0
}
```

**Step 3: Build**

```bash
go build ./cmd/circus/...
```
Expected: clean build.

**Step 4: Commit**

```bash
git add cmd/circus/
git commit -m "feat: cmd/circus — daemon lifecycle (start/stop/status)"
```

---

### Task 4: Wire `--provider circus` into clown

**Promotion criteria:** N/A — new provider value; existing providers unchanged.

**Files:**
- Modify: `internal/buildcfg/buildcfg.go` — add `CircusCliPath`
- Modify: `cmd/clown/main.go` — add `"circus"` case to `resolveProvider` and `run`

**Step 1: Add `CircusCliPath` to buildcfg**

In `internal/buildcfg/buildcfg.go`, add:
```go
CircusCliPath string
```

**Step 2: Add `"circus"` to `resolveProvider` in `cmd/clown/main.go`**

In the `resolveProvider` switch (around line 282), add:
```go
case "circus":
    return buildcfg.CircusCliPath, nil
```

**Step 3: Add `runCircus` and wire it in `run`**

In the provider switch in `run` (around line 69), add:
```go
case "circus":
    return runCircus(cliPath, flags, prompts, pluginDirs)
```

Add a new function `runCircus`. It is identical to `runClaude` except it launches the circus binary via the clown-protocol handshake, reads the announced address, sets `ANTHROPIC_BASE_URL` in the environment, then launches Claude Code. The simplest PoC approach: launch circus as a child (like ManagedServer in pluginhost), read the handshake line, set the env var, then `exec` Claude Code with the modified environment.

```go
func runCircus(circusPath string, flags parsedFlags, prompts promptwalk.PromptResult, pluginDirs []string) int {
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    cmd := exec.CommandContext(ctx, circusPath, "start")
    cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
    cmd.Stderr = os.Stderr
    // stdin pipe lets circus detect shutdown (EOF when we close it)
    stdinPipe, err := cmd.StdinPipe()
    if err != nil {
        fmt.Fprintf(os.Stderr, "clown: circus stdin pipe: %v\n", err)
        return 1
    }
    stdoutPipe, err := cmd.StdoutPipe()
    if err != nil {
        fmt.Fprintf(os.Stderr, "clown: circus stdout pipe: %v\n", err)
        return 1
    }

    if err := cmd.Start(); err != nil {
        fmt.Fprintf(os.Stderr, "clown: starting circus: %v\n", err)
        return 1
    }
    defer func() {
        stdinPipe.Close() // signals circus to stop
        cmd.Wait()
    }()

    // Read handshake from circus.
    hs, err := readCircusHandshake(stdoutPipe)
    if err != nil {
        fmt.Fprintf(os.Stderr, "clown: circus handshake: %v\n", err)
        return 1
    }

    baseURL := "http://" + hs.Address

    // Build Claude args and launch.
    claudePath := buildcfg.ClaudeCliPath
    args, cleanup, err := provider.BuildClaudeArgs(provider.ClaudeArgs{
        CLIPath:             claudePath,
        AgentsFile:          buildcfg.AgentsFile,
        DisallowedToolsFile: buildcfg.DisallowedToolsFile,
        SystemPromptFile:    prompts.SystemPromptFile,
        AppendFragments:     prompts.AppendFragments,
    }, flags.forwarded)
    if err != nil {
        fmt.Fprintf(os.Stderr, "clown: building claude args: %v\n", err)
        return 1
    }
    defer cleanup()

    fullArgs := prependPluginDirs(args, pluginDirs, nil)

    binary, err := exec.LookPath(claudePath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "clown: %v\n", err)
        return 1
    }

    claudeCmd := exec.Command(binary, fullArgs...)
    claudeCmd.Stdin = os.Stdin
    claudeCmd.Stdout = os.Stdout
    claudeCmd.Stderr = os.Stderr
    claudeCmd.Env = append(os.Environ(), "ANTHROPIC_BASE_URL="+baseURL)

    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
    go func() {
        sig := <-sigCh
        if claudeCmd.Process != nil {
            claudeCmd.Process.Signal(sig)
        }
    }()

    if err := claudeCmd.Run(); err != nil {
        if exitErr, ok := err.(*exec.ExitError); ok {
            resetTerminal()
            return exitErr.ExitCode()
        }
        fmt.Fprintf(os.Stderr, "clown: %v\n", err)
        return 1
    }
    resetTerminal()
    return 0
}

func readCircusHandshake(r io.Reader) (pluginhost.Handshake, error) {
    scanner := bufio.NewScanner(r)
    if !scanner.Scan() {
        if err := scanner.Err(); err != nil {
            return pluginhost.Handshake{}, err
        }
        return pluginhost.Handshake{}, fmt.Errorf("circus closed stdout before handshake")
    }
    return pluginhost.ParseHandshake(scanner.Text())
}
```

Note: `runCircus` uses `ClaudeCliPath` for the harness — it is always Claude Code in the PoC. `circusPath` is the circus binary path passed in as `cliPath` from `resolveProvider`.

**Step 4: Build**

```bash
go build ./...
```
Expected: clean build.

**Step 5: Commit**

```bash
git add internal/buildcfg/buildcfg.go cmd/clown/main.go
git commit -m "feat: wire --provider circus into clown"
```

---

### Task 5: Wire circus into the Nix flake

**Promotion criteria:** N/A — new output.

**Files:**
- Modify: `flake.nix` — add `circus-go` derivation, `CircusCliPath` ldflag, include in package outputs

**Step 1: Add `circus-go` derivation**

In `flake.nix`, after the `clown-go` derivation (around line 158), add:

```nix
circus-go = buildGoApplication {
  pname = "circus";
  version = clownVersion;
  src = goSrc;
  subPackages = [ "cmd/circus" ];
  modules = ./gomod2nix.toml;
  ldflags = [ "-s" "-w" ];
};
```

**Step 2: Add `CircusCliPath` ldflag to `clown-go`**

In the `clown-go` ldflags list, add:
```nix
"-X github.com/amarbel-llc/clown/internal/buildcfg.CircusCliPath=${circus-go}/bin/circus"
```

Note: `circus-go` is referenced before it is defined in the let block. In Nix, let bindings are mutually recursive, so this is fine.

**Step 3: Add `circus` to the package outputs**

Find where `clown` and `clown-plugin-host` are exposed as outputs (search for `packages.default` or `packages = {`). Add `circus = circus-go;` alongside them.

**Step 4: Build**

```bash
nix build .#circus
```
Expected: `/result/bin/circus` binary.

```bash
nix build .#default
```
Expected: clean build with circus path burned into clown.

**Step 5: Commit**

```bash
git add flake.nix
git commit -m "feat: add circus to Nix flake"
```

---

### Task 6: Update `gomod2nix.toml`

**Promotion criteria:** N/A.

**Files:**
- Modify: `gomod2nix.toml` (if any new Go deps were added — unlikely since stdlib only)

**Step 1: Re-run gomod2nix**

```bash
gomod2nix
```

If `gomod2nix.toml` changed, commit it:

```bash
git add gomod2nix.toml
git commit -m "chore: update gomod2nix"
```

If unchanged, nothing to do.

---

### Task 7: Smoke test

**Promotion criteria:** gemma3:12b runs a full Claude Code session on M2 Pro (16GB).

**Step 1: Verify circus standalone**

With `llama-server` available and `CIRCUS_MODEL` set:
```bash
circus status          # expect: not running
circus start           # expect: started llama-server at http://127.0.0.1:8080
circus status          # expect: running pid=<N> url=http://127.0.0.1:8080
circus stop            # expect: llama-server stopped
circus status          # expect: not running
```

**Step 2: Verify clown integration**

```bash
CLOWN_PROVIDER=circus clown --verbose
```
Expected: clown launches circus, circus announces handshake, clown sets `ANTHROPIC_BASE_URL`, Claude Code starts against local model.

**Step 3: Verify idempotent attach**

Start circus manually (`circus start`), then run `CLOWN_PROVIDER=circus clown`. Clown should attach to the existing daemon without spawning a new one. On exit, the daemon should remain running.

**Step 4: Commit any fixes**

```bash
git add -A
git commit -m "fix: <description of any smoke test fixes>"
```
