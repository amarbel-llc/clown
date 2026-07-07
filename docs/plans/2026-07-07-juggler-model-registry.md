# Juggler as a Unified Model Registry Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** Give juggler a persisted registry of "remote" models (static
gateway endpoints, no process) alongside its existing local llama-server
instances, and make clown's named-profile resolution (`applyNamedProfile`)
resolve a profile's `Model` field through juggler when there's no inline
`URL`/`Token` — including a compatibility check for subagent-model routing.

**Architecture:** `internal/juggler` gains a `RemoteModel` type + atomic
TOML store (mirroring `internal/profile/store.go`'s pattern) and four new
JSON-RPC methods (`ListModels`, `AddRemoteModel`, `RemoveRemoteModel`,
`ResolveModel`) on the existing UDS control socket. `cmd/juggler` gains a
`juggler model list|add|remove` subcommand family. `cmd/clown`'s
`applyNamedProfile` gains a juggler-resolution branch (inline URL/Token
still checked first, unchanged) and a subagent-model endpoint-compatibility
check that queries juggler's `ListModels` (no side effects) before ever
calling `ResolveModel` (which can start a local instance), so a plain
literal subagent-model string never accidentally triggers a spawn.

**Tech Stack:** Go, `BurntSushi/toml` (already vendored), the existing
juggler UDS JSON-RPC wire format (`internal/juggler/frame.go`), bats
(`zz-tests_bats`), just.

**Rollback:** Purely additive. A profile with inline `URL`/`Token` is
unaffected (checked first, unchanged code path). A profile whose `Env` map
has no subagent-model keys, or whose values aren't registered juggler
models, is unaffected. Don't register a remote model / don't set a
juggler-model-referencing `Env` value and nothing changes.

**Verification:** iterate with
`just zz-explore/go-test ./internal/juggler/... ./cmd/juggler/... ./cmd/clown/...`
(the throwaway ringmaster-bridge recipe from the prior session — works in
the devshell without a `nix build`). Cheap compile check:
`go build ./cmd/juggler/... ./cmd/clown/...`. Do NOT run full `just` before
merging — the spinclass pre-merge hook runs it.

**Dirty-tree gotcha (bit us last session):** any `nix build` (including
`.#bats-default`) only sees **git-tracked** files. Always `grit add` new
files *before* a verification `nix build`, or the build silently runs
against the old file set and gives a false green.

---

### Task 1: `internal/juggler` — RemoteModel type + atomic store

**Promotion criteria:** N/A (new capability, no old approach to retire).

**Files:**
- Create: `internal/juggler/remotemodels.go`
- Modify: `internal/juggler/paths.go` (add `RemoteModelsPath`)
- Test: `internal/juggler/remotemodels_test.go`

**Step 1: Write failing tests** (`remotemodels_test.go`, new file):

```go
package juggler

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSaveLoadRemoteModelsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "models.toml")
	in := []RemoteModel{{Name: "claude-openrouter", Style: "anthropic",
		URL: "https://openrouter.ai/api", Token: "${OPENROUTER_API_KEY}"}}
	if err := SaveRemoteModels(path, in); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	out, err := LoadRemoteModels(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip: %#v != %#v", in, out)
	}
}

func TestLoadRemoteModelsMissingFileIsEmpty(t *testing.T) {
	out, err := LoadRemoteModels(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || len(out) != 0 {
		t.Fatalf("out=%#v err=%v, want empty/nil", out, err)
	}
}

func TestUpsertAndRemoveRemoteModel(t *testing.T) {
	base := []RemoteModel{{Name: "a"}, {Name: "b"}}
	up := UpsertRemoteModel(base, RemoteModel{Name: "b", URL: "new"})
	if len(up) != 2 || up[1].URL != "new" {
		t.Fatalf("upsert replace: %#v", up)
	}
	up = UpsertRemoteModel(up, RemoteModel{Name: "c"})
	if len(up) != 3 {
		t.Fatalf("upsert append: %#v", up)
	}
	rm, found := RemoveRemoteModel(up, "a")
	if !found || len(rm) != 2 {
		t.Fatalf("remove: %#v found=%v", rm, found)
	}
	if _, found := RemoveRemoteModel(rm, "nope"); found {
		t.Fatal("remove of absent name must report false")
	}
}

func TestRemoteModelsPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JUGGLER_MODELS_PATH", "")
	t.Setenv("HOME", dir)
	got, err := RemoteModelsPath()
	want := filepath.Join(dir, ".local", "share", "juggler", "models.toml")
	if err != nil || got != want {
		t.Fatalf("got %q err=%v, want %q", got, err, want)
	}
	t.Setenv("JUGGLER_MODELS_PATH", "/tmp/override.toml")
	got, err = RemoteModelsPath()
	if err != nil || got != "/tmp/override.toml" {
		t.Fatalf("override: got %q err=%v", got, err)
	}
}
```

**Step 2:** `just zz-explore/go-test ./internal/juggler/...` — expect FAIL
(undefined `RemoteModel`, `SaveRemoteModels`, etc.).

**Step 3: Implement.** `internal/juggler/remotemodels.go`:

```go
package juggler

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// RemoteModel is a user-registered, process-less gateway endpoint — the
// "remote" half of juggler's model registry (the "local" half is GGUF
// files, auto-discovered by internal/jugglermodels; see Task 4's
// ListModels union). Token may be a literal secret or a "${VAR}"
// reference, resolved with os.ExpandEnv at ResolveModel time (Task 4) —
// not resolved here, so the on-disk file never needs the ambient env.
type RemoteModel struct {
	Name  string `toml:"name"`
	Style string `toml:"style"` // "anthropic" | "openai-compat"
	URL   string `toml:"url"`
	Token string `toml:"token"`
}

type remoteModelsFile struct {
	Model []RemoteModel `toml:"model"`
}

// LoadRemoteModels reads path, returning (nil, nil) if it doesn't exist —
// an empty registry is not an error (mirrors internal/profile's Load
// posture for an absent user profiles.toml).
func LoadRemoteModels(path string) ([]RemoteModel, error) {
	var f remoteModelsFile
	if _, err := toml.DecodeFile(path, &f); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("load remote models %s: %w", path, err)
	}
	return f.Model, nil
}

// SaveRemoteModels writes models to path atomically (temp file + rename),
// 0600 file in a 0700 directory. Mirrors internal/profile/store.go's
// Save — this file is juggler-managed, hand-edited comments are not
// preserved across a save.
func SaveRemoteModels(path string, models []RemoteModel) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(remoteModelsFile{Model: models}); err != nil {
		return fmt.Errorf("encode remote models: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".models-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename over %s: %w", path, err)
	}
	return nil
}

// UpsertRemoteModel replaces the entry whose Name matches m, or appends m.
func UpsertRemoteModel(models []RemoteModel, m RemoteModel) []RemoteModel {
	for i := range models {
		if models[i].Name == m.Name {
			out := make([]RemoteModel, len(models))
			copy(out, models)
			out[i] = m
			return out
		}
	}
	return append(append([]RemoteModel(nil), models...), m)
}

// RemoveRemoteModel filters out the entry named name, reporting presence.
func RemoveRemoteModel(models []RemoteModel, name string) ([]RemoteModel, bool) {
	out := make([]RemoteModel, 0, len(models))
	found := false
	for _, m := range models {
		if m.Name == name {
			found = true
			continue
		}
		out = append(out, m)
	}
	return out, found
}
```

Append to `internal/juggler/paths.go` (after `LogPath`):

```go
// RemoteModelsPath returns the remote-model registry file location:
// ~/.local/share/juggler/models.toml (sibling to the GGUF models/ dir
// jugglermodels.Dir() resolves). JUGGLER_MODELS_PATH overrides it (tests).
func RemoteModelsPath() (string, error) {
	if v := os.Getenv("JUGGLER_MODELS_PATH"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".local", "share", "juggler", "models.toml"), nil
}
```

**Step 4:** `just zz-explore/go-test ./internal/juggler/...` — PASS.

**Step 5: Commit:**
```
grit add internal/juggler/remotemodels.go internal/juggler/remotemodels_test.go internal/juggler/paths.go
grit commit -m "feat(juggler): remote-model registry (RemoteModel + atomic store)"
```

---

### Task 2: `internal/juggler` — RPC wire types for the new methods

**Promotion criteria:** N/A.

**Files:**
- Modify: `internal/juggler/rpc.go`
- Test: `internal/juggler/rpc_test.go` (extend — follow its existing
  round-trip-marshal style; read it first)

**Step 1: Write failing test.** Read `internal/juggler/rpc_test.go` first
to match its existing pattern (likely JSON marshal/unmarshal round-trips
per params/result type). Add one round-trip test per new type, e.g.:

```go
func TestResolveModelParamsRoundTrip(t *testing.T) {
	p := ResolveModelParams{Name: "claude-openrouter"}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var got ResolveModelParams
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Fatalf("got %#v, want %#v", got, p)
	}
}
```
(Mirror for `ListModelsResult`, `AddRemoteModelParams`, `RemoveRemoteModelParams`.)

**Step 2:** `just zz-explore/go-test ./internal/juggler/... -run RoundTrip` —
FAIL (undefined types).

**Step 3: Implement.** Append to `internal/juggler/rpc.go`:

```go
const (
	MethodListModels        = "ListModels"
	MethodAddRemoteModel     = "AddRemoteModel"
	MethodRemoveRemoteModel  = "RemoveRemoteModel"
	MethodResolveModel       = "ResolveModel"
)

// ModelKind distinguishes a spawned local instance from a static remote
// endpoint in the unified model listing.
type ModelKind string

const (
	ModelKindLocal  ModelKind = "local"
	ModelKindRemote ModelKind = "remote"
)

// Model is one entry in the unified ListModels view. Token is
// deliberately omitted here — only ResolveModel returns a resolved
// secret, keeping list output safe to print/log.
type Model struct {
	Name  string    `json:"name"`
	Kind  ModelKind `json:"kind"`
	Style string    `json:"style,omitempty"` // remote only
}

type (
	ListModelsParams struct{}
	ListModelsResult struct {
		Models []Model `json:"models"`
	}
)

// AddRemoteModelParams registers (or overwrites, by Name) a remote model.
type AddRemoteModelParams struct {
	Name  string `json:"name"`
	Style string `json:"style"`
	URL   string `json:"url"`
	Token string `json:"token"`
}
type AddRemoteModelResult struct{}

// RemoveRemoteModelParams deletes a remote model by name. No result type;
// the server returns a null result (mirrors StopInstance).
type RemoveRemoteModelParams struct {
	Name string `json:"name"`
}

// ResolveModelParams looks up name in the unified registry.
type ResolveModelParams struct {
	Name string `json:"name"`
}

// ResolveModelResult is what a consumer needs to actually talk to the
// model: for kind "remote", URL + the resolved Token (env-expanded
// server-side) + Style; for kind "local", just URL (the running
// instance's address — ResolveModel starts it if not already running).
type ResolveModelResult struct {
	Kind  ModelKind `json:"kind"`
	URL   string    `json:"url"`
	Token string    `json:"token,omitempty"`
	Style string    `json:"style,omitempty"`
}
```

**Step 4:** `just zz-explore/go-test ./internal/juggler/...` — PASS.

**Step 5: Commit:**
```
grit add internal/juggler/rpc.go internal/juggler/rpc_test.go
grit commit -m "feat(juggler): RPC wire types for ListModels/AddRemoteModel/RemoveRemoteModel/ResolveModel"
```

---

### Task 3: `internal/juggler/client.go` — client wrapper methods

**Promotion criteria:** N/A.

**Files:**
- Modify: `internal/juggler/client.go`
- Test: `internal/juggler/client_test.go` (extend existing — it likely
  spins up a fake server over a UDS pair; read it first and match that
  fixture)

**Step 1: Write failing test.** Following whatever fixture
`client_test.go` already uses for the existing methods (e.g.
`TestListInstances`), add one for a new method, e.g.:

```go
func TestClientListModels(t *testing.T) {
	// use the same fake-server harness the existing tests use; have it
	// answer MethodListModels with a canned ListModelsResult and assert
	// the client returns it unchanged.
}
```

**Step 2:** `just zz-explore/go-test ./internal/juggler/... -run TestClientListModels` — FAIL.

**Step 3: Implement.** Append to `internal/juggler/client.go`:

```go
func (c *Client) ListModels(ctx context.Context) (ListModelsResult, error) {
	var r ListModelsResult
	return r, c.call(ctx, MethodListModels, ListModelsParams{}, &r)
}

func (c *Client) AddRemoteModel(ctx context.Context, p AddRemoteModelParams) error {
	return c.call(ctx, MethodAddRemoteModel, p, nil)
}

func (c *Client) RemoveRemoteModel(ctx context.Context, p RemoveRemoteModelParams) error {
	return c.call(ctx, MethodRemoveRemoteModel, p, nil)
}

func (c *Client) ResolveModel(ctx context.Context, p ResolveModelParams) (ResolveModelResult, error) {
	var r ResolveModelResult
	return r, c.call(ctx, MethodResolveModel, p, &r)
}
```

**Step 4:** `just zz-explore/go-test ./internal/juggler/...` — PASS.

**Step 5: Commit:**
```
grit add internal/juggler/client.go internal/juggler/client_test.go
grit commit -m "feat(juggler): client methods for the model-registry RPCs"
```

---

### Task 4: `cmd/juggler/server.go` — dispatch the new methods

**Promotion criteria:** N/A.

**Files:**
- Modify: `cmd/juggler/server.go`
- Test: `cmd/juggler/server_test.go` (extend — read it first for the
  fixture it uses to drive `dispatch` directly, likely constructing a
  `server{}` with a fake `Launcher` and calling `s.dispatch(envelope)`)

**Step 1: Write failing tests.** Add cases to `server_test.go` covering:
`ListModels` returns local (from a temp `modelsDir` with a fake `.gguf`
file) + remote (from a temp `remoteModelsPath` with one entry) merged;
`AddRemoteModel` then `ListModels` shows it; `RemoveRemoteModel` then
`ListModels` no longer shows it; `ResolveModel` on a remote name returns
its (env-expanded) url/token; `ResolveModel` on an unknown name returns
error code `-32001` (mirror `GetInstance`'s not-found convention);
`ResolveModel` on a name matching an already-running instance's alias
returns `{kind:"local", url: "bind:port"}` without calling the launcher
again.

```go
func TestDispatchResolveModelUnknown(t *testing.T) {
	s := newServer(rm.NewRegistry(), fakeLauncher{})
	s.remoteModelsPath = filepath.Join(t.TempDir(), "models.toml")
	env := rm.Envelope{JSONRPC: "2.0", ID: "1", Method: rm.MethodResolveModel,
		Params: mustJSON(t, rm.ResolveModelParams{Name: "nope"})}
	resp := s.dispatch(env)
	if resp.Error == nil || resp.Error.Code != -32001 {
		t.Fatalf("resp = %#v, want -32001", resp)
	}
}
```

(Add the other cases named above in the same style; check `mustJSON` or
equivalent already exists in the test file, or add a small helper.)

**Step 2:** `just zz-explore/go-test ./cmd/juggler/... -run TestDispatchResolveModel` —
FAIL (unknown field `remoteModelsPath`, method not dispatched).

**Step 3: Implement.**

Add a field to `server` (alongside `modelsDir`):

```go
	// remoteModelsPath is where the remote-model registry file lives.
	// Empty in zero-value servers used by tests that don't exercise the
	// new methods; newServer sets it via juggler.RemoteModelsPath().
	remoteModelsPath string
```

In `newServer`, after `modelsDir: jugglermodels.Dir(),` add (best-effort —
an error here shouldn't fail daemon startup, just leave the path empty and
let the RPC calls surface it):

```go
	remoteModelsPath, _ := rm.RemoteModelsPath()
```
and set `remoteModelsPath: remoteModelsPath,` in the returned `&server{...}`.

Add dispatch cases (insert before `default:` in `dispatch`):

```go
	case rm.MethodListModels:
		local, err := jugglermodels.List(s.modelsDir)
		if err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("list local models: %v", err))
		}
		remote, err := rm.LoadRemoteModels(s.remoteModelsPath)
		if err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("list remote models: %v", err))
		}
		models := make([]rm.Model, 0, len(local)+len(remote))
		for _, name := range local {
			models = append(models, rm.Model{Name: name, Kind: rm.ModelKindLocal})
		}
		for _, m := range remote {
			models = append(models, rm.Model{Name: m.Name, Kind: rm.ModelKindRemote, Style: m.Style})
		}
		return rpcResult(req.ID, rm.ListModelsResult{Models: models})

	case rm.MethodAddRemoteModel:
		var p rm.AddRemoteModelParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcError(req.ID, -32602, fmt.Sprintf("invalid params: %v", err))
		}
		remote, err := rm.LoadRemoteModels(s.remoteModelsPath)
		if err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("load remote models: %v", err))
		}
		remote = rm.UpsertRemoteModel(remote, rm.RemoteModel{
			Name: p.Name, Style: p.Style, URL: p.URL, Token: p.Token,
		})
		if err := rm.SaveRemoteModels(s.remoteModelsPath, remote); err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("save remote models: %v", err))
		}
		return rpcResult(req.ID, rm.AddRemoteModelResult{})

	case rm.MethodRemoveRemoteModel:
		var p rm.RemoveRemoteModelParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcError(req.ID, -32602, fmt.Sprintf("invalid params: %v", err))
		}
		remote, err := rm.LoadRemoteModels(s.remoteModelsPath)
		if err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("load remote models: %v", err))
		}
		remaining, found := rm.RemoveRemoteModel(remote, p.Name)
		if !found {
			return rpcError(req.ID, -32001, fmt.Sprintf("remote model %q not found", p.Name))
		}
		if err := rm.SaveRemoteModels(s.remoteModelsPath, remaining); err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("save remote models: %v", err))
		}
		return rm.Envelope{JSONRPC: "2.0", ID: req.ID, Result: []byte("null")}

	case rm.MethodResolveModel:
		var p rm.ResolveModelParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcError(req.ID, -32602, fmt.Sprintf("invalid params: %v", err))
		}
		remote, err := rm.LoadRemoteModels(s.remoteModelsPath)
		if err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("load remote models: %v", err))
		}
		for _, m := range remote {
			if m.Name == p.Name {
				return rpcResult(req.ID, rm.ResolveModelResult{
					Kind: rm.ModelKindRemote, Style: m.Style,
					URL: os.ExpandEnv(m.URL), Token: os.ExpandEnv(m.Token),
				})
			}
		}
		// Not a remote entry — try local. Reuse a running instance if the
		// alias is already up; otherwise fall through to the same start
		// path MethodStartInstance uses.
		if in, ok := s.registry.Get(p.Name); ok {
			return rpcResult(req.ID, rm.ResolveModelResult{
				Kind: rm.ModelKindLocal, URL: fmt.Sprintf("http://%s:%d", in.Bind, in.Port),
			})
		}
		local, err := jugglermodels.List(s.modelsDir)
		if err != nil {
			return rpcError(req.ID, -32000, fmt.Sprintf("list local models: %v", err))
		}
		found := false
		for _, name := range local {
			if name == p.Name {
				found = true
				break
			}
		}
		if !found {
			return rpcError(req.ID, -32001, fmt.Sprintf("model %q not found (local or remote)", p.Name))
		}
		if s.launcher == nil {
			return rpcError(req.ID, -32000, "launcher not configured")
		}
		in, err := s.launcher.Start(context.Background(), rm.StartInstanceParams{Alias: p.Name, Model: p.Name})
		if err != nil {
			return rpcError(req.ID, -32000, err.Error())
		}
		return rpcResult(req.ID, rm.ResolveModelResult{
			Kind: rm.ModelKindLocal, URL: fmt.Sprintf("http://%s:%d", in.Bind, in.Port),
		})
```

**Step 4:** `just zz-explore/go-test ./cmd/juggler/...` — PASS.

**Step 5: Commit:**
```
grit add cmd/juggler/server.go cmd/juggler/server_test.go
grit commit -m "feat(juggler): dispatch ListModels/AddRemoteModel/RemoveRemoteModel/ResolveModel"
```

---

### Task 5: `cmd/juggler` — `juggler model list|add|remove` CLI

**Promotion criteria:** N/A. Deliberately a **new**, separate subcommand
from the existing `juggler models` (plural) — that one stays local-GGUF-only,
one-name-per-line, matching the pre-juggler `circusmodels.List` shell-pipeline
contract its own comment calls out. Repurposing it would be a silent
behavior break for existing scripts.

**Files:**
- Create: `cmd/juggler/model.go`
- Modify: `cmd/juggler/main.go` (dispatch `case "model":`, update the
  usage string)
- Test: `cmd/juggler/model_test.go`

**Step 1: Write failing tests.** Follow the style of `list_test.go` /
`start_test.go` (a fake `rm.Client`-shaped test double, or whatever
harness those already use — read one first). At minimum:

```go
func TestCmdModelListFormatsUnion(t *testing.T) {
	// fake client returns a ListModelsResult with one local + one remote
	// entry; assert the printed table has both, tagged by kind.
}

func TestCmdModelAddRequiresURLAndToken(t *testing.T) {
	// juggler model add <name> --style anthropic with no --url: usage
	// error, no RPC call made.
}
```

**Step 2:** `just zz-explore/go-test ./cmd/juggler/... -run TestCmdModel` — FAIL.

**Step 3: Implement.** `cmd/juggler/model.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	rm "github.com/amarbel-llc/clown/internal/juggler"
)

const modelUsage = "usage: juggler model <list|add <name> --style <anthropic|openai-compat> --url <url> --token <token>|remove <name>>"

func cmdModel(cli *rm.Client, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, modelUsage)
		return 2
	}
	switch args[0] {
	case "list":
		return cmdModelList(cli)
	case "add":
		return cmdModelAdd(cli, args[1:])
	case "remove":
		return cmdModelRemove(cli, args[1:])
	default:
		fmt.Fprintln(os.Stderr, modelUsage)
		return 2
	}
}

func cmdModelList(cli *rm.Client) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := cli.ListModels(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "juggler: model list: %v\n", err)
		return 1
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tSTYLE")
	for _, m := range res.Models {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", m.Name, m.Kind, m.Style)
	}
	return 0
	// NOTE: tw.Flush() error is deliberately swallowed here to match
	// printInstanceTable's existing posture in this file — revisit if a
	// reviewer wants it surfaced.
}

func cmdModelAdd(cli *rm.Client, args []string) int {
	var name, style, url, token string
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, modelUsage)
		return 2
	}
	name = args[0]
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		switch {
		case rest[i] == "--style" && i+1 < len(rest):
			style = rest[i+1]
			i++
		case rest[i] == "--url" && i+1 < len(rest):
			url = rest[i+1]
			i++
		case rest[i] == "--token" && i+1 < len(rest):
			token = rest[i+1]
			i++
		case strings.HasPrefix(rest[i], "--style="):
			style = strings.TrimPrefix(rest[i], "--style=")
		case strings.HasPrefix(rest[i], "--url="):
			url = strings.TrimPrefix(rest[i], "--url=")
		case strings.HasPrefix(rest[i], "--token="):
			token = strings.TrimPrefix(rest[i], "--token=")
		}
	}
	if style == "" || url == "" || token == "" {
		fmt.Fprintln(os.Stderr, modelUsage)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cli.AddRemoteModel(ctx, rm.AddRemoteModelParams{Name: name, Style: style, URL: url, Token: token}); err != nil {
		fmt.Fprintf(os.Stderr, "juggler: model add: %v\n", err)
		return 1
	}
	fmt.Printf("juggler: registered remote model %q\n", name)
	return 0
}

func cmdModelRemove(cli *rm.Client, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, modelUsage)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cli.RemoveRemoteModel(ctx, rm.RemoveRemoteModelParams{Name: args[0]}); err != nil {
		fmt.Fprintf(os.Stderr, "juggler: model remove: %v\n", err)
		return 1
	}
	fmt.Printf("juggler: removed remote model %q\n", args[0])
	return 0
}
```

In `cmd/juggler/main.go`, add to the `switch args[0]` in `run`:

```go
	case "model":
		return withClient(func(cli *rm.Client) int { return cmdModel(cli, args[1:]) })
```

and update the top-level usage string to mention `model`.

**Step 4:** `just zz-explore/go-test ./cmd/juggler/...` — PASS. Also
`go build ./cmd/juggler/...` to catch any wiring mistakes the tests miss.

**Step 5: Commit:**
```
grit add cmd/juggler/model.go cmd/juggler/model_test.go cmd/juggler/main.go
grit commit -m "feat(juggler): juggler model list/add/remove CLI"
```

---

### Task 6: `cmd/clown` — juggler-resolution helper

**Promotion criteria:** N/A.

**Files:**
- Create: `cmd/clown/jugglerresolve.go`
- Test: `cmd/clown/jugglerresolve_test.go`

**Step 1: Write failing tests.** These need a real (or fake-socket) juggler
client. Follow whatever pattern `internal/juggler/client_test.go` uses to
stand up a fake server on a temp UDS socket, and point `JUGGLER_SOCKET` at
it via `t.Setenv`:

```go
func TestResolveViaJugglerUnreachableDaemon(t *testing.T) {
	t.Setenv("JUGGLER_SOCKET", filepath.Join(t.TempDir(), "no-such.sock"))
	_, err := resolveViaJuggler("anything")
	if err == nil || !strings.Contains(err.Error(), "juggler daemon") {
		t.Fatalf("want a daemon-unreachable error, got %v", err)
	}
}
```

(A second test standing up a fake server that answers `ResolveModel`
successfully belongs here too — reuse the fake-server harness from
`internal/juggler/client_test.go` if it's exported/reusable, or a minimal
local copy.)

**Step 2:** `just zz-explore/go-test ./cmd/clown/... -run TestResolveViaJuggler` — FAIL.

**Step 3: Implement.** `cmd/clown/jugglerresolve.go`:

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/amarbel-llc/clown/internal/juggler"
)

// resolveViaJuggler asks the juggler daemon to resolve modelName to a
// runnable endpoint. The 90s timeout accommodates a local model that
// needs to be started (mirrors cmd/juggler's own cmdStart timeout);
// remote resolution returns near-instantly.
func resolveViaJuggler(modelName string) (juggler.ResolveModelResult, error) {
	socket, err := juggler.SocketPath()
	if err != nil {
		return juggler.ResolveModelResult{}, err
	}
	cli, err := juggler.NewClient(socket)
	if err != nil {
		return juggler.ResolveModelResult{}, fmt.Errorf(
			"juggler daemon unreachable (socket %s): %w — start it: juggler daemon", socket, err)
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := cli.ResolveModel(ctx, juggler.ResolveModelParams{Name: modelName})
	if err != nil {
		return juggler.ResolveModelResult{}, fmt.Errorf("resolve model %q: %w", modelName, err)
	}
	return res, nil
}

// jugglerModelKind is a side-effect-free lookup (ListModels — never starts
// an instance) used by the subagent-model compatibility check in
// applyNamedProfile, so a plain literal model string never accidentally
// spawns a local juggler instance just because it happens to collide with
// a registered alias name.
func jugglerModelKind(modelName string) (juggler.ModelKind, bool) {
	socket, err := juggler.SocketPath()
	if err != nil {
		return "", false
	}
	cli, err := juggler.NewClient(socket)
	if err != nil {
		return "", false
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := cli.ListModels(ctx)
	if err != nil {
		return "", false
	}
	for _, m := range res.Models {
		if m.Name == modelName {
			return m.Kind, true
		}
	}
	return "", false
}
```

**Step 4:** `just zz-explore/go-test ./cmd/clown/...` — PASS.

**Step 5: Commit:**
```
grit add cmd/clown/jugglerresolve.go cmd/clown/jugglerresolve_test.go
grit commit -m "feat(clown): juggler-resolution helpers for applyNamedProfile"
```

---

### Task 7: `cmd/clown/main.go` — wire `applyNamedProfile` through juggler

**Promotion criteria:** N/A (inline URL/Token path is permanent, not being
retired — see the design doc's rollback section).

**Files:**
- Modify: `cmd/clown/main.go` (`applyNamedProfile`, ~line 603)
- Test: `cmd/clown/main_test.go`

**Step 1: Write failing tests.** These need a fake juggler daemon on a
temp socket (same harness as Task 6). Cover:

```go
func TestApplyNamedProfileJugglerRemoteFallback(t *testing.T) {
	// no inline URL/Token; profile.Model names a registered remote model;
	// fake server's ResolveModel returns {kind:"remote", url, token};
	// assert ANTHROPIC_BASE_URL/AUTH_TOKEN/API_KEY="" set from the
	// resolved values, same as the inline-gateway test already covers.
}

func TestApplyNamedProfileJugglerLocalFallback(t *testing.T) {
	// backend:"local", profile.Model names a local alias; fake server's
	// ResolveModel returns {kind:"local", url}; assert ANTHROPIC_BASE_URL
	// set to that url, plus ANTHROPIC_AUTH_TOKEN=dummy,
	// ANTHROPIC_API_KEY=dummy, ANTHROPIC_CUSTOM_MODEL_OPTION containing
	// the model name (mirrors the justfile's smoke-clown-against-tailnet
	// recipe — the one already-proven-working local-model env shape).
}

func TestApplyNamedProfileSubagentModelSameEndpointOK(t *testing.T) {
	// main profile resolves (inline or juggler) to url X; Env map has
	// CLAUDE_CODE_SUBAGENT_MODEL naming a remote juggler model that also
	// resolves to url X; assert no error and the env var is set.
}

func TestApplyNamedProfileSubagentModelDifferentEndpointErrors(t *testing.T) {
	// main resolves to url X; subagent-model env value names a juggler
	// model resolving to url Y != X; assert applyNamedProfile returns an
	// error mentioning both endpoints.
}

func TestApplyNamedProfileSubagentModelLiteralUnaffected(t *testing.T) {
	// no juggler daemon reachable at all (or JUGGLER_SOCKET points
	// nowhere); Env["CLAUDE_CODE_SUBAGENT_MODEL"] = "claude-haiku-4-5"
	// (a plain literal); assert it's set verbatim, no error, and —
	// importantly — no instance-start side effect (can't observe this
	// directly without a daemon, but the absence of an error IS the
	// signal: jugglerModelKind's error path silently falls through).
}
```

**Step 2:** `just zz-explore/go-test ./cmd/clown/... -run TestApplyNamedProfile` — FAIL.

**Step 3: Implement.** Replace `applyNamedProfile` in `cmd/clown/main.go`:

```go
// applyNamedProfile applies a selected named profile (--profile, the clownfile
// pin, or the interactive picker) to the run. Unlike the clownfile [profile]
// defaults layer (applyClownfileProfile, only-if-unset), a named profile is an
// explicit user selection, so its gateway env is authoritative: picking
// claude-openrouter must mean OpenRouter even when the shell exports
// ANTHROPIC_* for something else. The generic Env map stays only-if-unset.
// Deliberately does not touch flags.backend — that is the tent container
// backend (podman|lima), a different axis from the profile's API backend.
//
// Resolution order for claude+gateway/local profiles: inline URL/Token
// first (unchanged, zero-migration path — docs/plans/2026-07-07-juggler-
// model-registry-design.md's rollback story), then juggler resolution via
// profile.Model when there's no inline URL/Token.
func applyNamedProfile(flags *parsedFlags, p profile.Profile) error {
	if p.Model != "" && providerTakesModelFlag(flags.provider) && claudeFlagValue(flags.forwarded, "--model") == "" {
		flags.forwarded = append([]string{"--model", p.Model}, flags.forwarded...)
	}
	if p.Provider == "claude" {
		switch {
		case p.Backend == "gateway" && p.URL != "":
			url := clownfile.ResolveEnv(p.URL)
			token := clownfile.ResolveEnv(p.Token)
			if url == "" {
				return fmt.Errorf("profile %q: url %q resolved empty", p.Name, p.URL)
			}
			if token == "" {
				return fmt.Errorf("profile %q: token %q resolved empty (is the referenced variable exported?)", p.Name, p.Token)
			}
			_ = os.Setenv("ANTHROPIC_BASE_URL", url)
			_ = os.Setenv("ANTHROPIC_AUTH_TOKEN", token)
			// Present-but-empty is the gateway contract: unset makes claude fall
			// back to Anthropic auth and conflict with the auth token.
			_ = os.Setenv("ANTHROPIC_API_KEY", "")
		case (p.Backend == "gateway" || p.Backend == "local") && p.Model != "":
			resolved, err := resolveViaJuggler(p.Model)
			if err != nil {
				return fmt.Errorf("profile %q: %w", p.Name, err)
			}
			if resolved.Kind == juggler.ModelKindRemote {
				if resolved.URL == "" || resolved.Token == "" {
					return fmt.Errorf("profile %q: juggler model %q resolved with an empty url or token", p.Name, p.Model)
				}
				_ = os.Setenv("ANTHROPIC_BASE_URL", resolved.URL)
				_ = os.Setenv("ANTHROPIC_AUTH_TOKEN", resolved.Token)
				_ = os.Setenv("ANTHROPIC_API_KEY", "")
			} else { // local
				_ = os.Setenv("ANTHROPIC_BASE_URL", resolved.URL)
				_ = os.Setenv("ANTHROPIC_AUTH_TOKEN", "dummy")
				_ = os.Setenv("ANTHROPIC_API_KEY", "dummy")
				_ = os.Setenv("ANTHROPIC_CUSTOM_MODEL_OPTION",
					fmt.Sprintf(`{"model":%q,"max_tokens":2048}`, p.Model))
			}
		}
	}
	mainURL := os.Getenv("ANTHROPIC_BASE_URL")
	for k, v := range p.Env {
		if os.Getenv(k) != "" {
			continue
		}
		value := v
		if isSubagentModelKey(k) {
			if kind, known := jugglerModelKind(v); known {
				resolved, err := resolveViaJuggler(v)
				if err != nil {
					return fmt.Errorf("%s=%q: %w", k, v, err)
				}
				if resolved.URL != mainURL {
					return fmt.Errorf(
						"%s=%q resolves to a different endpoint (%s) than the main model (%s) — "+
							"Claude Code shares one ANTHROPIC_BASE_URL per process, so this pairing can't "+
							"launch yet (see docs/plans/2026-07-07-juggler-model-registry-design.md)",
						k, v, resolved.URL, mainURL)
				}
				_ = kind // same-endpoint check above is what matters; kind is informational
			}
			// unknown to jugglerModelKind (no daemon, or not registered): treat
			// as a literal model slug, same as before this feature existed.
		}
		_ = os.Setenv(k, clownfile.ResolveEnv(value))
	}
	return nil
}

// isSubagentModelKey reports whether k is one of the env vars that select
// a subagent/tier model (docs: man/man7/claude-code-env.7 § MODEL SELECTION).
func isSubagentModelKey(k string) bool {
	switch k {
	case "CLAUDE_CODE_SUBAGENT_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":
		return true
	}
	return false
}
```

Add the new import: `"github.com/amarbel-llc/clown/internal/juggler"`.

**Step 4:** `just zz-explore/go-test ./cmd/clown/...` — PASS, including
the existing `TestApplyNamedProfileGatewayEnvAuthoritative` /
`TestApplyNamedProfileEmptyTokenRef` / `TestApplyNamedProfileModelInjection` /
`TestApplyNamedProfileEnvMapOnlyIfUnset` tests from last session (regression
check — the inline-URL/Token path must be byte-for-byte unchanged).

**Step 5: Commit:**
```
grit add cmd/clown/main.go cmd/clown/main_test.go
grit commit -m "feat(clown): resolve gateway/local profiles through juggler; subagent-model endpoint check"
```

---

### Task 8: bats coverage

**Files:**
- Modify: `zz-tests_bats/juggler.bats` (read it first for the existing
  fake-llama-server / `JUGGLER_SOCKET` fixture pattern)

**Step 1:** Add cases (adapt setup to match the file's existing
conventions):

```bash
@test "juggler model add then list shows the remote entry" {
  run "$JUGGLER_BIN" model add test-remote --style anthropic --url https://example.test/api --token sk-test
  assert_success
  run "$JUGGLER_BIN" model list
  assert_success
  assert_output --partial "test-remote"
  assert_output --partial "remote"
}

@test "juggler model remove deletes the entry" {
  run "$JUGGLER_BIN" model add to-remove --style anthropic --url https://example.test --token x
  assert_success
  run "$JUGGLER_BIN" model remove to-remove
  assert_success
  run "$JUGGLER_BIN" model list
  refute_output --partial "to-remove"
}
```

Point `JUGGLER_MODELS_PATH` (or `HOME`, if the fixture already does that
for the socket) at a bats temp dir so remote models never leak in/out of a
test.

**Step 2:** Run the bats lane the paved-path way. Remember the dirty-tree
gotcha from the header — `grit add` this file before building:

```
grit add zz-tests_bats/juggler.bats
```
then verify with the `chix.build` tool against `.#bats-default` (or
`just test-plugin-host` / `just test-stdio-bridge`, which both build the
same lane).

**Step 3: Commit:**
```
grit commit -m "test(juggler): bats coverage for model add/list/remove"
```

---

### Task 9: docs

**Files:**
- Modify: `AGENTS.md` (juggler section — describe the new model registry
  and its relationship to the profile registry; correct the stale
  `--provider juggler` / handshake description if it now overlaps)
- Modify: `man/man1/juggler.1` and/or `man/man7/juggler.7` (document
  `juggler model list|add|remove` and the `ResolveModel`/`ListModels`/etc.
  RPC methods, following the existing page's style)
- Modify: `docs/plans/2026-07-07-juggler-model-registry-design.md`
  (mark implemented, if the doc convention in this repo does that — check
  a prior merged design doc for the pattern, e.g. whether
  `2026-07-06-openrouter-profiles-design.md` got a post-merge edit)

**Steps:** update, `go build ./cmd/juggler/... ./cmd/clown/...`, commit:
`docs: juggler model registry, profile resolution through juggler`.

Run `@eng:doc-drift` against the full diff before merge (pre-merge
attestation requires it anyway) — in particular check whether this
finally resolves any part of FDR-0011's staleness (it implements one piece
of it) without accidentally contradicting the rest of that FDR's still-open
scope.

---

## Execution notes

- Fresh subagent per task (`eng:subagent-driven-development`), code review
  between tasks.
- Tasks 1→2→3→4→5 (juggler side) and 6→7 (clown side) are each internally
  ordered by dependency; 6/7 depend on 1-4 existing. Task 8/9 last.
- Do not run full `just` at the end — `merge-this-session`'s pre-merge
  hook is the CI lane.
- New files must be `grit add`ed before any `nix build` (dirty-tree builds
  only see tracked files — this bit the previous session's bats check).
