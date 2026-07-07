# AGENTS.md

This file provides guidance to coding agents working in this repository,
including Codex and Claude Code.

## Overview

Clown is a Nix-packaged wrapper around coding agents (Claude Code, Codex,
local models via `juggler`, OpenAI-compatible providers via `opencode`, and
charmbracelet's crush) that injects custom system prompts, applies
per-provider safety defaults, and manages MCP plugin lifecycle, session
attach/multiplexing, and cross-session job/chat coordination. A single
`clown` binary dispatches to the selected provider via
`--provider <claude|codex|juggler|opencode|crush>` (default: `claude`;
override with `CLOWN_PROVIDER` env var, or pin one per-directory via a
`clownfile`). Built entirely with Nix flakes; no standalone test suite
(pre-merge validation via `just build`, which runs `nix build --show-trace`).

## Agent gotchas specific to this repo

- **Skills vs `.clown/` are different things.** Skills (`/eng:fdr`,
  `/eng:rfc`, `/init`, etc.) are listed in the session's available-skills
  system reminder and invoked via the `Skill` tool — they do not live under
  `.clown/skills/` (that path doesn't exist). `.clown/` is exclusively for
  clown's own system-prompt injection: `.clown/system-prompt` (replace) and
  `.clown/system-prompt.d/*.md` (append fragments).
- **The job platform (`ringmaster`/`troupe`) is an external dependency, not
  in-tree.** `internal/jobwake`, `internal/jobmcp`, and the
  producer/monitor/chat CLI verbs were extracted to the standalone
  `code.linenisgreat.com/ringmaster` module and are consumed as prebuilt
  binaries (`RingmasterPath`/`TroupePath` in `internal/buildcfg`). Don't look
  for their implementation in this repo; `cmd/clown/jobmonitor.go` is just
  the synthesis point that wires those binaries into the
  `clown-builtin-jobs` plugin.

## Build commands

```sh
just build       # Default: nix build --show-trace
just build-go    # Build Go binaries (clown, clown-plugin-host)
just test-go     # Run Go unit tests
just clean       # rm -rf result
```

Format: `nix fmt` (treefmt; config in `treefmt.nix`).

## Architecture at a glance

The flake's `symlinkJoin` output bundles: the `clown` wrapper script, the
`clown-go` Go binary (`cmd/clown/main.go` — the main entrypoint: provider
dispatch, system-prompt injection, MCP plugin lifecycle via
`internal/pluginhost`, `clownfile`-driven config via `internal/clownfile`,
multiplexer self-attach via `internal/ptysuspend`/`cmd/clown/attach.go`,
and tent (sandboxed container) support via `internal/tent`), the standalone
`clown-plugin-host` binary, `clown sessions-complete` fish-completion
support, and `completions/clown.fish`.

Provider-specific behavior (juggler local models, opencode, crush, the
named profile registry, tent backend selection between podman/lima) is
implemented under `cmd/clown/*.go` and `internal/*`; read the source or the
relevant design doc (below) rather than a summary here, since these areas
change frequently.

## Where designs and decisions live

Detailed rationale, protocols, and in-flight design status belong in
`docs/`, not this file:

- `docs/adrs/` — accepted architecture decisions (e.g. sandboxing posture,
  MCP dispatch strategy).
- `docs/rfcs/` — protocol/interface specs (job-wakeup channel, job-platform
  MCP tools, clownfile schema, plugin sandboxing).
- `docs/features/` — feature-level design records (job-wakeup channel,
  hats/profile selection, single-entrypoint matrix).
- `docs/plans/` — active implementation plans (e.g. the profiles system).

When working on a subsystem covered by one of these, read the doc first;
when a change alters documented behavior, update the doc in the same PR.

## Nix conventions

`nixpkgs` tracks the `amarbel-llc/nixpkgs` fork's thin-overlay master. The
secondary inputs (`nixpkgs-master`, `nixpkgs-claude-code` — now unused,
kept for reference, `nixpkgs-codex`, `nixpkgs-llama`) are pinned to
pre-migration (full-fork) SHAs, where their packages still exist as
conventional `pkgs/by-name/` definitions; bump those conservatively to a
newer pre-migration SHA, not the thin-wrapper tip. `llm-agents` (source of
`crush`) follows our main `nixpkgs`.

## Spinclass integration

Worktree-based development managed by Spinclass. The `sweatfile` configures
a pre-merge hook that runs `just` (the full build) before merging a
worktree branch into master.
