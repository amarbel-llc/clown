# AGENTS.md

This file provides guidance to coding agents working in this repository,
including Codex and Claude Code.

## Overview

Clown is a Nix-packaged wrapper around coding agents (Claude Code and Codex)
that injects custom system prompts, applies per-provider safety defaults, and
provides fish shell completions and session management. A single `clown` binary
dispatches to the selected provider via `--provider <claude|codex>` (default:
`claude`; override with `CLOWN_PROVIDER` env var). Built entirely with Nix
flakes; no standalone test suite (pre-merge validation via `just build`).

## Build Commands

```sh
just build       # Default: nix build --show-trace
just build-go    # Build Go binaries (clown-plugin-host)
just test-go     # Run Go unit tests
just clean       # rm -rf result
```

Format nix: `lux fmt flake.nix`

## Architecture

The flake produces a `symlinkJoin` of four components:

1. **`clown-bin`** (shell wrapper, defined inline in `flake.nix`): Unified
   entrypoint that parses `--provider` and dispatches to Claude or Codex.
   Walks from `$PWD` up to `$HOME` collecting `.circus/` directories for
   system prompt injection. Two modes:
   - **Replace**: Deepest `.circus/system-prompt` file wins
   - **Append**: All `.md` files from `.circus/system-prompt.d/` directories,
     shallowest-first, plus builtin fragments from `system-prompt-append.d/`
   - Per-provider safety defaults:
     - Claude: `--disallowed-tools 'Bash(*)'`, `--disallowed-tools 'Agent(Explore)'`
     - Codex: `--sandbox workspace-write`
   - The `claude-code` derivation is patched to redirect its managed-settings
     path from `/etc/claude-code` to `$out/etc/claude`, and a managed
     `managed-settings.json` is shipped with `permissions.disableAutoMode:
     "disable"`. Auto-mode is therefore permanently unavailable through
     clown regardless of user settings, project settings, or CLI flags.

2. **`clown-sessions`** (`bin/clown-sessions`, Python3): Lists resumable
   sessions for shell completion. Accepts `--provider codex` to query Codex's
   SQLite state DB; defaults to scanning Claude's JSONL transcripts.

3. **`clown-plugin-host`** (`cmd/clown-plugin-host/main.go`, Go binary):
   Lifecycle manager for HTTP MCP servers declared in `clown.json` manifests.
   Sits between the shell wrapper and Claude Code. Scans `--plugin-dir`
   directories for `clown.json`, launches declared HTTP servers as child
   processes, reads their handshake lines (port negotiation via go-plugin
   protocol), polls `/healthz`, generates a temporary `.mcp.json` with
   server URLs, and passes it to Claude via `--mcp-config`. When no
   `clown.json` is found, exec's directly into claude (zero overhead).
   See `clown-plugin-host(1)` and `clown-json(5)`.

   **Plugin manifest compilation.** When a plugin has both `clown.json`
   (HTTP MCP servers) and `.claude-plugin/plugin.json` (claude-native
   manifest), its MCP servers would otherwise register twice in claude —
   once under `<plugin>/<server>` via the generated `.mcp.json` and once
   under `plugin:<plugin>:<server>` via claude's native plugin loader.
   To avoid this, `clown-plugin-host` **compiles** each affected plugin
   dir into a temporary staging directory: top-level entries are symlinked
   back to the source, but `.claude-plugin/plugin.json` is rewritten with
   the `mcpServers` key removed. Claude is handed the staged dir via
   `--plugin-dir`, so it still loads hooks/skills/commands/agents but no
   longer registers the duplicated MCP servers. Staged dirs are cleaned
   up on shutdown. The `--disable-clown-protocol` flag (and
   `CLOWN_DISABLE_CLOWN_PROTOCOL=1` env var) bypasses the entire
   clown-plugin-host pipeline — plugin dirs are passed to claude
   unmodified, and claude's native MCP path is the only one active.

4. **`clown-completions`** (`completions/clown.fish`): Provider-aware fish
   completions. Detects `--provider` on the command line (or `CLOWN_PROVIDER`
   env var) and offers Claude or Codex flags/subcommands accordingly.

## Nix Conventions

Follows the monorepo's stable-first pattern:
- `nixpkgs` -> stable (`nixos-25.11`)
- `nixpkgs-master` -> pinned SHA
- Claude Code is fetched via inline `fetchTarball` (not a flake input) with
  `allowUnfree` — pinned to a specific nixpkgs SHA for version stability.

## Spinclass Integration

Worktree-based development managed by Spinclass. The `sweatfile` configures a
pre-merge hook that runs `just` (i.e., the full build) before merging a worktree
branch into master.
