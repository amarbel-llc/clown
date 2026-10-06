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

`juggler` is also the fleet's **headless agent substrate** (FDR 0019,
`docs/features/0019-juggler-agent-substrate.md`): `juggler run` drives a
tool-use loop against moxy under a spawner-minted principal, `juggler spawn`
launches it as a systemd transient unit with a ringmaster job and a
post-stop exit wake, `juggler decide` is the Decisions-API router. Code lives
in `internal/juggler{loop,brief,eval,tools,decide,run}`; read the FDR before
touching any of it.

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
- **There is no go.mod, go.sum, gomod2nix.toml or vendor/ — dependencies
  live in `go.nix` (igloo FDR 0008), and Go builds/tests run only inside
  nix.** The devShell has no Go toolchain; `go build`/`go test` in the
  checkout cannot work. igloo renders go.mod from `go.nix` inside each
  derivation; fleet modules (ringmaster, tommy, purse-first's dewey) are
  `flakeInputs` entries resolved to the flake inputs' `go-pkgs`, so the
  flake.lock rev IS their version. To add or bump a third-party dep, run
  `just run-godyn-go go get <mod>@<ver>` (godyn's escape hatch; needs the
  host's impure-derivations feature), or edit `go.nix` by hand: `version`,
  `hash` (the NAR hash — copy it from a sibling repo's go.nix, or put a
  dummy and take the real one from the failed build's hash-mismatch
  message), and the module's own `go` version. Unit tests:
  `just test-go-godyn`. Editors: `just debug-render-go-mod` writes the
  nix-side go.mod to `.tmp/gomod/` (gopls/dlv are otherwise unsupported). Worse: once `gomod2nix.toml` lacks a module
  that tracked Go code imports, `just update-gomod2nix` cannot run at all
  (entering the devShell evaluates the godyn graph, which fails on the
  missing module) — add the entry by hand, copying `version`/`hash` from a
  sibling repo that already pins it. The planned godyn `go.nix` migration
  (igloo FDR 0008) removes `gomod2nix.toml` and `vendor/` entirely.
- **New untracked files are invisible to `nix build` (and hence `just
  build`).** `nix build` reads the git-tracked snapshot, not the working
  tree — a new `.go` file (or new directory) that hasn't been `git add`ed
  is simply absent from the build, which fails with a misleading
  "undefined: X" or "cannot find package" error. `git add` the
  new path before building (staging is enough; no commit needed).
- **`huh` (`charmbracelet/huh`) can't do live cross-field cascades or
  seamless multi-field arrow-key scrolling — reach for a bare
  `charmbracelet/bubbles/list` program instead.** `huh.MultiSelect`'s
  Up/Down cursor clamps at that field's own option-list boundary and only
  crosses to the next field on Enter/Tab (`field_multiselect.go`'s
  `Up`/`Down` cases call `max(cursor-1, 0)`/`min(cursor+1, len-1)`, no
  wraparound signal), so packing several `MultiSelect` fields into one
  `huh.Group` makes j/k/arrows stop dead at each field instead of
  scrolling through everything. `huh` also exposes no `onChange`/`onToggle`
  hook — only a post-submit `Validate` — so "toggling this checkbox should
  immediately flip these other checkboxes" (a parent/children cascade) is
  not expressible with `huh` at all. `cmd/clown/cheapcontext_picker.go`'s
  `--cheap-context` picker hit both limits and was rewritten as a bare
  `bubbles/list` program: a custom `list.Item` type carries whatever extra
  state you need (checked, parent/child links), and a custom
  `list.ItemDelegate.Update` runs *before* the list consumes a keypress —
  that's the interception point `huh` doesn't have. `huh.Option` is also
  single-line-only (just a `Key string` + `Value`, no title/description
  split) — `cmd/clown/openroutermodelpicker.go`'s dynamic OpenRouter model
  picker hit that limit (needing a two-line id/pricing + description row
  per model) and is a second bare `bubbles/list` program for the same
  reason, using `list.DefaultDelegate` rather than a custom one since it's
  a flat single-select list with no cross-row cascade. Keep using `huh` for
  simple, single-interaction forms (confirm prompts, the profile add/edit
  form in `cmd/clown/profileform.go` itself); reach for `bubbles/list` the
  moment a picker needs cross-row side effects, richer per-row layout than
  a single line, or one continuously-scrollable list spanning what would
  otherwise be several `huh` fields.

## Build commands

```sh
just build       # Default: nix build --show-trace — the authoritative check
just test-go-godyn  # Go unit suite on godyn's per-package lane (the only unit lane)
just test-go-race   # Go unit suite under -race (not part of `just test`)
just run-godyn-go go get <mod>@<ver>  # go commands needing a module, ingested into go.nix
just clean-result  # delete result symlinks
```

Format: `nix fmt` (conformist; overlay in `conformist.nix`, wired via `flake.nix`).

## Architecture at a glance

The flake's `symlinkJoin` output bundles: the `clown` wrapper script, the
`clown-go` Go binary (`cmd/clown/main.go` — the main entrypoint: provider
dispatch, system-prompt injection, MCP plugin lifecycle via
`internal/pluginhost`, `clownfile`-driven config via `internal/clownfile`,
multiplexer self-attach via `internal/ptysuspend`/`cmd/clown/attach.go`,
per-launch artifact staging via `internal/staging`, and tent (sandboxed
container) support via `internal/tent`), the standalone
`clown-plugin-host` binary, the `clown-stdio-bridge` binary (wraps a
single stdio MCP server over streamable-HTTP; see `internal/mcphttp`),
the `clown-mcp-collapse` binary (aggregates N upstream MCP servers behind
three generic verbs — `mcp_list`/`mcp_describe`/`mcp_call` — to save agent
context; see `internal/mcpcollapse`), `clown sessions-complete`
fish-completion support, and `completions/clown.fish`.

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
