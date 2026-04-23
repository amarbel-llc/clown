# Profile System Manual Test Plan

**Feature:** `--profile` flag + `CLOWN_PROFILE` env var + interactive charmbracelet picker  
**Binary:** rebuild first with `just build-go` (or `nix build` for a full rebuild)

---

## Setup

```sh
just build-go
alias clown="./clown"   # or use result/bin/clown after nix build
```

All tests below use `--naked` or `--version` to avoid actually launching a downstream agent.

---

## 1. Version / baseline (sanity check)

```sh
clown --version
```

Expected: prints version string, exits 0. No profile picker appears.

---

## 2. Picker appears when stdin is a terminal and no profile given

```sh
clown --version   # skip — this exits before picker
clown             # no flags → picker should appear
```

Expected: charmbracelet list TUI appears with 4 entries:
- `Claude (Anthropic)` — `claude / anthropic`
- `Claude (Local)` — `claude / local`
- `OpenCode (Anthropic)` — `opencode / anthropic`
- `OpenCode (Local)` — `opencode / local`

Press `q` or `esc`. Expected: exits 0, no downstream agent launched.

---

## 3. Picker does NOT appear when stdin is a pipe

```sh
echo | clown
```

Expected: exits immediately (no picker), no TUI rendered, exits 0 or 1 depending on whether a provider is configured. The key assertion is: **no TUI is drawn**.

---

## 4. Picker does NOT appear with `--naked`

```sh
clown --naked --provider claude --version
```

Expected: behaves as if picker was skipped; runs downstream directly (or errors on `--version` passthrough). No picker TUI.

---

## 5. `--profile` selects a named profile

```sh
clown --profile claude-anthropic --version
```

Expected: exits 0, version printed, no picker. The profile was resolved and `--provider claude` was set implicitly.

```sh
clown --profile opencode-local --version
```

Expected: same — exits 0, no picker.

---

## 6. `CLOWN_PROFILE` env var works

```sh
CLOWN_PROFILE=claude-anthropic clown --version
```

Expected: same as `--profile claude-anthropic --version` — no picker, exits 0.

---

## 7. Unknown profile name errors cleanly

```sh
clown --profile nonexistent --version
```

Expected: exits 1, stderr contains:
```
clown: unknown profile "nonexistent"
available profiles:
  claude-anthropic     Claude (Anthropic)
  claude-local         Claude (Local)
  opencode-anthropic   OpenCode (Anthropic)
  opencode-local       OpenCode (Local)
```

---

## 8. User-local profiles override builtins by name

Create `~/.config/circus/profiles.toml`:

```toml
[[profile]]
name     = "claude-anthropic"
display  = "My Custom Claude"
provider = "claude"
backend  = "anthropic"
model    = "claude-opus-4-7"
```

```sh
clown --profile claude-anthropic --version
```

Expected: exits 0. The picker (if invoked) would show `My Custom Claude` instead of `Claude (Anthropic)` for that slot.

To verify via picker:

```sh
clown   # → picker appears; first entry should read "My Custom Claude"
```

Press `q` to exit without selecting.

Clean up: remove or rename the file.

---

## 9. User-local profiles append new entries

Create `~/.config/circus/profiles.toml`:

```toml
[[profile]]
name     = "my-gateway"
display  = "My Gateway"
provider = "opencode"
backend  = "gateway"
model    = "gpt-4o"
url      = "https://example.com/v1"
token    = "tok-test"
```

```sh
clown --profile my-gateway --version
```

Expected: exits 0, no error.

```sh
clown   # picker should show 5 entries including "My Gateway"
```

Press `q` to exit. Clean up afterward.

---

## 10. Missing `~/.config/circus/profiles.toml` is not an error

```sh
rm -f ~/.config/circus/profiles.toml
clown --version
```

Expected: exits 0. No error about missing file.

---

## 11. Picker — select a profile with Enter

```sh
clown
```

Navigate to `Claude (Anthropic)` with arrow keys, press Enter.

Expected: clown attempts to run the `claude` provider. It will likely fail or prompt you (since you're not in a real session), but the key assertion is: **no "unknown profile" error** and **provider dispatch happened** (you see claude-code output or a claude-code error, not a clown error).

---

## 12. Picker — filter works

```sh
clown
```

Start typing `opencode`. Expected: list filters to the two opencode entries.

Press `esc` to quit.

---

## 13. `--profile` + `--provider` conflict — profile wins

```sh
clown --profile claude-anthropic --provider opencode --version
```

Expected: `flags.provider` is set to `claude` by the profile resolution code (profile sets provider). Verify by checking that the claude provider path is used (no opencode error about missing config).

---

## 14. Invalid `profiles.toml` syntax surfaces an error

Create `~/.config/circus/profiles.toml` with bad TOML:

```
[[profile]
name = "broken"
```

```sh
clown --version
```

Expected: exits 1, stderr contains `clown: loading profiles:` or `clown: additional profiles:` with a parse error. Does not silently ignore the bad file.

Clean up afterward.
