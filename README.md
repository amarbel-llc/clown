# clown

A Nix-packaged wrapper around [Claude Code](https://docs.anthropic.com/en/docs/claude-code)
that injects custom system prompts hierarchically and disables Bash by default.

## Install

Clown is a Nix flake. Add it as an input or run directly:

```sh
nix run github:amarbel-llc/clown
```

## What it does

Clown wraps the `claude` binary with three additions:

1. **Bash disabled by default** — passes `--disallowed-tools 'Bash(*)'` to
   every invocation.

2. **Hierarchical system prompt injection** — walks from `$PWD` up to `$HOME`,
   collecting `.circus/` directories along the way:

   - **Replace mode**: If any directory contains `.circus/system-prompt`, the
     deepest one replaces Claude's system prompt entirely
     (`--system-prompt-file`).
   - **Append mode**: All `.md` files found in `.circus/system-prompt.d/`
     directories are concatenated shallowest-first and appended to the system
     prompt (`--append-system-prompt-file`). Builtin fragments (in
     `system-prompt-append.d/`) are always prepended before user fragments.

3. **Fish shell completions** — full completions for all Claude Code flags, with
   dynamic session ID completion for `--resume` via the bundled
   `clown-sessions` utility.

All other arguments are passed through to `claude` unchanged.

## .circus directory

Place prompt fragments anywhere in your directory hierarchy:

```
~/.circus/system-prompt.d/
    00-global-rules.md        # applied everywhere
~/projects/.circus/system-prompt.d/
    00-coding-standards.md    # applied in ~/projects and below
~/projects/myapp/.circus/system-prompt
                              # replaces system prompt entirely for myapp
```

- Append fragments (`.circus/system-prompt.d/*.md`) stack from shallowest to
  deepest — broader rules come first, project-specific ones last.
- A replace file (`.circus/system-prompt`) short-circuits the default system
  prompt. Only the deepest one wins. Append fragments still apply on top.

## Building

Requires Nix with flakes enabled.

```sh
just build    # nix build --show-trace
just clean    # rm -rf result
```

## License

MIT — see [LICENSE](LICENSE).

---

:clown_face: Sasha, Head Clown
