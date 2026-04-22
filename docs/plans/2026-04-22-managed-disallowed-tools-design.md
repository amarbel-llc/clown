# Managed Disallowed Tools with --naked Escape Hatch

## Context

Clown wraps the Claude CLI and currently hardcodes two `--disallowed-tools`
entries: `Bash(*)` and `Agent(Explore)`. The only way to undo these restrictions
is `--clean`, which bypasses *all* clown wrapping (prompts, plugins, tool
restrictions) — a sledgehammer when you just want shell access.

Additionally, with moxy providing MCP-based replacements for many Claude
builtins (folio for file ops, rg for search, chrest for web access, lux for
LSP), more builtins should be disabled to force traffic through the moxy control
plane.

## Changes

### 1. Rename --clean to --naked

Replace `--clean` with `--naked` everywhere — flag parsing, manpage, fish
completions. No deprecation period. Same behavior: bypass all clown wrapping and
exec the provider directly.

### 2. Build-time disallowed-tools file

A plain text file, one glob pattern per line, baked into the nix build:

```
Bash(*)
Agent(Explore)
WebFetch
WebSearch
Write
EnterWorktree
NotebookEdit
PowerShell
LSP
Glob
Grep
```

- New build config var: `buildcfg.DisallowedToolsFile`
- Injected via ldflags in `flake.nix`, same pattern as `AgentsFile`
- The file is created as a nix derivation (`pkgs.writeText` or similar)

### 3. BuildClaudeArgs reads the file

- Add `DisallowedToolsFile string` to the `ClaudeArgs` struct
- Read the file, split by newlines, trim whitespace, skip blanks and
  `#`-prefixed comment lines
- Emit `--disallowed-tools <pattern>` for each entry
- Remove the two hardcoded `--disallowed-tools` lines from `claude.go`
- If the file is empty or unset, no disallowed-tools flags are emitted

### 4. --naked is the only escape hatch

No per-tool override flags. If you need a disallowed tool, run `clown --naked`
to get an unmodified Claude session. This keeps the CLI surface minimal.

## Files to modify

| File | Change |
|------|--------|
| `internal/buildcfg/buildcfg.go` | Add `DisallowedToolsFile` var |
| `internal/provider/claude.go` | Read file, emit per-line `--disallowed-tools`, remove hardcoded lines |
| `internal/provider/args_test.go` | Update tests for file-based disallowed tools |
| `cmd/clown/main.go` | Rename `clean` to `naked` in `parsedFlags` and `parseFlags`, pass `DisallowedToolsFile` to `ClaudeArgs` |
| `cmd/clown/main_test.go` | Update flag tests for `--naked` |
| `flake.nix` | Create disallowed-tools file, add ldflags entry |
| `man/man1/clown.1` | Rename `--clean` to `--naked` |
| `completions/clown.fish` | Rename `--clean` to `--naked` |

## Rollback

Revert the flake.nix change (removes the file and ldflags entry), restore the
two hardcoded lines in `claude.go`, and rename `--naked` back to `--clean`. One
commit.
