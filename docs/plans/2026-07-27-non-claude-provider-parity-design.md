# Non-Claude Provider Parity — Phase 0 + Phase 1 Design

Date: 2026-07-27
Status: approved (brainstorm with Sasha, session ready-dogwood; every
"verified" claim below was checked against upstream source or a live
experiment — see Evidence)

Issue: https://code.linenisgreat.com/clown/issues/202
Feature record: `docs/features/0016-non-claude-provider-parity.md`

## Problem

FDR 0016 identified three capability gaps between the claude family and
opencode/crush: the clown plugin protocol (MCP tool injection), plugin-
contributed system-prompt fragments, and ringmaster/troupe job-wakeup. This
design covers the first of those, plus a precondition the FDR did not
anticipate.

Agreed phasing:

- **Phase 0** — make clown's generated provider config authoritative.
- **Phase 1** — refactor the plugin-host pipeline behind a provider-pluggable
  seam, with opencode/crush MCP injection as its first consumer.
- **Phase 2** — static + dynamic system-prompt fragments.
- **Phase 3** — ringmaster async jobs / troupe wakeup.

## Why Phase 0 exists

FDR 0016 assumed clown's generated configs were hermetic. `cmd/clown/crush.go`'s
header comment asserts as much. **Both are wrong**, and the consequence is a
security problem rather than a tidiness one.

Verified by experiment (see Evidence): a repo-local `opencode.json` **replaces**
a same-named entry in clown's `mcp` map. A probe entry named `clownprobe`,
written by clown pointing at `127.0.0.1:19001`, resolved instead to the project
config's `127.0.0.1:19002` — and `opencode mcp list` reported "2 server(s)", an
override rather than a duplicate.

So **any repository you open can silently repoint a clown-managed MCP server
name at a URL of its choosing.** The agent calls what it believes are clown's
tools; the traffic goes where the project config says. Today this is latent,
because clown injects no `mcp` entries into these providers at all. Phase 1 is
precisely the change that arms it. Hence Phase 0 lands first.

A clown-side merge layer alone does **not** fix this: clown cannot win by
writing a better file, because both providers re-merge the external config
*after* clown's regardless of its contents. Suppression is required; the merge
layer is what keeps suppression from being a functional regression.

## Phase 0 design

**opencode** — set `OPENCODE_DISABLE_PROJECT_CONFIG=1` alongside the existing
`OPENCODE_CONFIG`. This stops `packages/opencode/src/config/config.ts:406-409`
from merging project files after clown's. The user's *global* config still
merges, and merges *before* clown's, so clown wins — deliberate: the per-repo
file is the attack surface, not the user's own `~/.config`.

**crush** — no suppression mechanism exists (`lookupConfigs` unconditionally
appends a bounded upward walk for `crush.json`/`.crush.json`, and no env var
disables it). Use precedence instead: write clown's config into the **data
directory**, which `load.go:65-66` loads last "so it has highest priority",
and pass `--data-dir`. Verified to override a project config in both
directions.

**Stable data dir.** `--data-dir` is also where crush keeps sessions and state,
and it is per-project. Pointing it at a `mkdtemp` — as clown's current config
handling does — would break `crush --continue` and session history on every
launch. Phase 0 therefore requires a *stable*, clown-owned, per-project data
directory. This is genuinely new state for clown to own and is the piece most
likely to need revisiting.

**End state (not phase 0).** The intended destination is a clown-side *safe
merge*: clown reads the project config itself and folds it in, honoring project
entries except where they would clobber a clown-owned key. Phase 0's blanket
suppression is a step toward that, not a permanent posture.

## Phase 1 design

### The seam

The only thing that genuinely varies between providers is **how started MCP
servers reach the provider**: claude stages plugin dirs and gets `--plugin-dir`
flags; opencode/crush get a JSON config plus an env var. Everything upstream —
discover, `StartAll`, failure policy, `--cheap-context` — is already provider-
agnostic.

```go
// pluginBinding delivers the started MCP servers to a provider in that
// provider's native form. It owns everything downstream of "servers are healthy".
type pluginBinding interface {
    Bind(host *pluginhost.Host, discovered []pluginhost.DiscoveredServer) (bindResult, error)
}

type bindResult struct {
    Args []string // final argv
    Env  []string // extra env for the child
}
```

- `claudeBinding` — a pure extraction of today's `CompileForClaude` +
  `prependPluginDirs`.
- `configFileBinding` — shared by opencode and crush, parameterized only by the
  function that writes that provider's JSON and names its env var.
- `runManaged`'s three fallback paths (nothing discovered / nothing healthy /
  cheap-context deselected everything) call `Bind` with a nil server set instead
  of `prependPluginDirs(..., nil)`.
- `runProvider` gains an `extraEnv []string` parameter; today it inherits
  `os.Environ()` with no additions.

**Rejected: extending `Executor` with a third method.** `tentExecutor` wraps
*claude* in podman and `passthroughExecutor` wraps claude in clownbox, so "which
binary/argv shape" and "how plugins are delivered" are independent axes.
Collapsing them forces a cross-product of executor types.

**Accepted consequence:** routing opencode/crush through `runProvider` gives
them behavior they lack today — ctrl-z pty-suspend, SIGTERM/SIGINT forwarding,
and clown staying in the process tree for post-exit hooks. This is an
improvement, but it is a change beyond "add MCP" and should be described as such.

### Where translation lives

`internal/pluginhost` gains one neutral accessor:

```go
// ServerEntries returns a flat, globally-unique name→entry map for every
// running server, for providers whose config has a single `mcp` object.
func (h *Host) ServerEntries(discovered []DiscoveredServer) map[string]MCPServerEntry
```

This **departs from FDR 0016's suggested `CompileForOpencode`/`CompileForCrush`**.
Those would put two more providers' JSON schemas inside `pluginhost`, when the
writers that already own those schemas (`writeOpencodeConfigFile`,
`writeCrushConfig`) live in `cmd/clown`. `pluginhost` hands out neutral
`MCPServerEntry` values; each provider's writer translates. Crush's ms→s
conversion is a property of crush's schema, so it belongs next to crush's writer.

### Schema translation is not re-serialization

| | type vocabulary | timeout unit | default |
|---|---|---|---|
| clown `MCPServerEntry` | `http` / `sse` | ms | none emitted |
| opencode `McpRemoteConfig` | `remote` (literal) | ms | 5000 |
| crush `MCPConfig` | `stdio` / `sse` / `http` | **seconds** | 15 |

Two consequences:

- crush needs ms→s conversion. A naive copy of clown's `30000` becomes an
  8-hour timeout.
- opencode's 5s default is *shorter* than clown's 30s plugin default, so clown
  must emit an explicit `timeout` or long-running MCP tools fail at 5 seconds.

### MCP key naming

Claude namespaces MCP servers per plugin dir; opencode and crush have one flat
`mcp` map, so two plugins each declaring a server named `mcp` collide. The two
providers also derive tool names from the config key by **different rules**:

- opencode (`packages/opencode/src/mcp/catalog.ts:117-119`) sanitizes:
  `value.replace(/[^a-zA-Z0-9_-]/g, "_")`, then `sanitize(client) + "_" + sanitize(tool)`.
- crush (`internal/agent/tools/mcp-tools.go:58-60`) does not sanitize at all:
  `fmt.Sprintf("mcp_%s_%s", mcpName, tool.Name)`.

A `/` key would be silently mangled by opencode and would produce an invalid
tool name under crush (most model APIs enforce `^[a-zA-Z0-9_-]{1,64}$`).
opencode's sanitizer adds a second hazard: every illegal character collapses to
`_`, so two distinct keys can sanitize to the same prefix and shadow each other.

**Decision:** keys are `<plugin>__<server>`, each component sanitized clown-side
to `[A-Za-z0-9_-]`. Staying inside that charset makes opencode's `sanitize` a
no-op, so clown's key is exactly what both providers use — no silent mangling,
no divergence between the two providers' tool names. Clown detects a
post-sanitization collision at compile time and fails loudly rather than letting
one server shadow another. `DiscoveredServer.Name()`'s `<plugin>/<server>` form
stays a log/display identifier, not a config key.

Recorded constraints: crush's `mcp_` prefix consumes 4 characters of the ~64-char
tool-name budget (`clown-builtin-jobs__ringmaster` leaves ~30 for the tool name);
opencode exposes `experimental.mcp_timeout` as a global default if per-entry
timeouts prove awkward.

### Data flow

```
runOpencode
  └─ resolve backend (url/token/model)          ← unchanged
  └─ build configFileBinding{write: writeOpencodeConfigFile(url,token,model, ·)}
  └─ runWithPluginHost(opencodeExecutor, args, pluginDirs, flags, nil, "", binding)
       ├─ Discover → StartAll → failure policy → cheap-context   ← shared with claude
       ├─ binding.Bind(host, discovered)
       │    └─ writeOpencodeConfigFile(..., host.ServerEntries(discovered))
       │       → env: OPENCODE_CONFIG=<tmp>, OPENCODE_DISABLE_PROJECT_CONFIG=1
       └─ runProvider(executor, args, env) → provider runs → host.Shutdown (deferred)
```

### Scope decisions

- **The full plugin set, including `clown-builtin-jobs`.** opencode/crush get
  `job_start`/`job_wait`/`chat_send`/`chat_read` as working tools. They do **not**
  get the wake (monitors are a Claude-Code mechanism) or the PreToolUse
  auto-allow hook, so the agent must poll (`job_wait` blocks) rather than being
  woken, and clown tool calls hit the provider's own permission prompt. Accepted
  as degraded-not-broken until phase 3.
- **Error handling follows claude's existing policy** rather than inventing one:
  discovery/compile failure aborts; start failures hit the same
  `--skip-failed` / interactive-confirm / abort ladder; no healthy servers writes
  the config with no `mcp` block and still launches, mirroring claude's fallback.
  The one new failure mode is config-write failure, which aborts.

## Testing

1. **Golden-config Go table tests first.** The seam is pure — `map[string]MCPServerEntry`
   in, config bytes out — so `writeOpencodeConfigFile` and `writeCrushConfig` get
   exact-JSON assertions, covering the ms→s conversion, the explicit opencode
   timeout, key sanitization, and the collision-detection error.
2. **Then a bats lane** (`eng:wiring-bats-tests`), backed by two new fixtures:
   - **dumbo** — a mock OpenAI/Anthropic-compatible API, so opencode/crush can be
     driven end-to-end without a real model provider.
   - a **stub MCP server**, so the lane can assert a tool call actually reaches
     clown's server. dumbo fakes the model; it does not fake the tool server.

Open: whether dumbo lives in-tree (`cmd/dumbo`) or as a standalone repo in the
style of ringmaster/troupe (FDR 0014). Not decided.

`just build` (`nix build --show-trace`) remains the authoritative check; note
that new files must be `git add`ed before it sees them.

## Rollback

- **claude path**: `claudeBinding` is a pure extraction, so claude's existing
  behavior is the control. A regression there is a bug, not a configuration
  choice.
- **new provider surface**: the escape hatch already exists —
  `--disable-clown-protocol` / `CLOWN_DISABLE_CLOWN_PROTOCOL=1` bypasses the
  plugin host entirely, which for opencode/crush means "behave exactly as today".
- **phase 0 suppression** needs its own switch, since `OPENCODE_DISABLE_PROJECT_CONFIG`
  is a real behavior change for anyone relying on a repo-local config. Gate it on
  a clownfile key defaulting to on, so rollback is one config line rather than a
  revert.

## Tuning levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| MCP key scheme | `<plugin>__<server>`, `[A-Za-z0-9_-]` | union of both providers' constraints; makes opencode's sanitize a no-op | tool-name length overruns near the 64-char cap |
| opencode per-entry `timeout` | explicit, from `clown.json` | opencode's 5s default is shorter than clown's 30s | per-entry proves noisy → switch to `experimental.mcp_timeout` |
| crush data-dir location | stable, clown-owned, per-project | `mkdtemp` would break session continuity | session-continuity complaints, or collisions across worktrees |
| Project-config suppression | on | closes the MCP-name override | a user legitimately needs repo-local config → build the safe merge sooner |
| Jobs tools without wake | included | polling is degraded, not broken | confusion from jobs that complete without notifying |

## Evidence

Claims marked *observed* were reproduced live against crush 0.86.0 and
opencode 1.17.7 during this session; claims marked *source* were read from
upstream but not separately executed.

| Claim | Basis | Result |
|---|---|---|
| crush merges project `crush.json` over clown's global config | observed | yes — `custom/modelPROJECT` appears only when the project file is present |
| crush loads workspace config at `<data-dir>/crush.json` | observed | yes |
| crush workspace config **wins** a scalar collision | observed, both directions | yes — `providers.custom.disable` from the workspace overrode the project's value whether `true` or `false` |
| crush cannot disable its project-config walk | source | no such env var; `lookupConfigs` always appends it |
| opencode merges project `opencode.json` over clown's | observed | yes |
| opencode project config **replaces** a clown-owned `mcp` key | observed | yes — `clownprobe` → project's `:19002`, "2 server(s)" |
| opencode `OPENCODE_DISABLE_PROJECT_CONFIG` suppresses the project config | observed (post-implementation) | yes — a project `opencode.json` declaring `EVILPROJECTENTRY` is absent with hermeticity on (3 servers) and present with `[providers] hermetic-config = false` (4 servers). The control matters: it shows the file really is read when suppression is off, so the absence is suppression rather than opencode ignoring it |
| clownfile `hermetic-config = false` rollback switch works end to end | observed (post-implementation) | yes — same experiment, the `false` arm |
| crush `mcp` map obeys the same last-wins merge | source | one `jsons.Merge` over the whole document, so `mcp` cannot be exempt; not separately observed |
| opencode/crush MCP tool-name derivation rules | source | differ; see MCP key naming |
| opencode/crush `mcp` schema shapes and timeout units | source | see table above |

## Implementation notes

- `cmd/clown/crush.go`'s header comment claiming hermeticity w.r.t. the user's
  config is verifiably inaccurate and should be corrected in the same change
  that lands phase 0.
- `providerUsesPluginDirs` (`cmd/clown/jobmonitor.go:104`) gates the synthesized
  job-monitor dir on claude/clownbox. It stays as-is: opencode/crush get the
  jobs plugin's *MCP servers* but not its *monitors*, which is exactly the
  phase-1 scope decision above.

## More information

- FDR 0016 — `docs/features/0016-non-claude-provider-parity.md`
- RFC 0002 — `docs/rfcs/0002-clown-plugin-protocol.md` (§5.5 documents the
  claude-family scope boundary this design starts to lift)
- FDR 0003 — `docs/features/0003-plugin-contributed-prompt-fragments.md` (phase 2)
- FDR 0013 — `docs/features/0013-job-wakeup-channel.md` (phase 3)
- opencode config loading: `packages/opencode/src/config/config.ts`;
  MCP schema: `packages/core/src/v1/config/mcp.ts`;
  tool naming: `packages/opencode/src/mcp/catalog.ts`
- crush config loading: `internal/config/load.go`;
  MCP schema: `internal/config/config.go`;
  tool naming: `internal/agent/tools/mcp-tools.go`
