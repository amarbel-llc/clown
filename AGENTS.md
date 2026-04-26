# AGENTS.md

This file provides guidance to coding agents working in this repository,
including Codex and Claude Code.

## Overview

Clown is a Nix-packaged wrapper around coding agents (Claude Code, Codex,
local models via `circus`, and OpenAI-compatible providers via `opencode`) that
injects custom system prompts, applies per-provider safety defaults, and
provides fish shell completions and session management. A single `clown` binary
dispatches to the selected provider via `--provider <claude|codex|circus|opencode>`
(default: `claude`; override with `CLOWN_PROVIDER` env var). Built entirely with
Nix flakes; no standalone test suite (pre-merge validation via `just build`).

## Build Commands

```sh
just build       # Default: nix build --show-trace
just build-go    # Build Go binaries (clown, clown-plugin-host)
just test-go     # Run Go unit tests
just clean       # rm -rf result
```

Format nix: `lux fmt flake.nix`

## Architecture

The flake produces a `symlinkJoin` of five components:

1. **`clown` wrapper** (2-line shell script in `flake.nix`): Sets the
   `CLOWN_PLUGIN_META` env var (pointing at the build-time plugin metadata
   directory) and execs the Go binary.

2. **`clown-go`** (`cmd/clown/main.go`, Go binary): The main entrypoint.
   Parses `--provider` and dispatches to Claude or Codex. Walks from `$PWD`
   up to `$HOME` collecting `.circus/` directories for system prompt injection
   (via `internal/circus`). Two prompt modes:
   - **Replace**: Deepest `.circus/system-prompt` file wins
   - **Append**: All `.md` files from `.circus/system-prompt.d/` directories,
     shallowest-first, plus builtin fragments from `system-prompt-append.d/`
   - Per-provider safety defaults (via `internal/provider`):
     - Claude: `--disallowed-tools 'Bash(*)'`, `--disallowed-tools 'Agent(Explore)'`
     - Codex: `--sandbox workspace-write`
   - Directly manages HTTP MCP server lifecycle for Claude plugins (via
     `internal/pluginhost`): discovers `clown.json` manifests, launches
     servers, reads handshakes, polls health endpoints, compiles replacement
     plugin manifests with URL-based MCP entries, and passes compiled
     plugin directories to Claude via `--plugin-dir`.
   - Build-time configuration injected via `-ldflags -X` into
     `internal/buildcfg` (provider CLI paths, version strings, agents file,
     system-prompt-append.d path).
   - The `claude-code` derivation is patched to redirect its managed-settings
     path from `/etc/claude-code` to `$out/etc/claude`, and a managed
     `managed-settings.json` is shipped with `permissions.disableAutoMode:
     "disable"`. Auto-mode is therefore permanently unavailable through
     clown regardless of user settings, project settings, or CLI flags.

   **Plugin manifest compilation.** When a plugin has both `clown.json`
   (HTTP MCP servers) and `.claude-plugin/plugin.json` (claude-native
   manifest), `clown` **compiles** each affected plugin dir into a temporary
   staging directory: top-level entries are symlinked back to the source, but
   `.claude-plugin/plugin.json` is rewritten with the `mcpServers` key
   replaced by URL-based entries pointing at the running HTTP servers. This
   preserves plugin identity (`plugin:<name>:<server>`) and original server
   names in Claude Code, which in turn preserves hook matching. Claude is
   handed the staged dir via `--plugin-dir`, so it still loads
   hooks/skills/commands/agents and registers the MCP servers as
   plugin-sourced. Staged dirs are cleaned up on shutdown. The
   `--disable-clown-protocol` flag (and `CLOWN_DISABLE_CLOWN_PROTOCOL=1` env
   var) bypasses the entire pipeline — plugin dirs are passed to claude
   unmodified, and claude's native MCP path is the only one active.

3. **`clown-plugin-host`** (`cmd/clown-plugin-host/main.go`, Go binary):
   Standalone lifecycle manager retained for backward compatibility.
   Functionally identical to `clown`'s built-in plugin management but
   invoked as a separate process with `--` separating its flags from the
   downstream command. `clown` no longer execs into `clown-plugin-host`.
   See `clown-plugin-host(1)` and `clown-json(5)`.

4. **`clown sessions-complete`** (built into the `clown` binary): emits one
   line per resumable session in fish completion format
   (`<clown://provider/id>\t<reldate>  <title-or-id>`). Pass `--pwd-only` to
   filter to sessions whose recorded cwd exactly matches `$PWD`. Used by
   the fish completion script to populate `clown resume` URI suggestions.
   Codex enumeration was dropped when `bin/clown-sessions` (Python) was
   removed; restoring it requires a Go SQLite reader (see issue #27).

5. **`clown-completions`** (`completions/clown.fish`): Provider-aware fish
   completions. Detects `--provider` on the command line (or `CLOWN_PROVIDER`
   env var) and offers Claude or Codex flags/subcommands accordingly.

   **Circus provider.** `--provider circus` runs Claude Code against a local
   `llama-server` daemon instead of Anthropic's API. `cmd/circus` manages the
   daemon lifecycle (pidfile/portfile at `~/.local/state/circus/`, log at
   `~/.local/state/circus/llama-server.log`). On start, if stdout is a pipe
   (i.e., clown launched it), circus emits a clown-protocol handshake
   (`1|1|tcp|<addr>|streamable-http`) and blocks until stdin closes. Clown
   reads the handshake, sets `ANTHROPIC_BASE_URL` to the local server address,
   and sets `ANTHROPIC_CUSTOM_MODEL_OPTION` to bypass Claude Code's model
   validation. `--model <name-or-path>` selects the GGUF model: absolute paths
   pass through; bare names are resolved from `~/.local/share/circus/models/<name>.gguf`.
   The default model and llama-server binary path are burned in at build time
   via `internal/buildcfg` ldflags. A separate `nixpkgs-llama` flake input
   (pinned to nixpkgs master) provides a llama-cpp build with Anthropic Messages
   API support (`/v1/messages`), which predates the nixos-25.11 stable pin.

   **Model management.** `circus models` lists installed models (from
   `~/.local/share/circus/models/`). `circus download <name>` fetches a model
   from the baked-in registry (`cmd/circus/registry.json`, embedded via
   `go:embed`), validates SHA256, and installs it atomically via temp-file +
   rename. A charmbracelet/bubbles progress bar renders during download. The
   registry ships with Qwen3 and Gemma3 variants; SHA256 digests in the current
   registry are 64-zero placeholders pending real values from HuggingFace.

   **Opencode provider.** `--provider opencode` runs the `opencode` TUI against
   any OpenAI-compatible backend. Configuration is read from
   `~/.config/circus/opencode.toml` (fields: `url`, `token`), which is user-local
   and never committed to the repo. Clown writes a temporary `opencode.json`
   config (in a `mkdtemp` dir) and passes it to opencode via `XDG_CONFIG_HOME`,
   using the `@ai-sdk/openai-compatible` custom provider. The default model is
   `gpt-4o`; model limits are hardcoded in `cmd/clown/opencode.go`. The opencode
   binary path is burned in at build time via `internal/buildcfg.OpencodeCliPath`.

   **Profile system (planned).** A future `--profile <name>` flag (and
   `CLOWN_PROFILE` env var) will select named (provider, backend, model) tuples
   from `profiles/builtin.toml` (open, burned in) and
   `~/.config/circus/profiles.toml` (user-local, may contain URLs/tokens). The
   design doc is at `docs/plans/2026-04-23-profiles-design.md` and the
   implementation plan at `docs/plans/2026-04-23-profiles.md`.

## Nix Conventions

Follows the monorepo's stable-first pattern:
- `nixpkgs` -> stable (`nixos-25.11`)
- `nixpkgs-master` -> pinned SHA
- `nixpkgs-llama` -> pinned to nixpkgs master SHA for llama-cpp with
  `/v1/messages` support (PR #17570, merged 2025-11-28; not in nixos-25.11)
- Claude Code is fetched via inline `fetchTarball` (not a flake input) with
  `allowUnfree` — pinned to a specific nixpkgs SHA for version stability.

## Spinclass Integration

Worktree-based development managed by Spinclass. The `sweatfile` configures a
pre-merge hook that runs `just` (i.e., the full build) before merging a worktree
branch into master.
