# Non-Claude Provider Parity (Phases 0+1) Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** Give `--provider opencode` and `--provider crush` clown's MCP plugin tools, after first making clown's generated provider config authoritative so a repo-local config cannot hijack a clown-owned MCP server name.

**Architecture:** Phase 0 suppresses/outranks repo-local provider config (`OPENCODE_DISABLE_PROJECT_CONFIG` for opencode; crush's highest-priority workspace config slot at `<data-dir>/crush.json`). Phase 1 refactors the plugin-host pipeline's Claude-specific tail behind a `pluginBinding` interface, then adds a `configFileBinding` that opencode and crush share, differing only in which JSON writer they hand it.

**Tech Stack:** Go 1.x, `internal/pluginhost`, `internal/clownfile`, Nix flakes (`just build` = `nix build --show-trace`).

**Rollback:** Phase 0 is gated on a clownfile key defaulting to on — set `[providers] hermetic-config = false` to restore today's behavior. Phase 1's new surface is already covered by the existing `--disable-clown-protocol` / `CLOWN_DISABLE_CLOWN_PROTOCOL=1` escape hatch, which bypasses the plugin host entirely (for opencode/crush that means "behave exactly as before"). `claudeBinding` is a pure extraction, so the claude path has no dual-architecture period — its existing tests are the control.

**Design record:** `docs/plans/2026-07-27-non-claude-provider-parity-design.md`. Read it before starting; it contains the evidence table distinguishing verified-by-experiment claims from source-read inferences.

---

## Before you start — repo gotchas that will bite you

1. **`just build` is the only fully-trustworthy check.** `just build-go` and `just update-gomod2nix` can fail or hang for reasons unrelated to your change (clown#174), because clown consumes the external `ringmaster` Go module through a Nix-injected `replace` that only exists inside `nix build`/`mkGoEnv`. If `go test ./cmd/clown/` reports "inconsistent vendoring", that is **not** your bug — use `just test-go` (or `just zz-explore/run-go-test ./cmd/clown/`) which runs inside the dev shell.
2. **New untracked files are invisible to `nix build`.** `git add` every new file before running `just build`, or you get a misleading "undefined: X". Staging is enough; no commit needed.
3. `internal/pluginhost` does **not** import ringmaster, so plain `go test ./internal/pluginhost/` is safe. `cmd/clown` does.
4. Format with `nix fmt` before committing (`just lint-fmt` checks it).

---

## Task 1: clownfile `[providers] hermetic-config` toggle

**Promotion criteria:** N/A — this is the rollback switch itself; it stays.

**Files:**
- Modify: `internal/clownfile/clownfile.go` (add type near `Messaging`, ~line 202; add field to `Clownfile`, ~line 266)
- Test: `internal/clownfile/clownfile_test.go`

**Step 1: Write the failing test**

```go
func TestProviders_HermeticConfigEnabled_DefaultsOn(t *testing.T) {
	var p Providers
	if !p.HermeticConfigEnabled() {
		t.Error("unset hermetic-config should default to ON (fail-closed)")
	}
}

func TestProviders_HermeticConfigEnabled_ExplicitFalse(t *testing.T) {
	off := false
	p := Providers{HermeticConfig: &off}
	if p.HermeticConfigEnabled() {
		t.Error("explicit false must disable")
	}
}
```

**Step 2: Run it and verify it fails**

Run: `just zz-explore/run-go-test ./internal/clownfile/`
Expected: FAIL — `undefined: Providers`

**Step 3: Implement**

Note the polarity difference from `Attach.PtySuspend`: that one is nil = off, this one is **nil = on**, because the safe default is to suppress. Keep it a `*bool` anyway so a deeper clownfile can override an `false` back to `true`.

```go
// Providers holds cross-provider launch policy.
type Providers struct {
	// HermeticConfig makes clown's generated provider config authoritative by
	// suppressing (opencode) or outranking (crush) repo-local config files.
	//
	// Verified 2026-07-27: without this, a repo-local opencode.json REPLACES a
	// same-named entry in clown's `mcp` map, letting any repository silently
	// repoint a clown-managed MCP server at a URL of its choosing. See
	// docs/plans/2026-07-27-non-claude-provider-parity-design.md, finding 0.
	//
	// A *bool, like Attach.PtySuspend — but with the opposite default: nil
	// (unset) means ON, because the failure mode of being off is a hijack.
	HermeticConfig *bool `toml:"hermetic-config"`
}

// HermeticConfigEnabled reports whether clown's generated provider config
// should outrank repo-local config. Unset defaults to true.
func (p Providers) HermeticConfigEnabled() bool {
	return p.HermeticConfig == nil || *p.HermeticConfig
}
```

Add to `Clownfile`:

```go
type Clownfile struct {
	Profile   Profile   `toml:"profile"`
	Attach    Attach    `toml:"attach"`
	Messaging Messaging `toml:"messaging"`
	Providers Providers `toml:"providers"`
}
```

**Step 4: Verify it passes**

Run: `just zz-explore/run-go-test ./internal/clownfile/`
Expected: PASS

**Step 5: Wire it into the clownfile merge**

Find how `Attach.PtySuspend` is carried through clownfile merging (the nearest/deepest clownfile wins) and mirror it exactly for `Providers.HermeticConfig`. Add a test asserting a deeper clownfile's `hermetic-config = false` overrides a shallower `true`.

**Step 6: Commit**

```bash
git add internal/clownfile/
git commit -m "clownfile: add [providers] hermetic-config toggle (#202)"
```

---

## Task 2: opencode — suppress repo-local config

**Promotion criteria:** N/A.

**Files:**
- Modify: `cmd/clown/opencode.go` (`runOpencode`, the `cmd.Env` line ~366)
- Test: `cmd/clown/opencode_test.go`

`runOpencode` currently does:

```go
cmd.Env = append(os.Environ(), "OPENCODE_CONFIG="+cfgPath)
```

**Step 1: Extract the env construction so it is testable without exec**

Add to `opencode.go`:

```go
// opencodeEnv returns the additional environment entries clown sets for an
// opencode launch. OPENCODE_DISABLE_PROJECT_CONFIG is what makes cfgPath
// authoritative: opencode otherwise merges every project-level opencode.json
// AFTER the OPENCODE_CONFIG file (packages/opencode/src/config/config.ts:406-409),
// which lets a repo-local config replace a clown-owned `mcp` entry outright.
// The user's own GLOBAL config still merges, and merges before clown's, so
// clown still wins — only the per-repo file is suppressed.
func opencodeEnv(cfgPath string, hermetic bool) []string {
	env := []string{"OPENCODE_CONFIG=" + cfgPath}
	if hermetic {
		env = append(env, "OPENCODE_DISABLE_PROJECT_CONFIG=1")
	}
	return env
}
```

**Step 2: Write the failing test**

```go
func TestOpencodeEnv_HermeticSuppressesProjectConfig(t *testing.T) {
	got := opencodeEnv("/tmp/x/opencode.json", true)
	want := []string{"OPENCODE_CONFIG=/tmp/x/opencode.json", "OPENCODE_DISABLE_PROJECT_CONFIG=1"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestOpencodeEnv_NonHermeticOmitsSuppression(t *testing.T) {
	got := opencodeEnv("/tmp/x/opencode.json", false)
	for _, e := range got {
		if strings.HasPrefix(e, "OPENCODE_DISABLE_PROJECT_CONFIG") {
			t.Errorf("suppression must not be set when hermetic=false: %v", got)
		}
	}
}
```

**Step 3: Run, verify fail, implement, verify pass**

Run: `just zz-explore/run-go-test ./cmd/clown/`

**Step 4: Use it in `runOpencode`**

```go
cmd.Env = append(os.Environ(), opencodeEnv(cfgPath, hermetic)...)
```

`hermetic` comes from the resolved clownfile. `runOpencode`'s signature today is `runOpencode(opencodePath string, args []string, prof *profile.Profile) int` — it receives no `flags`. Thread the single bool through rather than the whole `parsedFlags`; Task 11 changes the signature more substantially and you do not want to do it twice.

**Step 5: Commit**

```bash
git add cmd/clown/opencode.go cmd/clown/opencode_test.go
git commit -m "opencode: suppress repo-local config so clown's is authoritative (#202)"
```

---

## Task 3: crush — stable data dir and workspace-slot config

**Promotion criteria:** N/A.

**Files:**
- Modify: `cmd/clown/crush.go` (header comment; `runCrush`)
- Test: `cmd/clown/crush_test.go`

**Background you must not get wrong.** crush merges, in order: system config, `GlobalConfig()` (what `CRUSH_GLOBAL_CONFIG` redirects), `GlobalConfigData()`, then every `crush.json`/`.crush.json` found walking up from cwd to the git root — and finally the **workspace** config at `<data-dir>/crush.json`, "loaded last so it has highest priority" (`internal/config/load.go:65-66`). There is no env var to disable the project walk. Verified experimentally that the workspace slot overrides a project config in both directions.

**Step 1: Fix the inaccurate comment**

`crush.go`'s header claims the call is "hermetic w.r.t. the user's own `~/.config/crush/crush.json`". That is true of *that one file* and false of hermeticity generally. Replace with an accurate description of the merge order above and of what phase 0 does about it.

**Step 2: Write the failing test for the data-dir resolver**

```go
func TestCrushDataDir_StableAcrossCalls(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	a, err := crushDataDir("/home/u/proj")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := crushDataDir("/home/u/proj")
	if a != b {
		t.Errorf("data dir must be stable for a given project: %q vs %q", a, b)
	}
}

func TestCrushDataDir_DistinctPerProject(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	a, _ := crushDataDir("/home/u/proj-a")
	b, _ := crushDataDir("/home/u/proj-b")
	if a == b {
		t.Errorf("distinct projects must not share a data dir: %q", a)
	}
}
```

**Step 3: Implement**

```go
// crushDataDir returns the stable, clown-owned crush data directory for a
// project. It MUST be stable across launches: --data-dir is where crush keeps
// its sessions, so a mkdtemp here would silently break `crush --continue` and
// session history on every run. It also holds crush's WORKSPACE config, the
// highest-priority slot in crush's merge order — which is how clown's config
// outranks a repo-local crush.json (internal/config/load.go:65-66).
func crushDataDir(projectDir string) (string, error) {
	base, err := os.UserHomeDir()
	// ... honor XDG_STATE_HOME, else ~/.local/state
	sum := sha256.Sum256([]byte(projectDir))
	return filepath.Join(base, "clown", "crush", hex.EncodeToString(sum[:8])), nil
}
```

Hash the project path rather than embedding it: paths contain characters that are awkward in directory names, and worktree paths are long. Keep the first 8 bytes — collision risk is negligible for this use and the directory stays readable.

**Step 4: Change `writeCrushConfig`'s call site**

`runCrush` currently writes into `tmpDir` and sets `CRUSH_GLOBAL_CONFIG=tmpDir`. Now write the same config into `crushDataDir(cwd)` as well (that is the workspace slot) and pass `--data-dir <thatDir>` in the argv when hermetic. Keep `CRUSH_GLOBAL_CONFIG` pointing at the temp dir as before — it is still the lower-priority copy and removing it is not part of this task.

**Step 5: Add a test asserting `--data-dir` appears in the argv when hermetic and not otherwise**

Extract the argv construction into a pure helper (`crushArgs(baseArgs []string, dataDir string, hermetic bool) []string`) so this is testable without exec, mirroring how `resolveCrushGateway` was pulled out of `runCrush` for testability.

**Step 6: Run tests, `just build`, commit**

```bash
git add cmd/clown/crush.go cmd/clown/crush_test.go
git commit -m "crush: write config to the workspace slot so it outranks repo-local config (#202)"
```

---

## Task 4: `pluginhost.ServerEntries` — flat keys, sanitized, collision-checked

**Promotion criteria:** N/A — additive; `CompileForClaude` is untouched.

**Files:**
- Modify: `internal/pluginhost/host.go` (add after `serverEntriesByPluginDir`, ~line 529)
- Test: `internal/pluginhost/host_test.go`

**Why a new accessor rather than `CompileForOpencode`/`CompileForCrush`:** the design rejects putting opencode's and crush's JSON schemas inside `pluginhost`. This returns neutral `MCPServerEntry` values; each provider's config writer in `cmd/clown` translates.

**Why sanitizing matters (do not skip this):** the two providers derive tool names from the config key by *different* rules. opencode sanitizes (`catalog.ts:117-119`, `[^a-zA-Z0-9_-]` → `_`); crush interpolates verbatim (`mcp-tools.go:58-60`, `fmt.Sprintf("mcp_%s_%s", ...)`). A `/` key is silently mangled by one and produces an invalid tool name under the other. Staying inside `[A-Za-z0-9_-]` makes opencode's sanitizer a no-op so both providers see the identical key.

**Step 1: Write the failing tests**

```go
func TestSanitizeMCPKey(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"moxy", "moxy"},
		{"clown-builtin-jobs", "clown-builtin-jobs"},
		{"a/b", "a_b"},
		{"a.b c", "a_b_c"},
	} {
		if got := sanitizeMCPKey(tc.in); got != tc.want {
			t.Errorf("sanitizeMCPKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestServerEntries_KeyIsPluginDoubleUnderscoreServer(t *testing.T) {
	// ... construct a Host with one started server for plugin "moxy", server "moxy"
	entries, err := host.ServerEntries(discovered)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := entries["moxy__moxy"]; !ok {
		t.Errorf("want key moxy__moxy, got keys %v", maps.Keys(entries))
	}
}

func TestServerEntries_CollisionIsAnError(t *testing.T) {
	// Two discovered servers whose sanitized keys are identical
	// (e.g. plugin "a/b" server "c" and plugin "a_b" server "c").
	_, err := host.ServerEntries(discovered)
	if err == nil {
		t.Fatal("expected a collision error rather than silent shadowing")
	}
	if !strings.Contains(err.Error(), "a_b__c") {
		t.Errorf("error should name the colliding key: %v", err)
	}
}
```

That last test is the important one. Silent shadowing here means one plugin's tools vanish with no diagnostic.

**Step 2: Run, verify fail**

Run: `go test ./internal/pluginhost/ -run 'ServerEntries|sanitizeMCPKey' -v`
(safe to run directly — this package does not import ringmaster)

**Step 3: Implement**

```go
// mcpKeyDisallowed matches every character that is not legal in a flat `mcp`
// map key. See ServerEntries for why the charset is this narrow.
var mcpKeyDisallowed = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func sanitizeMCPKey(s string) string {
	return mcpKeyDisallowed.ReplaceAllString(s, "_")
}

// ServerEntries returns a flat name→entry map for every running server, for
// providers whose config has a single `mcp` object (opencode, crush) rather
// than claude's per-plugin-dir namespacing.
//
// Keys are "<plugin>__<server>", each component sanitized to [A-Za-z0-9_-].
// Both providers derive tool names from this key but by different rules —
// opencode sanitizes, crush interpolates verbatim — so clown pre-sanitizes to
// the intersection, making opencode's sanitizer a no-op and keeping the two
// providers' tool names identical.
//
// A post-sanitization collision is an ERROR, not a last-write-wins: silently
// shadowing one plugin's servers with another's would make its tools
// disappear with no diagnostic.
func (h *Host) ServerEntries(discovered []DiscoveredServer) (map[string]MCPServerEntry, error) {
	// build origin map from discovered (mirror serverEntriesByPluginDir), then
	// for each started server in h.Servers compute
	//   key := sanitizeMCPKey(plugin) + "__" + sanitizeMCPKey(server)
	// erroring on a duplicate key, and set
	//   result[key] = h.serverEntryForManaged(srv)
}
```

**Step 4: Verify pass, commit**

```bash
git add internal/pluginhost/
git commit -m "pluginhost: add ServerEntries for flat-mcp-map providers (#202)"
```

---

## Task 5: `runProvider` gains `extraEnv`

**Promotion criteria:** N/A.

**Files:**
- Modify: `cmd/clown/main.go` (`runProvider`, ~line 1466; `cmd := exec.Command(...)`, ~line 1501; all call sites)

`runProvider` currently inherits `os.Environ()` with no additions. The config-file providers need `OPENCODE_CONFIG` / `CRUSH_GLOBAL_CONFIG` on the child.

**Step 1:** Add an `extraEnv []string` parameter. When non-empty, set `cmd.Env = append(os.Environ(), extraEnv...)`; when empty, leave `cmd.Env` nil so the current inheritance behavior is byte-identical.

**Step 2:** Update every existing call site to pass `nil`. There are several (`runWithPluginHost`, `runManaged`'s fallbacks).

**Step 3:** Also thread it through the `ptysuspend.Run` branch — that path builds its own process and must not silently drop the env.

**Step 4:** Run the full Go suite. Nothing should change.

Run: `just test-go`
Expected: PASS, no diffs in behavior.

**Step 5: Commit**

```bash
git add cmd/clown/main.go
git commit -m "clown: let runProvider carry extra child env (#202)"
```

---

## Task 6: the `pluginBinding` seam + `claudeBinding` (pure extraction)

**Promotion criteria:** N/A — `claudeBinding` *replaces* the inline claude tail rather than coexisting with it. The existing claude tests are the control: if any behavior changes, that is a bug in the extraction.

**Files:**
- Create: `cmd/clown/pluginbinding.go`
- Modify: `cmd/clown/main.go` (`runWithPluginHost` ~1384, `runManaged` ~1621)
- Test: `cmd/clown/pluginbinding_test.go`

**Step 1: Define the seam**

```go
// pluginBinding delivers the started MCP servers to a provider in that
// provider's native form. It owns everything downstream of "the servers are
// healthy": claude stages plugin dirs and gets --plugin-dir flags; opencode
// and crush get a generated JSON config plus an env var.
//
// Bind is called with the post-cheap-context server set, and ALSO on the
// fallback paths with a nil/empty set (nothing discovered, nothing healthy,
// everything deselected) — an implementation must treat that as "launch with
// no clown-managed MCP servers", not as an error.
type pluginBinding interface {
	Bind(host *pluginhost.Host, discovered []pluginhost.DiscoveredServer) (bindResult, error)
}

type bindResult struct {
	Args []string // final argv (excluding argv[0])
	Env  []string // additional env for the child; nil for none
}
```

**Step 2: `claudeBinding` is a verbatim move**

It holds `baseArgs`, `pluginDirs`, and the cheap-context bookkeeping, and its `Bind` performs exactly what `runManaged` lines 1717-1762 do today: `CompileForClaude`, the cheap-context dir exclusion, then `prependPluginDirs`. **Do not "improve" anything while moving it.**

**Step 3: Replace the three fallback paths**

`runWithPluginHost` (no plugins discovered) and `runManaged` (no healthy servers; everything deselected) each call `prependPluginDirs(baseArgs, pluginDirs, nil)` then `runProvider`. Each becomes `binding.Bind(host, nil)` then `runProvider`. Verify `claudeBinding.Bind(host, nil)` reproduces `prependPluginDirs(baseArgs, pluginDirs, nil)` exactly — add a test for precisely that:

```go
func TestClaudeBinding_NilServersMatchesLegacyFallback(t *testing.T) {
	b := &claudeBinding{baseArgs: []string{"--foo"}, pluginDirs: []string{"/a", "/b"}}
	got, err := b.Bind(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := prependPluginDirs([]string{"--foo"}, []string{"/a", "/b"}, nil)
	if !slices.Equal(got.Args, want) {
		t.Errorf("fallback argv drifted: got %v want %v", got.Args, want)
	}
}
```

**Step 4: Run the whole suite plus a real claude smoke**

Run: `just test-go` then `just build` then `./result/bin/clown --version`
Expected: PASS; claude behavior unchanged.

**Step 5: Commit**

```bash
git add cmd/clown/pluginbinding.go cmd/clown/pluginbinding_test.go cmd/clown/main.go
git commit -m "clown: extract the plugin-host tail behind a pluginBinding seam (#202)"
```

---

## Task 7: `configFileBinding`

**Promotion criteria:** N/A.

**Files:**
- Modify: `cmd/clown/pluginbinding.go`
- Test: `cmd/clown/pluginbinding_test.go`

```go
// configFileBinding is the pluginBinding for providers configured by a
// generated JSON file plus an env var (opencode, crush). It is shared by both;
// only writeConfig differs.
//
// writeConfig receives the flat entry map (possibly empty — see pluginBinding)
// and returns the env entries the child needs to find the file it wrote.
type configFileBinding struct {
	baseArgs    []string
	writeConfig func(mcp map[string]pluginhost.MCPServerEntry) ([]string, error)
}

func (b *configFileBinding) Bind(host *pluginhost.Host, discovered []pluginhost.DiscoveredServer) (bindResult, error) {
	var entries map[string]pluginhost.MCPServerEntry
	if host != nil && len(discovered) > 0 {
		var err error
		if entries, err = host.ServerEntries(discovered); err != nil {
			return bindResult{}, err
		}
	}
	env, err := b.writeConfig(entries)
	if err != nil {
		return bindResult{}, err
	}
	return bindResult{Args: b.baseArgs, Env: env}, nil
}
```

Test both the populated and the empty-entries paths, and that a `writeConfig` error propagates rather than launching a provider with a half-written config.

Commit: `clown: add configFileBinding for opencode/crush (#202)`

---

## Task 8: `writeOpencodeConfigFile` emits `mcp`

**Promotion criteria:** N/A.

**Files:**
- Modify: `cmd/clown/opencode.go` (~line 165)
- Test: `cmd/clown/opencode_test.go`

**The schema (verified against `packages/core/src/v1/config/mcp.ts:44-59`):** `McpRemoteConfig` is `{type: "remote", url, enabled?, headers?, oauth?, timeout?}`. `type` is the literal string `"remote"` — **not** `"http"`. `timeout` is in **milliseconds**, defaulting to 5000.

**Emit `timeout` explicitly.** opencode's 5s default is shorter than clown's 30s plugin default, so omitting it makes long-running MCP tools fail at five seconds. `pluginhost.MCPServerEntry.Timeout` is already milliseconds, so this is a straight copy — but only emit it when non-zero, so a plugin that sets no timeout gets opencode's default rather than `timeout: 0`.

**Step 1: Write the failing golden test**

```go
func TestWriteOpencodeConfigFile_EmitsMCPRemoteEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	mcp := map[string]pluginhost.MCPServerEntry{
		"moxy__moxy": {Type: "http", URL: "http://127.0.0.1:5001/mcp", Timeout: 30000},
	}
	if err := writeOpencodeConfigFile(path, "https://gw/v1", "tok", "gpt-4o", mcp); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &cfg)

	servers := cfg["mcp"].(map[string]any)
	entry := servers["moxy__moxy"].(map[string]any)
	if entry["type"] != "remote" {
		t.Errorf(`type = %v, want "remote" (opencode's literal, not clown's "http")`, entry["type"])
	}
	if entry["url"] != "http://127.0.0.1:5001/mcp" {
		t.Errorf("url = %v", entry["url"])
	}
	if entry["timeout"] != float64(30000) {
		t.Errorf("timeout = %v, want 30000 (ms); opencode's 5000 default is too short", entry["timeout"])
	}
	if entry["enabled"] != true {
		t.Errorf("enabled = %v", entry["enabled"])
	}
}

func TestWriteOpencodeConfigFile_OmitsMCPWhenEmpty(t *testing.T) {
	// nil entries must not emit an empty "mcp": {} block
}

func TestWriteOpencodeConfigFile_OmitsZeroTimeout(t *testing.T) {
	// Timeout: 0 must leave the key absent so opencode's default applies
}
```

**Step 2-4:** Run (fail), implement (add the `mcp` field to the local `opencodeConfig` struct with `json:"mcp,omitempty"`), run (pass).

**Step 5:** Update the existing four `writeOpencodeConfigFile` callers/tests for the new parameter.

Commit: `opencode: emit clown's MCP servers into the generated config (#202)`

---

## Task 9: `writeCrushConfig` emits `mcp` — with the seconds conversion

**Promotion criteria:** N/A.

**Files:**
- Modify: `cmd/clown/crush.go` (~line 198)
- Test: `cmd/clown/crush_test.go`

**The trap.** crush's `MCPConfig.Timeout` is in **seconds** (`internal/config/config.go:198`, default 15). clown's is milliseconds. A naive copy turns clown's `30000` into an **8-hour** timeout. Convert, round up, and floor at 1.

**Step 1: Write the failing test — lead with the conversion**

```go
func TestCrushMCPTimeoutSeconds(t *testing.T) {
	for _, tc := range []struct {
		ms   int
		want int
	}{
		{0, 0},      // unset stays unset; crush applies its own default
		{30000, 30}, // the common case
		{1500, 2},   // round up, never truncate toward zero
		{1, 1},      // sub-second floors at 1, never 0
	} {
		if got := crushMCPTimeoutSeconds(tc.ms); got != tc.want {
			t.Errorf("crushMCPTimeoutSeconds(%d) = %d, want %d", tc.ms, got, tc.want)
		}
	}
}

func TestWriteCrushConfig_EmitsMCPEntries(t *testing.T) {
	dir := t.TempDir()
	mcp := map[string]pluginhost.MCPServerEntry{
		"moxy__moxy": {Type: "http", URL: "http://127.0.0.1:5001/mcp", Timeout: 30000},
	}
	if err := writeCrushConfig(dir, crushBackendOpenAICompat, "u", "k", "m", mcp); err != nil {
		t.Fatal(err)
	}
	cfg := readCrushConfigJSON(t, dir)
	entry := cfg["mcp"].(map[string]any)["moxy__moxy"].(map[string]any)
	if entry["type"] != "http" {
		t.Errorf("type = %v", entry["type"])
	}
	if entry["timeout"] != float64(30) {
		t.Errorf("timeout = %v, want 30 SECONDS (clown stores ms)", entry["timeout"])
	}
}
```

**Step 2-4:** Run (fail), implement, run (pass). Add `MCP map[string]crushMCPEntry \`json:"mcp,omitempty"\`` to the local `crushConfig` struct.

**Step 5:** Update the existing `writeCrushConfig` callers/tests for the new parameter.

Commit: `crush: emit clown's MCP servers, converting timeouts to seconds (#202)`

---

## Task 10: route `runOpencode` through the plugin host

**Promotion criteria:** N/A.

**Files:** `cmd/clown/opencode.go`, `cmd/clown/main.go` (dispatch ~line 648, 654)

`runOpencode(cliPath, flags.forwarded, selectedProfile)` becomes a call that also receives `flags` and `pluginDirs`. Keep the backend resolution exactly as-is; the change is that instead of `exec.Command` at the end, it builds a `configFileBinding` whose `writeConfig` closes over the resolved url/token/model and the temp dir, and calls `runWithPluginHost`.

Watch for: `tmpDir` is currently `defer os.RemoveAll`-ed inside `runOpencode`. That defer must outlive the provider run — it will, because `runWithPluginHost` runs the provider as a subprocess and returns, but confirm it rather than assuming.

Also: `openrouter` dispatches to `runOpencode` too (main.go:654). Both call sites change.

Run `just build`, then a real smoke: `just verify-opencode-against-openrouter` (config synthesis only, no network call).

Commit: `opencode: run under the clown plugin host (#202)`

---

## Task 11: route `runCrush` through the plugin host

Same shape as Task 10 for `cmd/clown/crush.go` and main.go:656. Smoke with `just verify-crush-tailnet` if a tailnet model is reachable, else `./result/bin/clown --provider crush -- --version`.

Commit: `crush: run under the clown plugin host (#202)`

---

## Task 12: confirm the job-monitor dir is still claude-only

**Files:** `cmd/clown/jobmonitor_test.go`

`providerUsesPluginDirs` (`jobmonitor.go:104`) returns true only for claude/clownbox and **must keep doing so**. opencode/crush now consume plugin dirs in a different sense (their MCP servers), but they still must not receive the synthesized *monitor* dir — monitors are a Claude Code mechanism and the dir would leak.

The existing test at `jobmonitor_test.go:203-216` already covers this. Add an explicit comment there recording *why* opencode/crush stay false even though they now run under the plugin host, so a future reader does not "fix" it.

Commit: `jobmonitor: document why opencode/crush stay off the monitor dir (#202)`

---

## Task 13: docs

- Update `docs/features/0016-non-claude-provider-parity.md` status when phase 1 lands (`proposed` → `testing`).
- Check whether `clown-json(5)` / `clownfile(5)` manpages need the new `[providers]` table documented — see `eng-manpages(7)` for authoring conventions. `just lint-man` gates this.
- Run the `eng:doc-drift` skill before merging.

---

## Out of scope for this plan (phase 1b)

The **dumbo** mock OpenAI/Anthropic-compatible API fixture and the bats integration lane that uses it. The design records it as agreed, but its home (in-tree `cmd/dumbo` vs a standalone repo per FDR 0014) is an open question, and it needs a companion stub MCP server — dumbo fakes the model, not the tool server. Write that plan separately once the question is settled; use `eng:wiring-bats-tests`.

---

## Final verification before merge

```bash
just build      # authoritative; ensure every new file is git-added first
just test-go
just lint
```

Then `mcp__spinclass__merge-this-session`.
