# Clown Profile System Design

**Goal:** Allow users to select a (provider, backend, model) combination via a
named profile, with a charmbracelet picker when no profile is specified and
validation that rejects unsupported combinations.

**Architecture:** Two-layer TOML profile files (burned-in builtin + optional
user-local additional). Clown merges them at startup, validates the selected
combo, and dispatches. A charmbracelet bubble-tea picker is shown when running
interactively with no profile selected.

**Rollback:** Purely additive. `--provider` / `CLOWN_PROVIDER` keep working
unchanged. Removing `--profile` / `CLOWN_PROFILE` restores prior behavior with
no migration.

---

## Profile Schema

Both files use the same TOML schema:

```toml
[[profile]]
name     = "local-qwen"           # machine name — used as --profile value and CLOWN_PROFILE
display  = "Local (Qwen3-Coder)"  # label shown in charmbracelet picker
provider = "claude"               # claude | opencode
backend  = "local"                # local | gateway | anthropic
model    = "qwen3-coder"          # provider-specific model identifier

# Optional — only present in additional (user-local) profiles, never committed
url   = "https://..."
token = "..."
```

### Valid combinations

| provider  | backend     | url/token required |
|-----------|-------------|-------------------|
| `claude`  | `anthropic` | no                |
| `claude`  | `local`     | no                |
| `opencode`| `anthropic` | no                |
| `opencode`| `gateway`   | yes               |
| `opencode`| `local`     | no                |

Any other `(provider, backend)` pair is rejected at startup with a clear error
listing valid combinations.

### Provider-specific model injection

| provider   | backend     | mechanism                                           |
|------------|-------------|-----------------------------------------------------|
| `claude`   | `anthropic` | `--model` flag forwarded to claude-code             |
| `claude`   | `local`     | `ANTHROPIC_BASE_URL` + `ANTHROPIC_CUSTOM_MODEL_OPTION` env vars (via circus handshake) |
| `opencode` | `anthropic` | `model` field in injected `opencode.json`           |
| `opencode` | `gateway`   | `model` field in injected `opencode.json`, `url`/`token` from profile |
| `opencode` | `local`     | `model` field in injected `opencode.json`, `baseURL` from circus portfile |

---

## File Locations

### Burned-in profiles (`profiles/builtin.toml`)
- Committed to the repo under `profiles/builtin.toml`
- Embedded via `go:embed` in `cmd/clown`
- Contains only open combinations — no URLs, no tokens
- Ships with at minimum:
  - `(claude, anthropic, claude-sonnet-4-6)`
  - `(claude, local, qwen3-coder)` (requires circus)
  - `(opencode, anthropic, claude-sonnet-4-6)`
  - `(opencode, local, qwen3-coder)` (requires circus)

### Additional profiles (`~/.config/circus/profiles.toml`)
- Optional, user-local, never referenced in source
- May contain gateway profiles with URLs and tokens
- Loaded at startup if present; missing file is not an error
- Duplicate `name` in additional file overrides the builtin entry

---

## CLI Surface

### Flag
```
clown --profile <name>
```
Selects a profile by its `name` field. Bypasses the picker.

### Environment variable
```
CLOWN_PROFILE=<name>
```
Same as `--profile`. Lower precedence than the flag.

### Interactive picker
When clown is invoked with no `--profile` / `CLOWN_PROFILE` and stdout is a
TTY, it presents a charmbracelet bubbletea list of all valid profiles (display
names), sorted builtin-first then additional. Selecting one dispatches
immediately.

### Escape hatches
`--provider` and `CLOWN_PROVIDER` continue to work. When used without
`--profile`, clown infers a best-effort profile (e.g. `--provider claude`
defaults to `(claude, anthropic)`). Invalid `(provider, backend)` combos from
direct flags print a clear error.

---

## Error Handling

- Unknown `--profile` name: print available profiles and exit 1
- Invalid `(provider, backend)` combo: print valid combos table and exit 1
- `gateway` backend with missing `url` or `token`: print config file path and exit 1
- `local` backend with circus not running / not configured: existing circus error path

---

## Tracer Bullet Scope

For the initial prototype:

1. Parse `profiles/builtin.toml` (embedded) and `~/.config/circus/profiles.toml` (optional)
2. `--profile` flag and `CLOWN_PROFILE` env var selection
3. Validation matrix (fail fast on invalid combos)
4. Wire `(opencode, local)` — new combo not yet implemented
5. Charmbracelet picker when no profile selected and TTY

Out of scope for tracer bullet:
- `--profile` tab completions
- Per-profile managed settings overrides
