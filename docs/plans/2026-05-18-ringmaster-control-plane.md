# Ringmaster Control Plane — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** Introduce a long-lived `ringmaster` daemon that owns running `llama-server` instances in memory, rewrite `circus` as a JSON-RPC client over a Unix domain socket, and remove the flat-file pid/port state. Implements FDR-0010.

**Architecture:** A new `cmd/ringmaster` binary listens on `~/.local/state/circus/control.sock` and speaks newline-delimited JSON-RPC 2.0. It spawns `llama-server` children with `--alias <name>` and tracks them in an in-memory registry keyed by alias. `cmd/circus` is rewritten as a CLI client of that socket: every subcommand becomes an RPC. A shared `internal/ringmaster` package holds RPC types, framing, and a Go client SDK used by both `circus` and (later) `clown`. Home-manager modules ship a launchd agent (macOS) and a systemd user service (Linux) that keep ringmaster running.

**Tech Stack:** Go stdlib only (`net`, `encoding/json`, `os/exec`, `syscall`). No new third-party dependencies. Home-manager modules use existing patterns from `amarbel-llc/piggy`.

**Rollback:** Revert the merge commit. Pre-ringmaster `circus` (single-instance flat-files) is preserved in git history and can be restored. No production data is at risk — circus is a developer tool.

**Out of scope (deferred):**
- Strict-proxy mode (ringmaster sitting in the HTTP data path).
- Tailnet-exposed control plane.
- Reference-counted instance ownership across multiple clown sessions.
- Migration shim for the old pid/port files (we delete them outright).

---

### Task 1: Create `internal/ringmaster` skeleton with RPC types

**Promotion criteria:** Package compiles and unit tests pass. No callers yet.

**Files:**
- Create: `internal/ringmaster/types.go`
- Create: `internal/ringmaster/types_test.go`

**Step 1: Write a failing test that round-trips an Instance via JSON**

`internal/ringmaster/types_test.go`:

```go
package ringmaster

import (
	"encoding/json"
	"testing"
	"time"
)

func TestInstance_RoundTripJSON(t *testing.T) {
	in := Instance{
		Alias:     "qwen3-coder",
		Model:     "qwen3-coder",
		Port:      43219,
		PID:       91234,
		Bind:      "127.0.0.1",
		StartedAt: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Instance
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", out, in)
	}
}
```

**Step 2: Run the test, expect failure (no Instance type defined)**

```bash
just test-go
```

Expected: `undefined: Instance` in `internal/ringmaster`.

**Step 3: Define the types**

`internal/ringmaster/types.go`:

```go
// Package ringmaster defines the wire types and helpers for the
// ringmaster control plane. Both cmd/ringmaster (the daemon) and
// cmd/circus (the CLI client) depend on this package; clown will
// too in a later plan.
package ringmaster

import "time"

// Instance describes a running llama-server child as ringmaster sees it.
// PID is the child process's PID; ringmaster reaps the child on exit.
type Instance struct {
	Alias     string    `json:"alias"`
	Model     string    `json:"model"`
	Port      int       `json:"port"`
	PID       int       `json:"pid"`
	Bind      string    `json:"bind"`
	StartedAt time.Time `json:"started_at"`
}

// AvailableModel describes a GGUF file on disk that can be loaded.
type AvailableModel struct {
	Name string `json:"name"` // bare name without .gguf, e.g. "qwen3-coder"
	Path string `json:"path"` // absolute path to the file
	Size int64  `json:"size"` // bytes
}
```

**Step 4: Run tests, expect pass**

```bash
just test-go
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/ringmaster/
git commit -m "feat(ringmaster): types skeleton with Instance and AvailableModel"
```

---

### Task 2: Define JSON-RPC request/response shapes

**Promotion criteria:** Package compiles; round-trip tests cover every method.

**Files:**
- Modify: `internal/ringmaster/types.go`
- Create: `internal/ringmaster/rpc.go`
- Create: `internal/ringmaster/rpc_test.go`

**Step 1: Write a failing test for the StartInstance request shape**

`internal/ringmaster/rpc_test.go`:

```go
package ringmaster

import (
	"encoding/json"
	"testing"
)

func TestStartInstanceParams_JSON(t *testing.T) {
	p := StartInstanceParams{
		Alias: "coder-32k",
		Model: "qwen3-coder",
		Bind:  "127.0.0.1",
		Args:  []string{"--ctx-size", "32768"},
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"alias":"coder-32k","model":"qwen3-coder","bind":"127.0.0.1","args":["--ctx-size","32768"]}`
	if string(data) != want {
		t.Errorf("got  %s\nwant %s", data, want)
	}
}
```

**Step 2: Run test, expect failure**

`just test-go` — undefined symbol.

**Step 3: Write rpc.go**

`internal/ringmaster/rpc.go`:

```go
package ringmaster

// Method names. Exported as constants so tests, server, and client
// agree without string typos.
const (
	MethodStartInstance       = "StartInstance"
	MethodStopInstance        = "StopInstance"
	MethodStopAll             = "StopAll"
	MethodListInstances       = "ListInstances"
	MethodGetInstance         = "GetInstance"
	MethodListAvailableModels = "ListAvailableModels"
	MethodDownloadModel       = "DownloadModel"
)

// StartInstanceParams launches a new llama-server child. Alias is the
// registry key; Model resolves to a GGUF file in the models dir (or an
// absolute path). Bind defaults to "127.0.0.1" if empty. Args are
// passed through to llama-server.
type StartInstanceParams struct {
	Alias string   `json:"alias"`
	Model string   `json:"model"`
	Bind  string   `json:"bind,omitempty"`
	Args  []string `json:"args,omitempty"`
}
type StartInstanceResult struct {
	Instance Instance `json:"instance"`
}

// StopInstanceParams stops by alias. Returns no result on success.
type StopInstanceParams struct {
	Alias string `json:"alias"`
}

// StopAllParams is empty by convention (no fields). Returns the
// aliases that were stopped.
type StopAllParams struct{}
type StopAllResult struct {
	Stopped []string `json:"stopped"`
}

// ListInstancesParams is empty.
type ListInstancesParams struct{}
type ListInstancesResult struct {
	Instances []Instance `json:"instances"`
}

// GetInstanceParams looks up a single instance by alias.
type GetInstanceParams struct {
	Alias string `json:"alias"`
}
type GetInstanceResult struct {
	Instance Instance `json:"instance"`
}

// ListAvailableModelsParams is empty.
type ListAvailableModelsParams struct{}
type ListAvailableModelsResult struct {
	Models []AvailableModel `json:"models"`
}

// DownloadModelParams identifies a model by registry name.
type DownloadModelParams struct {
	Name string `json:"name"`
}
type DownloadModelResult struct {
	Model AvailableModel `json:"model"`
}
```

**Step 4: Add round-trip tests for every result type**

Extend `rpc_test.go` with one test per shape (StopAllResult, ListInstancesResult, etc.) that round-trips through JSON and asserts a stable serialization.

**Step 5: Run tests, expect pass**

```bash
just test-go
```

**Step 6: Commit**

```bash
git add internal/ringmaster/
git commit -m "feat(ringmaster): RPC request/response shapes"
```

---

### Task 3: Newline-delimited JSON-RPC framing

**Promotion criteria:** A Frame/Unframe pair round-trips a JSON-RPC envelope.

**Files:**
- Create: `internal/ringmaster/frame.go`
- Create: `internal/ringmaster/frame_test.go`

**Step 1: Failing test**

`internal/ringmaster/frame_test.go`:

```go
package ringmaster

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFrame_RoundTrip(t *testing.T) {
	env := Envelope{JSONRPC: "2.0", ID: 1, Method: "Ping"}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, env); err != nil {
		t.Fatal(err)
	}
	// Frame must end with exactly one newline.
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		t.Fatalf("frame missing trailing newline: %q", buf.String())
	}
	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "Ping" || got.ID != json.Number("1") {
		t.Errorf("got %+v", got)
	}
}
```

(Note: ID is `json.Number` because JSON-RPC 2.0 permits string or number IDs; we keep them opaque on the wire.)

**Step 2: Run, expect failure**

**Step 3: Implementation**

`internal/ringmaster/frame.go`:

```go
package ringmaster

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// Envelope is the JSON-RPC 2.0 message shape. Either Method (request)
// or Result/Error (response) is populated. ID is opaque to satisfy
// the JSON-RPC 2.0 ID rules (string | number | null).
type Envelope struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.Number      `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
}

type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// WriteFrame writes a single JSON-RPC envelope followed by a newline.
func WriteFrame(w io.Writer, env Envelope) error {
	if env.JSONRPC == "" {
		env.JSONRPC = "2.0"
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

// ReadFrame reads one newline-terminated JSON envelope from r.
// Callers wrap r in a bufio.Reader if they want buffering across calls.
func ReadFrame(r io.Reader) (Envelope, error) {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	line, err := br.ReadBytes('\n')
	if err != nil {
		return Envelope{}, fmt.Errorf("read frame: %w", err)
	}
	var env Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return Envelope{}, fmt.Errorf("decode frame: %w", err)
	}
	return env, nil
}
```

**Step 4: Tests pass**

**Step 5: Commit**

```bash
git add internal/ringmaster/
git commit -m "feat(ringmaster): newline-delimited JSON-RPC framing"
```

---

### Task 4: Client SDK (`internal/ringmaster.Client`)

**Promotion criteria:** Client can be constructed from a socket path and exposes one Go method per RPC. Round-trips with a fake server in-process.

**Files:**
- Create: `internal/ringmaster/client.go`
- Create: `internal/ringmaster/client_test.go`

**Step 1: Failing test using a Unix socket pair**

`internal/ringmaster/client_test.go`:

```go
package ringmaster

import (
	"context"
	"net"
	"path/filepath"
	"testing"
)

func TestClient_ListInstances_Empty(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Fake server: accept one connection, reply with empty list.
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		req, _ := ReadFrame(conn)
		WriteFrame(conn, Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  []byte(`{"instances":[]}`),
		})
	}()

	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	res, err := cli.ListInstances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Instances) != 0 {
		t.Errorf("expected empty, got %+v", res.Instances)
	}
}
```

**Step 2: Run, expect failure**

**Step 3: Implementation**

`internal/ringmaster/client.go`:

```go
package ringmaster

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
)

// Client is a thread-safe JSON-RPC 2.0 client over a Unix domain
// socket. One Client owns one connection; concurrent calls share the
// connection and serialize requests behind a mutex. For high-fanout
// use cases, callers can construct multiple Clients.
type Client struct {
	conn   net.Conn
	br     *bufio.Reader
	mu     sync.Mutex
	nextID atomic.Int64
}

func NewClient(socketPath string) (*Client, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", socketPath, err)
	}
	return &Client{conn: conn, br: bufio.NewReader(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := json.Number(fmt.Sprintf("%d", c.nextID.Add(1)))

	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal params: %w", err)
		}
		rawParams = b
	}

	if err := WriteFrame(c.conn, Envelope{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  rawParams,
	}); err != nil {
		return err
	}

	env, err := ReadFrame(c.br)
	if err != nil {
		return err
	}
	if env.Error != nil {
		return fmt.Errorf("rpc %s: %s (code %d)", method, env.Error.Message, env.Error.Code)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(env.Result, result); err != nil {
		return fmt.Errorf("unmarshal result: %w", err)
	}
	return nil
}

// Generated-style wrappers — one per method. Hand-written for v1.

func (c *Client) StartInstance(ctx context.Context, p StartInstanceParams) (StartInstanceResult, error) {
	var r StartInstanceResult
	return r, c.call(ctx, MethodStartInstance, p, &r)
}
func (c *Client) StopInstance(ctx context.Context, p StopInstanceParams) error {
	return c.call(ctx, MethodStopInstance, p, nil)
}
func (c *Client) StopAll(ctx context.Context) (StopAllResult, error) {
	var r StopAllResult
	return r, c.call(ctx, MethodStopAll, StopAllParams{}, &r)
}
func (c *Client) ListInstances(ctx context.Context) (ListInstancesResult, error) {
	var r ListInstancesResult
	return r, c.call(ctx, MethodListInstances, ListInstancesParams{}, &r)
}
func (c *Client) GetInstance(ctx context.Context, p GetInstanceParams) (GetInstanceResult, error) {
	var r GetInstanceResult
	return r, c.call(ctx, MethodGetInstance, p, &r)
}
func (c *Client) ListAvailableModels(ctx context.Context) (ListAvailableModelsResult, error) {
	var r ListAvailableModelsResult
	return r, c.call(ctx, MethodListAvailableModels, ListAvailableModelsParams{}, &r)
}
func (c *Client) DownloadModel(ctx context.Context, p DownloadModelParams) (DownloadModelResult, error) {
	var r DownloadModelResult
	return r, c.call(ctx, MethodDownloadModel, p, &r)
}
```

**Step 4: Tests pass**

**Step 5: Commit**

```bash
git add internal/ringmaster/
git commit -m "feat(ringmaster): Client SDK over UDS"
```

---

### Task 5: Socket-path helper

**Promotion criteria:** A single function returns the canonical control-socket path and respects the `RINGMASTER_SOCKET` env override.

**Files:**
- Create: `internal/ringmaster/paths.go`
- Create: `internal/ringmaster/paths_test.go`

**Step 1: Test**

`internal/ringmaster/paths_test.go`:

```go
package ringmaster

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSocketPath_Default(t *testing.T) {
	t.Setenv("RINGMASTER_SOCKET", "")
	t.Setenv("HOME", "/tmp/ringmaster-test")
	got, err := SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp/ringmaster-test", ".local", "state", "circus", "control.sock")
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestSocketPath_EnvOverride(t *testing.T) {
	t.Setenv("RINGMASTER_SOCKET", "/tmp/x.sock")
	got, err := SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/x.sock" {
		t.Errorf("got %q", got)
	}
}

func TestLogPath_XDGLogHome(t *testing.T) {
	t.Setenv("XDG_LOG_HOME", "/tmp/log")
	got, err := LogPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/log/ringmaster.log" {
		t.Errorf("got %q", got)
	}
	// fallback
	t.Setenv("XDG_LOG_HOME", "")
	t.Setenv("HOME", "/tmp/h")
	got, err = LogPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/h/.local/log/ringmaster.log" {
		t.Errorf("got %q", got)
	}
	_ = os.Setenv // silence unused-import linter
}
```

**Step 2: Run, expect failure**

**Step 3: Implementation**

`internal/ringmaster/paths.go`:

```go
package ringmaster

import (
	"fmt"
	"os"
	"path/filepath"
)

// SocketPath returns the canonical control-socket location. The
// RINGMASTER_SOCKET env var overrides it (useful for tests and
// non-default deployments).
func SocketPath() (string, error) {
	if v := os.Getenv("RINGMASTER_SOCKET"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".local", "state", "circus", "control.sock"), nil
}

// LogPath returns ringmaster's log file location. Respects
// XDG_LOG_HOME if set (the eng convention; see ~/eng/home/xdg.nix),
// else falls back to $HOME/.local/log.
func LogPath() (string, error) {
	if v := os.Getenv("XDG_LOG_HOME"); v != "" {
		return filepath.Join(v, "ringmaster.log"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".local", "log", "ringmaster.log"), nil
}
```

**Step 4: Tests pass**

**Step 5: Commit**

```bash
git add internal/ringmaster/
git commit -m "feat(ringmaster): SocketPath and LogPath helpers"
```

---

### Task 6: In-memory registry

**Promotion criteria:** Registry supports add/remove/list/get with concurrent safety and rejects duplicate aliases.

**Files:**
- Create: `internal/ringmaster/registry.go`
- Create: `internal/ringmaster/registry_test.go`

**Step 1: Tests**

```go
package ringmaster

import (
	"testing"
	"time"
)

func TestRegistry_AddAndGet(t *testing.T) {
	r := NewRegistry()
	in := Instance{Alias: "a", Model: "m", Port: 1, PID: 2, StartedAt: time.Now()}
	if err := r.Add(in); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Get("a")
	if !ok || got.Alias != "a" {
		t.Errorf("got=%+v ok=%v", got, ok)
	}
}

func TestRegistry_DuplicateAlias(t *testing.T) {
	r := NewRegistry()
	_ = r.Add(Instance{Alias: "a"})
	err := r.Add(Instance{Alias: "a"})
	if err == nil {
		t.Fatal("expected duplicate-alias error")
	}
}

func TestRegistry_RemoveAndList(t *testing.T) {
	r := NewRegistry()
	_ = r.Add(Instance{Alias: "a", Port: 1})
	_ = r.Add(Instance{Alias: "b", Port: 2})
	if got := len(r.List()); got != 2 {
		t.Errorf("len=%d", got)
	}
	r.Remove("a")
	if got := len(r.List()); got != 1 {
		t.Errorf("after remove len=%d", got)
	}
}
```

**Step 2: Run, expect failure**

**Step 3: Implementation**

`internal/ringmaster/registry.go`:

```go
package ringmaster

import (
	"fmt"
	"sort"
	"sync"
)

// Registry tracks the running llama-server instances. All methods are
// safe for concurrent use. The registry is purely in-memory; there is
// no on-disk persistence.
type Registry struct {
	mu        sync.RWMutex
	instances map[string]Instance
}

func NewRegistry() *Registry {
	return &Registry{instances: make(map[string]Instance)}
}

func (r *Registry) Add(in Instance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.instances[in.Alias]; ok {
		return fmt.Errorf("alias %q already registered", in.Alias)
	}
	r.instances[in.Alias] = in
	return nil
}

func (r *Registry) Remove(alias string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.instances, alias)
}

func (r *Registry) Get(alias string) (Instance, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	in, ok := r.instances[alias]
	return in, ok
}

func (r *Registry) List() []Instance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Instance, 0, len(r.instances))
	for _, in := range r.instances {
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out
}
```

**Step 4: Tests pass**

**Step 5: Commit**

```bash
git add internal/ringmaster/
git commit -m "feat(ringmaster): in-memory registry"
```

---

### Task 7: `cmd/ringmaster` daemon skeleton

**Promotion criteria:** `ringmaster daemon --socket <path>` listens on the socket, accepts a connection, replies with an "unknown method" error to a stub request, exits cleanly on SIGTERM.

**Files:**
- Create: `cmd/ringmaster/main.go`
- Create: `cmd/ringmaster/server.go`
- Create: `cmd/ringmaster/server_test.go`

**Step 1: Failing integration test**

`cmd/ringmaster/server_test.go`:

```go
package main

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

func TestServer_ListInstances_Empty(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(rm.NewRegistry(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go srv.Serve(ctx, ln)

	cli, err := rm.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	res, err := cli.ListInstances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Instances) != 0 {
		t.Errorf("expected empty, got %+v", res.Instances)
	}
}
```

**Step 2: Run, expect failure**

**Step 3: Implementation: `cmd/ringmaster/server.go`**

```go
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"

	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

// server is the ringmaster daemon's RPC dispatcher. It owns the
// registry and (later) the llama-server launcher.
type server struct {
	registry *rm.Registry
	launcher Launcher // nil-safe; methods check before use
	log      *slog.Logger
}

// Launcher abstracts how new llama-server instances are spawned. The
// real implementation calls exec.Command; tests pass a fake.
type Launcher interface {
	Start(ctx context.Context, p rm.StartInstanceParams) (rm.Instance, error)
	Stop(ctx context.Context, alias string) error
}

func newServer(reg *rm.Registry, l Launcher) *server {
	return &server{
		registry: reg,
		launcher: l,
		log:      slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
}

// Serve accepts connections until ctx is cancelled. Each connection is
// handled in its own goroutine. Errors on individual connections are
// logged, not returned.
func (s *server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.log.Error("accept", "err", err)
			continue
		}
		go s.handle(conn)
	}
}

func (s *server) handle(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	for {
		env, err := rm.ReadFrame(br)
		if err != nil {
			return
		}
		resp := s.dispatch(env)
		if err := rm.WriteFrame(conn, resp); err != nil {
			s.log.Error("write frame", "err", err)
			return
		}
	}
}

func (s *server) dispatch(req rm.Envelope) rm.Envelope {
	switch req.Method {
	case rm.MethodListInstances:
		out := rm.ListInstancesResult{Instances: s.registry.List()}
		data, _ := json.Marshal(out)
		return rm.Envelope{JSONRPC: "2.0", ID: req.ID, Result: data}
	default:
		return rm.Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &rm.Error{
				Code:    -32601,
				Message: fmt.Sprintf("method not found: %s", req.Method),
			},
		}
	}
}
```

**Step 4: Implementation: `cmd/ringmaster/main.go`**

```go
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) < 1 || args[0] != "daemon" {
		fmt.Fprintln(os.Stderr, "usage: ringmaster daemon [--socket PATH]")
		return 2
	}

	socket := ""
	for i := 1; i < len(args); i++ {
		if args[i] == "--socket" && i+1 < len(args) {
			socket = args[i+1]
			i++
		}
	}
	if socket == "" {
		var err error
		socket, err = rm.SocketPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "ringmaster:", err)
			return 1
		}
	}

	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "ringmaster:", err)
		return 1
	}
	// Stale socket cleanup. If a previous daemon crashed, the file
	// remains; net.Listen("unix") refuses to bind over it.
	_ = os.Remove(socket)

	ln, err := net.Listen("unix", socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ringmaster: listen:", err)
		return 1
	}
	defer os.Remove(socket)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-sigCh; cancel() }()

	srv := newServer(rm.NewRegistry(), nil)
	fmt.Fprintln(os.Stderr, "ringmaster: listening on", socket)
	if err := srv.Serve(ctx, ln); err != nil {
		fmt.Fprintln(os.Stderr, "ringmaster:", err)
		return 1
	}
	return 0
}
```

**Step 5: Tests pass**

```bash
just test-go
```

**Step 6: Build**

```bash
just build-go
ls cmd/ringmaster/ringmaster   # produced by go build
```

**Step 7: Commit**

```bash
git add cmd/ringmaster/
git commit -m "feat(ringmaster): daemon skeleton with ListInstances RPC"
```

---

### Task 8: `Launcher` implementation backing `StartInstance` / `StopInstance`

**Promotion criteria:** A `Launcher` can spawn a real `llama-server`, wait for it to become healthy, register it, and stop it. Tested with a `fake-llama-server` shell-script fixture that listens on the right port.

**Files:**
- Create: `cmd/ringmaster/launcher.go`
- Create: `cmd/ringmaster/launcher_test.go`
- Create: `cmd/ringmaster/testdata/fake-llama-server.sh` (executable)

**Step 1: Write the fake llama-server fixture**

`cmd/ringmaster/testdata/fake-llama-server.sh`:

```sh
#!/usr/bin/env bash
# Minimal stand-in for llama-server: parses --port, --alias, binds a
# trivial HTTP server that returns 200 on /health and /v1/models with
# the alias echoed back.

set -euo pipefail

port=0
alias=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --port)  port="$2"; shift 2 ;;
    --alias) alias="$2"; shift 2 ;;
    *)       shift ;;
  esac
done

# Use a tiny Python one-liner — every macOS/Linux dev box has it.
python3 - <<PY
import http.server, json, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self.send_response(200); self.end_headers(); self.wfile.write(b"ok"); return
        if self.path == "/v1/models":
            body = json.dumps({"object":"list","data":[{"id":"$alias","object":"model"}]}).encode()
            self.send_response(200); self.send_header("content-type","application/json"); self.end_headers()
            self.wfile.write(body); return
        self.send_response(404); self.end_headers()
    def log_message(self, *a, **k): pass
srv = http.server.HTTPServer(("127.0.0.1", $port), H)
srv.serve_forever()
PY
```

```bash
chmod +x cmd/ringmaster/testdata/fake-llama-server.sh
```

**Step 2: Failing test**

`cmd/ringmaster/launcher_test.go`:

```go
package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

func TestLauncher_StartAndStop(t *testing.T) {
	bin, _ := filepath.Abs("testdata/fake-llama-server.sh")
	reg := rm.NewRegistry()
	l := newLauncher(bin, reg, t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	in, err := l.Start(ctx, rm.StartInstanceParams{
		Alias: "test",
		Model: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if in.Port == 0 || in.PID == 0 {
		t.Errorf("expected non-zero port/pid: %+v", in)
	}

	if err := l.Stop(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("test"); ok {
		t.Errorf("registry should be empty after Stop")
	}
}
```

**Step 3: Run, expect failure**

**Step 4: Implementation**

`cmd/ringmaster/launcher.go`:

```go
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

const (
	healthTimeout  = 60 * time.Second
	healthInterval = 250 * time.Millisecond
	stopGrace      = 5 * time.Second
)

type llauncher struct {
	llamaServerPath string
	reg             *rm.Registry
	modelsDir       string

	mu       sync.Mutex
	children map[string]*exec.Cmd // alias → process handle
}

func newLauncher(binary string, reg *rm.Registry, modelsDir string) *llauncher {
	return &llauncher{
		llamaServerPath: binary,
		reg:             reg,
		modelsDir:       modelsDir,
		children:        make(map[string]*exec.Cmd),
	}
}

func (l *llauncher) Start(ctx context.Context, p rm.StartInstanceParams) (rm.Instance, error) {
	bind := p.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}

	// Pick a free port by binding :0 and immediately closing.
	port, err := pickFreePort(bind)
	if err != nil {
		return rm.Instance{}, fmt.Errorf("pick port: %w", err)
	}

	modelPath := p.Model
	if !filepath.IsAbs(modelPath) {
		modelPath = filepath.Join(l.modelsDir, p.Model+".gguf")
	}

	args := []string{
		"--port", strconv.Itoa(port),
		"--host", bind,
		"--alias", p.Alias,
	}
	if modelPath != "" {
		args = append(args, "--model", modelPath)
	}
	args = append(args, p.Args...)

	cmd := exec.Command(l.llamaServerPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return rm.Instance{}, fmt.Errorf("start llama-server: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", bind, port)
	hctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	if err := waitHealthy(hctx, addr); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return rm.Instance{}, err
	}

	in := rm.Instance{
		Alias:     p.Alias,
		Model:     p.Model,
		Port:      port,
		PID:       cmd.Process.Pid,
		Bind:      bind,
		StartedAt: time.Now().UTC(),
	}
	if err := l.reg.Add(in); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return rm.Instance{}, err
	}

	l.mu.Lock()
	l.children[p.Alias] = cmd
	l.mu.Unlock()
	return in, nil
}

func (l *llauncher) Stop(ctx context.Context, alias string) error {
	l.mu.Lock()
	cmd, ok := l.children[alias]
	delete(l.children, alias)
	l.mu.Unlock()
	if !ok {
		return fmt.Errorf("alias %q not running", alias)
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		return err
	}
	deadline := time.Now().Add(stopGrace)
	for time.Now().Before(deadline) {
		if cmd.ProcessState != nil {
			break
		}
		_, err := cmd.Process.Wait()
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if cmd.ProcessState == nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	l.reg.Remove(alias)
	return nil
}

func pickFreePort(host string) (int, error) {
	ln, err := net.Listen("tcp", host+":0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func waitHealthy(ctx context.Context, addr string) error {
	url := "http://" + addr + "/health"
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("health timeout: %w", ctx.Err())
		case <-time.After(healthInterval):
		}
	}
}
```

(Add `import "sync"` to the imports list.)

**Step 5: Tests pass**

```bash
just test-go
```

**Step 6: Commit**

```bash
git add cmd/ringmaster/
git commit -m "feat(ringmaster): Launcher spawns and reaps llama-server children"
```

---

### Task 9: Wire `StartInstance`/`StopInstance`/`GetInstance` RPCs into the server

**Promotion criteria:** Client SDK methods produce the right registry state when called against the real server. Integration test covers start → list → stop → list.

**Files:**
- Modify: `cmd/ringmaster/server.go` — extend `dispatch`
- Modify: `cmd/ringmaster/server_test.go` — add integration test

**Step 1: Add a failing integration test**

```go
func TestServer_StartListStop(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "control.sock")
	ln, _ := net.Listen("unix", sock)
	bin, _ := filepath.Abs("testdata/fake-llama-server.sh")
	reg := rm.NewRegistry()
	l := newLauncher(bin, reg, t.TempDir())
	srv := newServer(reg, l)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go srv.Serve(ctx, ln)

	cli, _ := rm.NewClient(sock)
	defer cli.Close()

	if _, err := cli.StartInstance(ctx, rm.StartInstanceParams{
		Alias: "a", Model: "a",
	}); err != nil {
		t.Fatal(err)
	}

	list, _ := cli.ListInstances(ctx)
	if len(list.Instances) != 1 || list.Instances[0].Alias != "a" {
		t.Errorf("list=%+v", list)
	}

	if err := cli.StopInstance(ctx, rm.StopInstanceParams{Alias: "a"}); err != nil {
		t.Fatal(err)
	}
	list2, _ := cli.ListInstances(ctx)
	if len(list2.Instances) != 0 {
		t.Errorf("after stop list=%+v", list2)
	}
}
```

**Step 2: Run, expect failure (method not found)**

**Step 3: Extend dispatch**

```go
func (s *server) dispatch(req rm.Envelope) rm.Envelope {
	switch req.Method {
	case rm.MethodListInstances:
		// ... (existing)
	case rm.MethodStartInstance:
		var p rm.StartInstanceParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcError(req.ID, -32602, err.Error())
		}
		in, err := s.launcher.Start(context.Background(), p)
		if err != nil {
			return rpcError(req.ID, -32000, err.Error())
		}
		data, _ := json.Marshal(rm.StartInstanceResult{Instance: in})
		return rm.Envelope{JSONRPC: "2.0", ID: req.ID, Result: data}
	case rm.MethodStopInstance:
		var p rm.StopInstanceParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcError(req.ID, -32602, err.Error())
		}
		if err := s.launcher.Stop(context.Background(), p.Alias); err != nil {
			return rpcError(req.ID, -32000, err.Error())
		}
		return rm.Envelope{JSONRPC: "2.0", ID: req.ID, Result: []byte("null")}
	case rm.MethodGetInstance:
		var p rm.GetInstanceParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcError(req.ID, -32602, err.Error())
		}
		in, ok := s.registry.Get(p.Alias)
		if !ok {
			return rpcError(req.ID, -32001, fmt.Sprintf("alias %q not found", p.Alias))
		}
		data, _ := json.Marshal(rm.GetInstanceResult{Instance: in})
		return rm.Envelope{JSONRPC: "2.0", ID: req.ID, Result: data}
	default:
		return rpcError(req.ID, -32601, "method not found: "+req.Method)
	}
}

func rpcError(id json.Number, code int, msg string) rm.Envelope {
	return rm.Envelope{JSONRPC: "2.0", ID: id, Error: &rm.Error{Code: code, Message: msg}}
}
```

**Step 4: Tests pass**

**Step 5: Commit**

```bash
git add cmd/ringmaster/
git commit -m "feat(ringmaster): wire StartInstance/StopInstance/GetInstance"
```

---

### Task 10: `StopAll` and `ListAvailableModels` RPCs

**Promotion criteria:** Both methods covered by tests.

**Files:**
- Modify: `cmd/ringmaster/server.go`
- Create: `cmd/ringmaster/models.go` (models-dir scanner)
- Modify: `cmd/ringmaster/server_test.go`

Add `models.go` that reads `~/.local/share/circus/models/*.gguf` (delegate to existing `internal/circusmodels.Dir()`) and returns `[]rm.AvailableModel`. Wire into `dispatch`. Add `MethodStopAll` that iterates the registry and calls Stop. Tests cover both with the fake server.

(Steps follow the same TDD pattern. Omitted for brevity — same shape as Task 9.)

**Commit:**

```bash
git commit -m "feat(ringmaster): StopAll and ListAvailableModels RPCs"
```

---

### Task 11: `DownloadModel` RPC

**Promotion criteria:** Delegates to the existing `internal/circusmodels` download path; client receives final `AvailableModel` on completion.

**Files:**
- Modify: `cmd/ringmaster/server.go`
- Reuse: `internal/circusmodels` for the actual download
- Modify: `cmd/ringmaster/server_test.go`

Wire `MethodDownloadModel` to call into `circusmodels.Download(ctx, name)` (extract a function from `cmd/circus/download.go` into `internal/circusmodels` if not already there). For v1 the RPC is synchronous — the client blocks until the download completes and gets the final `AvailableModel`. No progress events on the wire; that's a future enhancement.

**Commit:**

```bash
git commit -m "feat(ringmaster): DownloadModel RPC"
```

---

### Task 12: Reap dead llama-server children

**Promotion criteria:** When a llama-server child dies unexpectedly, ringmaster removes it from the registry within one health-poll tick. Verified by killing the fake child mid-test.

**Files:**
- Modify: `cmd/ringmaster/launcher.go` — spawn a goroutine per child that `cmd.Wait()`s and calls `reg.Remove` on exit
- Create: `cmd/ringmaster/launcher_reap_test.go`

Test: start a fake child, `kill -9` its pid, sleep, assert registry is empty.

**Commit:**

```bash
git commit -m "feat(ringmaster): reap dead children, remove from registry"
```

---

### Task 13: Rewrite `cmd/circus` as a UDS client (staged)

**Promotion criteria (overall):** All existing `circus` subcommands (`start`, `stop`, `status`, `list`, `models`, `download`) work against a running ringmaster. End-to-end test: spawn ringmaster, run `circus start a`, `circus list`, `circus stop a`. `cmd/circus/daemon.go` is deleted.

**Rationale for staging:** The original plan called for one big rewrite commit. During execution we shifted to finer-grained steps so each subcommand migration can be reviewed and simplified in isolation, catching bugs at the boundary between cmd/circus and ringmaster early. The original Task 14 (`dialClient`) is also pulled into the start of this sequence since every subsequent subcommand needs it.

The sub-tasks follow read-only-first → state-mutating → cleanup ordering:

#### Task 13a: `dialClient` helper + connection-error UX

(This was originally listed as Task 14; pulled forward because every following subcommand depends on it.)

**Promotion criteria:** When the socket is missing or refused, `circus` prints a fix-it message naming the home-manager option. No subcommands wired yet.

**Files:**
- Create: `cmd/circus/dial.go`
- Create: `cmd/circus/dial_test.go`

```go
package main

import (
    "errors"
    "fmt"
    "io/fs"
    "net"
    "os"

    rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

func dialClient() (*rm.Client, error) {
    socket, err := rm.SocketPath()
    if err != nil {
        return nil, err
    }
    cli, err := rm.NewClient(socket)
    if err == nil {
        return cli, nil
    }
    if errors.Is(err, fs.ErrNotExist) || isENOENT(err) || isECONNREFUSED(err) {
        fmt.Fprintf(os.Stderr,
            "circus: ringmaster is not running.\n"+
                "  fix: enable it in your home-manager config:\n"+
                "    programs.ringmaster.enable = true;\n"+
                "  then run: home-manager switch\n",
        )
    }
    return nil, err
}

func isENOENT(err error) bool {
    var pe *os.PathError
    if errors.As(err, &pe) {
        return errors.Is(pe.Err, fs.ErrNotExist)
    }
    return false
}

func isECONNREFUSED(err error) bool {
    var oe *net.OpError
    if errors.As(err, &oe) {
        // "connect: connection refused" comes through as OpError wrapping
        // a syscall.Errno. Match by string for portability.
        return oe.Op == "dial" && strings.Contains(oe.Err.Error(), "refused")
    }
    return false
}
```

Test: point `RINGMASTER_SOCKET` env at a nonexistent file, call `dialClient`, assert an error is returned and the stderr message names `programs.ringmaster.enable`.

**Commit:** `feat(circus): dialClient helper with home-manager hint on missing socket`

---

#### Task 13b: Wire `circus list` to ringmaster

**Promotion criteria:** Running `circus list` against a ringmaster with 0 or more instances prints them in a stable columnar format. Pre-existing `daemon.go` is unchanged.

**Files:**
- Modify: `cmd/circus/main.go` — replace the `case "list"` body with a `cli.ListInstances` call (it'll still be a one-instance world before Task 13d, but the wiring is in place)
- Modify: `cmd/circus/main.go` — add subcommand handler `cmdList(cli *rm.Client, args []string) int`
- Create: `cmd/circus/list_test.go` — integration test that spawns ringmaster on a temp socket, runs the `list` codepath via the binary, and asserts output

The existing `status` flat-file logic stays; only `list` migrates here.

**Commit:** `feat(circus): wire 'circus list' to ringmaster`

---

#### Task 13c: Wire `circus status` to ringmaster

**Promotion criteria:** `circus status [alias]` prints either the single-instance status (when alias given) or summary across all instances. Uses `GetInstance` and `ListInstances`.

**Files:**
- Modify: `cmd/circus/main.go` — replace the `case "status"` body with the new handler
- Create: `cmd/circus/status_test.go` — integration test

Note: `status` previously could probe llama-server's `/health` for liveness. For v1, "in the ringmaster registry" IS the liveness signal (the reaper removes dead children). If we want a deeper health probe (`/slots`, etc.) it belongs in a later task.

**Commit:** `feat(circus): wire 'circus status' to ringmaster`

---

#### Task 13d: Wire `circus start` and `circus stop` to ringmaster

**Promotion criteria:** `circus start <model> [--alias x] [--bind addr]` and `circus stop <alias>` work end-to-end. After this commit, `attachOrStart`, `startDaemon`, and `stopDaemon` in `daemon.go` are dead code (but still compile because nothing else references them yet).

**Files:**
- Modify: `cmd/circus/main.go` — replace `start` and `stop` handlers
- Create: `cmd/circus/start_test.go`, `cmd/circus/stop_test.go` — integration tests using the fake llama-server binary already in `cmd/ringmaster/testdata/`

The replacement is mechanical:

```go
case "start":
    cli, err := dialClient()
    if err != nil { ... }
    defer cli.Close()
    return cmdStart(cli, args[1:])
```

Where `cmdStart` parses `--model`, `--alias`, `--bind`, and any pass-through `--` args, then calls `cli.StartInstance`. Pretty-print the resulting `Instance`.

**Commit:** `feat(circus): wire 'circus start' and 'circus stop' to ringmaster`

---

#### Task 13e: Wire `circus models` to ringmaster

**Promotion criteria:** `circus models` lists installed GGUFs via `ListAvailableModels`. Stays read-only.

**Files:**
- Modify: `cmd/circus/main.go` — replace the `case "models"` body with the new handler
- Create: `cmd/circus/models_test.go`

Note: `circus download` stays unchanged in this commit — per the agreed scope it keeps using `circusmodels.Download` directly (with bubbletea UI). The ringmaster `DownloadModel` RPC exists for clown-side automation but circus CLI doesn't route through it.

**Commit:** `feat(circus): wire 'circus models' to ringmaster`

---

#### Task 13f: Delete `daemon.go` and `daemon_test.go`

**Promotion criteria:** No references to the flat-file pid/port logic remain. Build clean, tests still pass. `cmd/circus` consists of `main.go`, `dial.go`, subcommand handlers, the unchanged `download.go`, and tests.

**Files:**
- Delete: `cmd/circus/daemon.go`
- Delete: `cmd/circus/daemon_test.go`
- Modify: `cmd/circus/main.go` — remove any imports the deletion frees up (the daemon helpers)

Verification: `rg -l 'llama-server\.(pid|port)' cmd/circus` returns nothing. `go vet ./cmd/circus/...` clean. `just test-go` green.

**Commit:** `refactor(circus): delete flat-file daemon, all subcommands on ringmaster`

---

#### Task 13g: Final simplify pass on `cmd/circus`

**Promotion criteria:** After the migration, sweep for: unused imports, unused state-file path constants, redundant helpers shadowed by `internal/ringmaster`, and any code paths that became unreachable. Document why anything that *looks* removable is actually still needed.

**Files:** as needed.

**Commit:** `refactor(circus): drop dead state-file helpers after ringmaster migration` (or skip the commit if nothing surfaces).

---

### Task 15: `cmd/clown` adjustments to remove `runCircus`'s spawning

**Promotion criteria:** `cmd/clown` no longer spawns `circus start` inline. The `runCircus` path either deletes itself or short-circuits to an error pointing at the new flow (the real `--backend=circus` plumbing lives in plan 2).

**Files:**
- Modify: `cmd/clown/main.go` — `runCircus` becomes a stub that errors out for now
- Modify: `cmd/clown/main.go` — readCircusHandshake and related helpers can go (no more clown-protocol-via-circus)

Keep the changes surgical. The full unification of `runCircus` into `runClaude` belongs in plan 2; this task just stops the old inline spawning.

**Commit:**

```bash
git commit -m "refactor(clown): stub runCircus; old inline spawn no longer applicable"
```

---

### Task 16: Home-manager module — launchd agent (macOS)

**Promotion criteria:** A working `home.nix` snippet that brings up ringmaster on macOS via launchd. Verified by `launchctl list | grep ringmaster` showing it running after `home-manager switch`.

**Files:**
- Create: `nix/home-manager/ringmaster.nix`
- Update: `flake.nix` to export the module under `homeManagerModules.ringmaster`

Pattern matches `amarbel-llc/piggy`'s pivy-agent module — see `~/eng/repos/piggy/nix/` for shape. The module exposes `programs.ringmaster.enable` (bool) and creates a `launchd.user.agents.ringmaster` with:

```nix
{
  serviceConfig = {
    Label = "co.amarbel.ringmaster";
    ProgramArguments = [
      "${cfg.package}/bin/ringmaster"
      "daemon"
    ];
    RunAtLoad = true;
    KeepAlive = true;
    StandardOutPath  = "${config.home.homeDirectory}/.local/log/ringmaster.log";
    StandardErrorPath = "${config.home.homeDirectory}/.local/log/ringmaster.log";
  };
}
```

**Commit:**

```bash
git commit -m "feat(nix): home-manager module for ringmaster on macOS (launchd)"
```

---

### Task 17: Home-manager module — systemd user service (Linux)

**Promotion criteria:** Same module, Linux branch. `systemctl --user status ringmaster` shows running after `home-manager switch`.

Same file, Linux branch:

```nix
{
  systemd.user.services.ringmaster = {
    Unit.Description = "Ringmaster (llama-server control plane)";
    Service = {
      ExecStart = "${cfg.package}/bin/ringmaster daemon";
      Restart = "always";
      StandardOutput = "journal";
      StandardError  = "journal";
    };
    Install.WantedBy = [ "default.target" ];
  };
}
```

**Commit:**

```bash
git commit -m "feat(nix): home-manager module for ringmaster on Linux (systemd)"
```

---

### Task 18: Update manpages

**Promotion criteria:** `man ringmaster`, `man circus`, and `man circus-start` reflect the new architecture.

**Files:**
- Create: `man/ringmaster.7.scd`
- Modify: `man/circus.1.scd`
- Modify: `man/circus-start.1.scd` (or merge into circus.1)

Document: the daemon, the socket path, the home-manager enablement, the client/server split, the alias-vs-model distinction.

**Commit:**

```bash
git commit -m "docs(man): document ringmaster and the new circus client"
```

---

### Task 19: End-to-end smoke test (bats)

**Promotion criteria:** A bats test boots ringmaster against a fake llama-server, runs `circus start`/`list`/`stop`, and asserts on output.

**Files:**
- Create: `zz-tests_bats/ringmaster.bats`

(See `eng:wiring-bats-tests` skill for wiring conventions in this repo.)

**Commit:**

```bash
git commit -m "test(bats): end-to-end ringmaster smoke test"
```

---

### Task 20: Final build and full test sweep

```bash
just build
just test-go
just zz-tests_bats/ringmaster.bats   # if a per-file recipe exists
```

Then a `git log --oneline` review and a final `just` (the full pre-merge gate).

**Commit any leftover housekeeping, then:**

```bash
mcp__plugin_spinclass_spinclass__merge-this-session
```

---

## Notes for the implementing agent

- **Use Go stdlib only.** Resist the urge to pull in a JSON-RPC library; the wire format is small and hand-rolled.
- **Each `cmd/circus/<subcommand>` test is now an integration test against a real ringmaster.** Spawn ringmaster on a temp socket in the test setup; tear it down in cleanup.
- **The fake llama-server fixture exists because the real one is huge and slow to load.** Don't try to test against a real GGUF in unit tests. Integration tests on the test machine can opt into a real model if needed via `CIRCUS_INTEGRATION_MODEL`.
- **Flat-file removal is non-negotiable.** If anything in the diff still references `llama-server.pid` or `llama-server.port`, you've missed a spot.
- **`circus` is now the only client.** Don't add another binary that talks to ringmaster — clown will use the same `internal/ringmaster.Client` SDK in plan 2.
