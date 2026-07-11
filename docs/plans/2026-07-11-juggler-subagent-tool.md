# Juggler Subagent-Delegation Tool Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** Give a running Claude Code session a way to delegate a task to a
juggler-registered model (local llama-server or OpenRouter/remote gateway
entry) and get a text result back — a new MCP tool, not a Task-tool redirect
(Claude Code's native subagent mechanism can't route to a different endpoint;
see `docs/plans/2026-07-11-juggler-subagent-tool-design.md`).

**Architecture:** A new `juggler mcp` subcommand (same binary/package as
`juggler prompt`) runs a hand-rolled, line-delimited JSON-RPC 2.0 server on
stdin/stdout, mirroring `ringmaster mcp`'s `jobmcp.Serve` implementation
exactly (read from the nix store this session:
`/nix/store/j1nxw2brqq239qi0jcb4p2q2inqykbqk-ringmaster-go-pkgs/jobmcp/jobmcp.go`).
It exposes one tool, `juggler-prompt(model, task, max_tokens?)`, dispatching
to the existing `sendAnthropicPrompt`/`sendOpenAICompatPrompt` functions
(same package, zero duplication). `cmd/clown` synthesizes a new
`clown-builtin-juggler` plugin at session launch — a sibling to the existing
`clown-builtin-jobs`, built by copying `cmd/clown/jobmonitor.go`'s
`synthJobMonitorPluginDir` pattern — wiring `juggler mcp` as a `stdioServers`
entry via the already-existing `buildcfg.JugglerCliPath` (no new build/ldflags
wiring needed — confirmed already burned into `flake.nix`'s clown-go
derivation for the existing local-model picker). `cmd/clown-hook-allow` is
left untouched functionally — its existing "defer everything not explicitly
allowed" fallthrough already covers this new tool's tools — with one comment
added explaining the omission is intentional.

**Tech Stack:** Go, hand-rolled JSON-RPC 2.0 (no MCP SDK, matching the
existing `jobmcp`/`cmd/clown-stdio-bridge` precedent), `internal/pluginhost`'s
existing Desugar/HTTP-bridge machinery (untouched — this plan only produces
the `stdioServers` manifest entry the existing machinery already knows how to
consume), bats (optional — see Task 8), just.

**Rollback:** Purely additive — new binary subcommand, new synthesized
plugin, no existing behavior changed. `CLOWN_DISABLE_JUGGLER_MCP=1` (mirroring
the existing `CLOWN_DISABLE_JOB_WAKEUP=1` convention) skips synthesizing the
plugin entirely for a session — no code change needed to turn it off.

**Verification:** iterate with
`just zz-explore/go-test ./cmd/juggler/... ./cmd/clown/... ./cmd/clown-hook-allow/...`
(this repo's paved-path recipe for `go test`, works around a vendoring gap).
Do NOT run full `just` before merging each task — the spinclass pre-merge
hook runs it.

**Dirty-tree gotcha:** `nix build` (used for the final manual-smoke task)
only sees git-tracked files. `grit add` new files before any `nix build`, or
the build silently runs against the old file set and gives a false green —
this bit an earlier session in this same repo.

---

### Task 1: `cmd/juggler/mcp.go` — the JSON-RPC server + tool catalog

**Promotion criteria:** N/A (new capability).

**Files:**
- Create: `cmd/juggler/mcp.go`
- Test: `cmd/juggler/mcp_test.go`

**Step 1: Write failing tests.** Drive `Serve` directly over `bytes.Buffer`s —
no real stdin/stdout, no daemon required for the protocol-shape tests. Reuse
`prompt_test.go`'s `fakeResolveModelDaemon`/`dialClient` harness for the
`tools/call` integration test (read `cmd/juggler/prompt_test.go` lines
287-370 first to copy the exact fixture — it starts a fake UDS listener
answering one `ResolveModel` RPC, sets `JUGGLER_SOCKET` via `t.Setenv`, then
`dialClient()` picks it up).

```go
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	rm "github.com/amarbel-llc/clown/internal/juggler"
)

func TestMCPServe_Initialize(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")
	var out bytes.Buffer
	Serve(in, &out, nil)

	var resp struct {
		Result struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, out.Bytes())
	}
	if resp.Result.ServerInfo.Name != "juggler" {
		t.Errorf("serverInfo.name = %q, want juggler", resp.Result.ServerInfo.Name)
	}
}

func TestMCPServe_ToolsList(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n")
	var out bytes.Buffer
	Serve(in, &out, nil)

	var resp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Required []string `json:"required"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, out.Bytes())
	}
	if len(resp.Result.Tools) != 1 || resp.Result.Tools[0].Name != "juggler-prompt" {
		t.Fatalf("tools = %+v, want exactly one juggler-prompt tool", resp.Result.Tools)
	}
	req := resp.Result.Tools[0].InputSchema.Required
	if len(req) != 2 || req[0] != "model" || req[1] != "task" {
		t.Errorf("required params = %v, want [model task]", req)
	}
}

// TestMCPServe_ToolsCall_HappyPath drives a full tools/call round trip:
// fake juggler daemon resolves "my-model" to a fake remote HTTP endpoint,
// Serve dispatches the call, and the MCP tool-result content carries the
// model's reply.
func TestMCPServe_ToolsCall_HappyPath(t *testing.T) {
	var gotPrompt string
	remoteSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r.Body)
		var req anthropicRequest
		_ = json.Unmarshal(body, &req)
		if len(req.Messages) == 1 {
			gotPrompt = req.Messages[0].Content
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"delegated reply"}]}`))
	}))
	defer remoteSrv.Close()

	fakeResolveModelDaemon(t, rm.ResolveModelResult{
		Kind: rm.ModelKindRemote, URL: remoteSrv.URL, Token: "sekret", Style: "anthropic",
	})
	cli, err := dialClient()
	if err != nil {
		t.Fatalf("dialClient: %v", err)
	}
	defer cli.Close()

	params := `{"name":"juggler-prompt","arguments":{"model":"my-model","task":"do the thing"}}`
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + params + `}` + "\n")
	var out bytes.Buffer
	Serve(in, &out, cli)

	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, out.Bytes())
	}
	if resp.Result.IsError {
		t.Fatalf("unexpected isError, content: %+v", resp.Result.Content)
	}
	if len(resp.Result.Content) != 1 || resp.Result.Content[0].Text != "delegated reply" {
		t.Errorf("content = %+v, want [{delegated reply}]", resp.Result.Content)
	}
	if gotPrompt != "do the thing" {
		t.Errorf("prompt sent to model = %q, want %q", gotPrompt, "do the thing")
	}
}

// TestMCPServe_ToolsCall_UnknownModel verifies a not-found model surfaces
// as an isError tool result (agent-readable), not a JSON-RPC transport error.
func TestMCPServe_ToolsCall_UnknownModel(t *testing.T) {
	// A daemon that immediately closes without answering simulates
	// "socket exists but nothing registered" closely enough for this test —
	// simplest is to point JUGGLER_SOCKET at a nonexistent path so dialClient
	// itself fails, which cmdMCPToolCall must also turn into an isError result
	// rather than crashing the whole Serve loop.
	t.Setenv("JUGGLER_SOCKET", t.TempDir()+"/no-such.sock")
	cli, _ := dialClient() // cli is nil on error — Serve/dispatch must handle a nil cli gracefully via the daemon-unreachable path
	_ = cli

	params := `{"name":"juggler-prompt","arguments":{"model":"nope","task":"x"}}`
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + params + `}` + "\n")
	var out bytes.Buffer
	Serve(in, &out, nil) // nil cli: exactly the "daemon unreachable" case this test wants

	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, out.Bytes())
	}
	if !resp.Result.IsError {
		t.Fatal("want isError=true for an unreachable daemon")
	}
	if len(resp.Result.Content) != 1 || resp.Result.Content[0].Text == "" {
		t.Errorf("content = %+v, want a non-empty error message", resp.Result.Content)
	}
}
```

You'll need a tiny `readAll` helper if `cmd/juggler` doesn't already import
`io` with that exact name available at package scope in a test file — check
`prompt_test.go`'s imports first; it already imports `io` and uses
`io.ReadAll`, so just call `io.ReadAll` directly instead of a custom
`readAll` (the snippet above uses a placeholder name — replace with
`io.ReadAll` in the real file and add `"io"` to the import block if not
already present in `mcp_test.go`).

**Step 2:** `just zz-explore/go-test ./cmd/juggler/... -run TestMCPServe` —
expect FAIL (undefined `Serve`).

**Step 3: Implement.** `cmd/juggler/mcp.go`:

```go
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	rm "github.com/amarbel-llc/clown/internal/juggler"
)

// mcpMaxTokensDefault is deliberately higher than juggler prompt's CLI
// default (256): the CLI is a quick smoke/hello-world tool, this tool is
// for real delegated subagent-shaped work. This session's own testing
// showed a reasoning model needs a much larger budget to get past its
// thinking tokens to an actual answer (docs/plans/2026-07-11-juggler-
// subagent-tool-design.md's Tuning Levers).
const mcpMaxTokensDefault = 1024

// mcpToolTimeout mirrors promptTimeout's rationale (may need to start a
// local instance) — one budget per tools/call.
const mcpToolTimeout = 120 * time.Second

// Serve runs the JSON-RPC 2.0 loop against in/out — the MCP stdio
// transport, hand-rolled to match ringmaster's jobmcp.Serve exactly (no
// MCP SDK is vendored in this repo). cli is used to resolve and prompt
// models on tools/call; it may be nil, in which case every tools/call
// reports the daemon as unreachable (used by tests exercising that path
// without a live socket).
func Serve(in io.Reader, out io.Writer, cli *rm.Client) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue // skip unparseable line (transport noise)
		}
		switch req.Method {
		case "initialize":
			_ = enc.Encode(mcpResult(req.ID, map[string]any{
				"protocolVersion": "2025-06-18",
				"serverInfo":      map[string]any{"name": "juggler", "version": "1"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}))
		case "tools/list":
			_ = enc.Encode(mcpResult(req.ID, map[string]any{"tools": []map[string]any{jugglerPromptToolSchema()}}))
		case "tools/call":
			_ = enc.Encode(mcpResult(req.ID, callJugglerPromptTool(cli, req.Params)))
		case "notifications/initialized":
			// Notification (no id): no response.
		default:
			if len(req.ID) > 0 {
				_ = enc.Encode(mcpError(req.ID, -32601, fmt.Sprintf("unknown method %q", req.Method)))
			}
		}
	}
}

func mcpResult(id json.RawMessage, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func mcpError(id json.RawMessage, code int, message string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}
}

// mcpToolText/mcpToolErr mirror ringmaster jobmcp's toolText/toolErr
// convention: a tool-level failure is content with isError set, so the
// agent sees the message as tool output it can react to, not a transport
// error.
func mcpToolText(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
}

func mcpToolErr(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": true}
}

// jugglerPromptToolSchema is the single tool this server exposes — model
// as a parameter, not one tool per registered model, so the catalog never
// needs regenerating when models are added/removed (mirrors how Task's
// own subagent_type is a parameter, not one tool per subagent type).
func jugglerPromptToolSchema() map[string]any {
	return map[string]any{
		"name":        "juggler-prompt",
		"description": "Delegate a task to a registered juggler model (local or remote) and get its text reply.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"model":      map[string]any{"type": "string", "description": "a name registered via `juggler model add`, or a local GGUF name"},
				"task":       map[string]any{"type": "string", "description": "the task/prompt to send"},
				"max_tokens": map[string]any{"type": "integer", "description": "default 1024"},
			},
			"required": []string{"model", "task"},
		},
	}
}

// callJugglerPromptTool unmarshals a tools/call request for juggler-prompt,
// resolves the model, and sends the task — reusing the exact same
// sendAnthropicPrompt/sendOpenAICompatPrompt functions `juggler prompt`
// uses. A nil cli (daemon unreachable at dial time — see cmdMCP below)
// short-circuits straight to the daemon-unreachable error.
func callJugglerPromptTool(cli *rm.Client, params json.RawMessage) map[string]any {
	var call struct {
		Name      string `json:"name"`
		Arguments struct {
			Model     string `json:"model"`
			Task      string `json:"task"`
			MaxTokens int    `json:"max_tokens"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return mcpToolErr(fmt.Sprintf("invalid tools/call params: %v", err))
	}
	if call.Name != "juggler-prompt" {
		return mcpToolErr(fmt.Sprintf("unknown tool %q", call.Name))
	}
	if cli == nil {
		return mcpToolErr("juggler daemon unreachable — start it: juggler daemon")
	}
	maxTokens := call.Arguments.MaxTokens
	if maxTokens == 0 {
		maxTokens = mcpMaxTokensDefault
	}

	ctx, cancel := context.WithTimeout(context.Background(), mcpToolTimeout)
	defer cancel()

	resolved, err := cli.ResolveModel(ctx, rm.ResolveModelParams{Name: call.Arguments.Model})
	if err != nil {
		return mcpToolErr(fmt.Sprintf("resolve model %q: %v", call.Arguments.Model, err))
	}
	reply, err := sendPrompt(ctx, httpClientForMCP(), resolved, call.Arguments.Model, call.Arguments.Task, maxTokens)
	if err != nil {
		return mcpToolErr(err.Error())
	}
	return mcpToolText(reply)
}
```

You'll need an `httpClientForMCP()` helper (or just inline
`http.DefaultClient` — check how `cmdPrompt` in `prompt.go` sources its
`*http.Client` for `sendPrompt` and match that exactly rather than
introducing a new pattern).

**Step 4:** `just zz-explore/go-test ./cmd/juggler/... -run TestMCPServe` —
PASS.

**Step 5: Commit:**
```
grit add cmd/juggler/mcp.go cmd/juggler/mcp_test.go
grit commit -m "feat(juggler): juggler mcp — stdio MCP server exposing juggler-prompt"
```

---

### Task 2: `cmd/juggler/main.go` — wire the `mcp` subcommand

**Promotion criteria:** N/A.

**Files:**
- Modify: `cmd/juggler/main.go`

**Step 1:** Read the current `run` function's `switch args[0]` block (it's
short — every case is one line via `withClient`). No new test needed here;
Task 1's tests already exercise `Serve` directly. Add a cheap manual check
after implementing (Step 3) instead of a unit test, since this is a one-line
dispatch wire — matching how `case "prompt":` itself needed no dedicated
dispatch-wiring test beyond the `cmdPrompt` tests it calls into.

**Step 2:** N/A (no new failing test for this task — see above).

**Step 3: Implement.** In `cmd/juggler/main.go`:

```go
	case "mcp":
		return withClient(func(cli *rm.Client) int { Serve(os.Stdin, os.Stdout, cli); return 0 })
```

Add this alongside the existing `case "prompt":` entry. Also update the
top-level usage string (line 20) to include `mcp`:
`"usage: juggler <daemon|start|stop|status|list|models|model|prompt|mcp|download> [args]"`.

Note: `withClient` currently prints a stderr error and returns 1 if dialing
fails (see `dial.go`) — for `mcp`, that means a dial failure exits the
process immediately rather than running `Serve` with a nil client (the nil-
client path in Task 1 is exercised directly via unit test, not through this
real dispatch path — that's fine, both are valid: a real `juggler mcp`
invocation with no daemon running fails fast at startup with a clear
stderr message, exactly like every other `juggler` subcommand today).

**Step 4:** `go build ./cmd/juggler/...` (or the go-test recipe, which also
compiles) to confirm it compiles. `just zz-explore/go-test ./cmd/juggler/...`
— PASS, no regressions.

**Step 5: Commit:**
```
grit add cmd/juggler/main.go
grit commit -m "feat(juggler): wire juggler mcp into the subcommand dispatch"
```

---

### Task 3: `cmd/clown/jugglermonitor.go` — synthesize the `clown-builtin-juggler` plugin

**Promotion criteria:** N/A.

**Files:**
- Create: `cmd/clown/jugglermonitor.go`
- Test: `cmd/clown/jugglermonitor_test.go`

**Step 1: Write failing tests.** Read `cmd/clown/jobmonitor_test.go` in full
first (already read this session — it's the exact pattern to mirror: one
test asserting the plugin.json/clown.json shape when `buildcfg.JugglerCliPath`
is set, one asserting `CLOWN_DISABLE_JUGGLER_MCP=1` yields `("", nil)`).

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/amarbel-llc/clown/internal/buildcfg"
)

func TestJugglerPluginDirSynthesized(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JUGGLER_MCP", "")
	orig := buildcfg.JugglerCliPath
	buildcfg.JugglerCliPath = "/nix/store/x/bin/juggler"
	t.Cleanup(func() { buildcfg.JugglerCliPath = orig })

	dir, err := synthJugglerPluginDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir == "" {
		t.Fatal("expected a synthesized plugin dir when JugglerCliPath is set")
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	manifestPath := filepath.Join(dir, ".claude-plugin", "plugin.json")
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading synthesized manifest: %v", err)
	}
	var m struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v\n%s", err, b)
	}
	if m.Name != "clown-builtin-juggler" {
		t.Fatalf("plugin name = %q, want clown-builtin-juggler", m.Name)
	}

	cb, err := os.ReadFile(filepath.Join(dir, "clown.json"))
	if err != nil {
		t.Fatalf("expected clown.json: %v", err)
	}
	var cfg struct {
		Version      int `json:"version"`
		StdioServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"stdioServers"`
	}
	if err := json.Unmarshal(cb, &cfg); err != nil {
		t.Fatalf("clown.json invalid: %v\n%s", err, cb)
	}
	srv, ok := cfg.StdioServers["juggler"]
	if !ok {
		t.Fatalf("clown.json missing stdioServers.juggler; got %s", cb)
	}
	if srv.Command != buildcfg.JugglerCliPath {
		t.Fatalf("command = %q, want the baked JugglerCliPath %q", srv.Command, buildcfg.JugglerCliPath)
	}
	if len(srv.Args) != 1 || srv.Args[0] != "mcp" {
		t.Fatalf("args = %v, want [mcp]", srv.Args)
	}
}

func TestJugglerPluginDirNoPathReturnsEmpty(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JUGGLER_MCP", "")
	orig := buildcfg.JugglerCliPath
	buildcfg.JugglerCliPath = ""
	t.Cleanup(func() { buildcfg.JugglerCliPath = orig })

	dir, err := synthJugglerPluginDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != "" {
		_ = os.RemoveAll(dir)
		t.Fatalf("expected no plugin dir when JugglerCliPath is empty (dev build), got %q", dir)
	}
}

func TestJugglerPluginDirDisabledReturnsEmpty(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JUGGLER_MCP", "1")
	orig := buildcfg.JugglerCliPath
	buildcfg.JugglerCliPath = "/nix/store/x/bin/juggler"
	t.Cleanup(func() { buildcfg.JugglerCliPath = orig })

	dir, err := synthJugglerPluginDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != "" {
		_ = os.RemoveAll(dir)
		t.Fatalf("expected no plugin dir when CLOWN_DISABLE_JUGGLER_MCP=1, got %q", dir)
	}
}
```

**Step 2:** `just zz-explore/go-test ./cmd/clown/... -run TestJugglerPluginDir`
— FAIL (undefined `synthJugglerPluginDir`).

**Step 3: Implement.** `cmd/clown/jugglermonitor.go` (note: unlike
`clown-builtin-jobs`, this plugin carries NO monitors — just the
`stdioServers` entry — so `plugin.json` has no top-level `monitors` key at
all; only `name`/`version`):

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/amarbel-llc/clown/internal/buildcfg"
)

// jugglerMCPDisabled reports whether the juggler subagent-delegation tool is
// switched off via CLOWN_DISABLE_JUGGLER_MCP=1. When set, the synthesized
// plugin dir is not written so no juggler-prompt tool is registered.
func jugglerMCPDisabled() bool {
	return os.Getenv("CLOWN_DISABLE_JUGGLER_MCP") == "1"
}

// jugglerPluginManifest is the minimal .claude-plugin/plugin.json this
// built-in plugin needs — no monitors, unlike clown-builtin-jobs.
type jugglerPluginManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// synthJugglerPluginDir writes a temporary built-in plugin directory
// declaring `juggler mcp` as a stdioServers entry (docs/plans/2026-07-11-
// juggler-subagent-tool-design.md), and returns its path. The caller
// appends the path to the --plugin-dir set passed to Claude and removes
// the directory on shutdown, mirroring synthJobMonitorPluginDir's contract
// exactly. Returns ("", nil) when disabled (CLOWN_DISABLE_JUGGLER_MCP=1) or
// when buildcfg.JugglerCliPath is empty (dev builds — go run/go build never
// burn this in; only the nix derivation does), so a dev build never ships
// a clown.json pointing at a nonexistent path.
func synthJugglerPluginDir() (string, error) {
	if jugglerMCPDisabled() || buildcfg.JugglerCliPath == "" {
		return "", nil
	}
	dir, err := os.MkdirTemp("", "clown-juggler-plugin-")
	if err != nil {
		return "", err
	}
	manifestDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	manifest := jugglerPluginManifest{Name: "clown-builtin-juggler", Version: "1"}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), b, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}

	clownCfg := map[string]any{
		"version": 1,
		"stdioServers": map[string]any{
			"juggler": map[string]any{
				"command": buildcfg.JugglerCliPath,
				"args":    []string{"mcp"},
			},
		},
	}
	cb, err := json.MarshalIndent(clownCfg, "", "  ")
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "clown.json"), cb, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	// Deliberately NO hooks/hooks.json here: unlike clown-builtin-jobs, this
	// tool makes live calls to (potentially paid, third-party) APIs on the
	// agent's own initiative, so it must NOT auto-allow. clown-builtin-jobs'
	// own hooks.json (matcher ".*") already runs clown-hook-allow for every
	// tool call in the session including this plugin's — and clown-hook-allow
	// deliberately does not list this tool's prefix in its allow-map, so it
	// falls through to the existing defer-to-native-prompt path with zero new
	// hook code (see cmd/clown-hook-allow/main.go's comment near
	// jobToolPrefix). If clown-builtin-jobs is ever disabled independently
	// (CLOWN_DISABLE_JOB_WAKEUP=1) while this plugin stays enabled, no hook
	// runs at all for this tool either — which still means "defer to Claude
	// Code's native prompt," the same outcome, just via a different path.
	return dir, nil
}
```

**Step 4:** `just zz-explore/go-test ./cmd/clown/... -run TestJugglerPluginDir`
— PASS.

**Step 5: Commit:**
```
grit add cmd/clown/jugglermonitor.go cmd/clown/jugglermonitor_test.go
grit commit -m "feat(clown): synthesize the clown-builtin-juggler plugin"
```

---

### Task 4: `cmd/clown/main.go` — wire the new plugin into the launch path

**Promotion criteria:** N/A.

**Files:**
- Modify: `cmd/clown/main.go:565-581` (the `synthJobMonitorPluginDir` call
  site — re-locate by searching for that function name, since line numbers
  may have shifted)

**Step 1:** No new test for this task specifically — Task 3's tests already
cover `synthJugglerPluginDir`'s own logic; this task is purely the one call
site wiring it into `pluginDirs`, mirroring the existing job-monitor call
site's exact structure (error handling, defer cleanup, append-last ordering).

**Step 2:** N/A.

**Step 3: Implement.** Immediately after the existing block that appends
`monitorDir` (still inside the same
`if providerUsesPluginDirs(flags.provider) && !flags.naked {` scope — check
the live file first to confirm whether to nest inside that same `if` or
add a sibling one; mirror whichever reads more consistently with the
existing code), add:

```go
	if providerUsesPluginDirs(flags.provider) && !flags.naked {
		if jugglerDir, err := synthJugglerPluginDir(); err != nil {
			fmt.Fprintf(os.Stderr, "clown: registering juggler-prompt tool: %v\n", err)
		} else if jugglerDir != "" {
			defer os.RemoveAll(jugglerDir)
			pluginDirs = append(pluginDirs, jugglerDir)
		}
	}
```

**Step 4:** `just zz-explore/go-test ./cmd/clown/...` — PASS, no
regressions (existing `TestJobMonitor*` tests untouched).

**Step 5: Commit:**
```
grit add cmd/clown/main.go
grit commit -m "feat(clown): register clown-builtin-juggler for --plugin-dir providers"
```

---

### Task 5: `cmd/clown-hook-allow/main.go` — explicit defer comment

**Promotion criteria:** N/A.

**Files:**
- Modify: `cmd/clown-hook-allow/main.go`

**Step 1:** No new test — this is a comment-only change (no behavior
change: the tool was already going to defer, since it's absent from
`jobToolPrefix`/the allow-map). If you want a belt-and-suspenders
regression test proving a `mcp__plugin_clown-builtin-juggler_juggler__`-
prefixed tool name is NOT in `evaluate`'s allow set, add one — otherwise
skip, since "absence from an allow-list" isn't really testable without
inventing a tautological assertion (see Step 3's comment for the reasoning
this task documents instead of code-enforces).

**Step 2:** N/A.

**Step 3: Implement.** Add a comment near `jobToolPrefix`
(`cmd/clown-hook-allow/main.go:47-55`), e.g. right after that const block:

```go
// clown-builtin-juggler's tools (the juggler-prompt subagent-delegation
// tool, docs/plans/2026-07-11-juggler-subagent-tool-design.md) are
// deliberately NOT added to any allow-list here. Unlike clown-builtin-jobs
// (inert local job-channel plumbing), this tool makes live calls to
// potentially-paid third-party APIs on the agent's own initiative — it
// falls through to the default `return nil // defer` path below, so every
// call gets Claude Code's normal per-tool-call permission prompt. Revisit
// only if that friction proves excessive in practice (a scoped allow-list
// of specific pre-registered model names, not a blanket allow, would be
// the next step — see the design doc's Tuning Levers).
```

**Step 4:** `just zz-explore/go-test ./cmd/clown-hook-allow/...` — PASS
(comment-only change; confirms nothing broke).

**Step 5: Commit:**
```
grit add cmd/clown-hook-allow/main.go
grit commit -m "docs(clown-hook-allow): note clown-builtin-juggler tools intentionally defer"
```

---

### Task 6: man page

**Files:**
- Modify: `man/man1/juggler.1`

**Steps:** Read the current file (already read this session for the
`prompt`/`model` subcommand entries — follow that exact style). Add:
- `mcp` to the `.Sh SYNOPSIS` block.
- A `.It Cm mcp` entry in `.Sh SUBCOMMANDS` describing: runs a stdio MCP
  server exposing one tool, `juggler-prompt(model, task, max_tokens?)`;
  normally spawned by Claude Code via the `clown-builtin-juggler` plugin's
  `stdioServers` entry, not invoked directly by a human (mirror how the
  `download` entry's CAVEATS section explains an unusual invocation
  pattern, for the right tone here).
- Cross-reference: add `.Xr clown-hook-allow 1` (if that man page exists —
  check) or otherwise a plain-text pointer to
  `docs/plans/2026-07-11-juggler-subagent-tool-design.md` for the permission
  posture (defer to native prompt, no auto-allow).

`go build ./cmd/juggler/... ./cmd/clown/...`, commit:
`docs: juggler mcp subcommand`.

Run `@eng:doc-drift` against the full diff before merge (mandatory
pre-merge attestation) — in particular check whether `AGENTS.md` needs
anything (per this session's established finding, it's deliberately terse/
pointer-based post-rewrite — likely nothing to add, confirm rather than
assume).

---

### Task 7: manual smoke test

Not a commit — verification only, before the final merge.

Reuse this session's exact working harness (temp `JUGGLER_SOCKET`/
`JUGGLER_MODELS_PATH`, real daemon, the already-verified live OpenRouter
models `claude-3-haiku-20240307` and `openai/gpt-oss-20b:free`):

1. `nix build .#juggler` (after `grit add`-ing all new files — the
   dirty-tree gotcha).
2. Start the daemon, register both test models (both `--style anthropic`
   and one re-registered `--style openai-compat` variant, to exercise both
   protocol paths through the MCP surface too — not just the CLI path
   already proven this session).
3. Pipe a raw JSON-RPC `tools/call` line into `juggler mcp`'s stdin
   directly (no clown involved yet):
   ```sh
   printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"juggler-prompt","arguments":{"model":"claude-3-haiku-20240307","task":"Say hello in exactly 3 words."}}}' \
     | ./result/bin/juggler mcp
   ```
   Confirm a real `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"..."}]}}`
   comes back.
4. Only after step 3 succeeds, build clown (`nix build .#default`) and do
   one real interactive Claude Code launch to confirm the
   `clown-builtin-juggler` plugin actually gets discovered and the
   `juggler-prompt` tool shows up in Claude Code's own tool list (e.g. ask
   Claude "what tools do you have available" or check via whatever
   introspection this repo's other plugin work used — `FetchToolCatalog`
   in `internal/pluginhost/host.go` if a programmatic check is easier than
   a live session).

---

## Execution notes

- Fresh subagent per task (`eng:subagent-driven-development`), code review
  between tasks.
- Tasks 1→2 (juggler side) and 3→4 (clown side) are each internally ordered
  by dependency; Task 5 is independent and can happen anytime after Task 3
  exists (references its design rationale in a comment, no code
  dependency). Task 6/7 last.
- Do not run full `just` at the end of each task — `merge-this-session`'s
  pre-merge hook is the CI lane.
- New files must be `grit add`ed before any `nix build` (dirty-tree builds
  only see tracked files).
