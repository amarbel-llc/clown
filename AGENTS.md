# AGENTS.md

This file provides guidance to coding agents working in this repository,
including Codex and Claude Code.

## Overview

Clown is a Nix-packaged wrapper around Claude Code that injects custom system
prompts, disables Bash by default, and provides fish shell completions and
session management. Built entirely with Nix flakes; no standalone test suite
(pre-merge validation via `just build`).

## Build Commands

```sh
just build       # Default: nix build --show-trace
just clean       # rm -rf result
```

Format nix: `lux fmt flake.nix`

## Architecture

The flake produces a `symlinkJoin` of three components:

1. **`clown-bin`** (shell wrapper, defined inline in `flake.nix`): Wraps
   the pinned Claude Code binary. Walks from `$PWD` up to `$HOME` collecting
   `.circus/` directories for system prompt injection. Two modes:
   - **Replace**: Deepest `.circus/system-prompt` file wins (`--system-prompt-file`)
   - **Append**: All `.md` files from `.circus/system-prompt.d/` directories,
     shallowest-first, plus builtin fragments from `system-prompt-append.d/`
     (`--append-system-prompt-file`)
   - Always passes `--disallowed-tools 'Bash(*)'`

2. **`clown-sessions`** (`bin/clown-sessions`, Python3): Scans
   `~/.claude/projects/` to list resumable sessions for shell completion.

3. **`clown-completions`** (`completions/clown.fish`): Fish completions covering
   Claude Code flags, with dynamic session ID completion via `clown-sessions`.

## Nix Conventions

Follows the monorepo's stable-first pattern:
- `nixpkgs` -> stable (`nixos-25.11`)
- `nixpkgs-master` -> pinned SHA
- Claude Code is fetched via inline `fetchTarball` (not a flake input) with
  `allowUnfree` — pinned to a specific nixpkgs SHA for version stability.

## Codex Note

This repository currently targets Claude Code directly. Supporting Codex would
require changes beyond agent docs because the wrapper, completion script, and
session discovery all assume the Claude CLI and its flag surface.

## Spinclass Integration

Worktree-based development managed by Spinclass. The `sweatfile` configures a
pre-merge hook that runs `just` (i.e., the full build) before merging a worktree
branch into master.
