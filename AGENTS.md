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
just clean       # rm -rf result
```

Format nix: `lux fmt flake.nix`

## Architecture

The flake produces a `symlinkJoin` of three components:

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

2. **`clown-sessions`** (`bin/clown-sessions`, Python3): Lists resumable
   sessions for shell completion. Accepts `--provider codex` to query Codex's
   SQLite state DB; defaults to scanning Claude's JSONL transcripts.

3. **`clown-completions`** (`completions/clown.fish`): Provider-aware fish
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
