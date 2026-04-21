# clown

A Nix-packaged wrapper around [Claude Code](https://docs.anthropic.com/en/docs/claude-code)
that injects custom system prompts hierarchically, disables Bash by default,
and supports parameterized plugin loading.

## Install

Clown is a Nix flake. The bare wrapper (no plugins) can be run directly:

```sh
nix run github:amarbel-llc/clown
```

To build clown with plugins, use `mkCircus` from your own flake (see
[Plugins](#plugins) below).

## What it does

Clown wraps the `claude` binary with four additions:

1. **Bash disabled by default** — passes `--disallowed-tools 'Bash(*)'` to
   every invocation.

2. **Auto-mode disabled permanently** — the `claude-code` bundle is patched
   to read its managed-settings from clown's own store path, which ships
   with `permissions.disableAutoMode: "disable"`. Managed settings sit at
   the highest precedence tier, so neither user settings nor CLI flags can
   re-enable auto-mode through clown.

3. **Hierarchical system prompt injection** — walks from `$PWD` up to `$HOME`,
   collecting `.circus/` directories along the way:

   - **Replace mode**: If any directory contains `.circus/system-prompt`, the
     deepest one replaces Claude's system prompt entirely
     (`--system-prompt-file`).
   - **Append mode**: All `.md` files found in `.circus/system-prompt.d/`
     directories are concatenated shallowest-first and appended to the system
     prompt (`--append-system-prompt-file`). Builtin fragments (in
     `system-prompt-append.d/`) are always prepended before user fragments.

4. **Fish shell completions** — full completions for all Claude Code flags, with
   dynamic session ID completion for `--resume` via the bundled
   `clown-sessions` utility.

All other arguments are passed through to `claude` unchanged.

## Plugins

Clown ships with zero default plugins. Consumers supply plugin flake inputs
via `mkCircus`, which resolves plugin directories and burns `--plugin-dir`
flags into the wrapper at build time.

### mkCircus

`clown.lib.${system}.mkCircus` accepts a single attribute set with a
`plugins` list. Each plugin definition has two fields:

| Field | Type | Description |
|-------|------|-------------|
| `flake` | flake input | The plugin's flake input value |
| `dirs` | list of strings | Paths relative to the flake's default package output. Literal paths or glob patterns. |

It returns `{ packages.default, devShells.default, checks }`.

### Consumer example

```nix
{
  inputs = {
    clown.url = "github:amarbel-llc/clown";
    clown.inputs.nixpkgs.follows = "nixpkgs";
    moxy.url = "github:amarbel-llc/moxy";
    moxy.inputs.nixpkgs.follows = "nixpkgs";
    bob.url = "github:amarbel-llc/bob";
    bob.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs = { self, nixpkgs, clown, moxy, bob, ... }:
    let
      system = "aarch64-darwin";
      circus = clown.lib.${system}.mkCircus {
        plugins = [
          { flake = moxy; dirs = [ "share/purse-first/moxy" ]; }
          { flake = bob;  dirs = [ "share/purse-first/*" ]; }
        ];
      };
    in {
      packages.${system}.default = circus.packages.default;
    };
}
```

### Plugin directory requirements

Each path in `dirs` must point to a directory containing
`.claude-plugin/plugin.json` with at minimum a `name` field. Glob patterns
(`*`, `?`, `[`) are expanded at build time; non-plugin directories are
silently skipped. A glob that matches zero plugins fails the build.

### Version output

`clown version` displays all components including dynamically loaded plugins:

```
COMPONENT            VERSION      REV
bob/bob              -            474d0c4fac8a084c5378dba51337fea45b86ee2d
bob/caldav           -            474d0c4fac8a084c5378dba51337fea45b86ee2d
claude-code          1.0.46       b2b9662ffe1e9a5702e7bfbd983595dd56147dbf
clown                0.0.1        edc5db5...
codex                0.0.1-beta   e2dde111aea2c0699531dc616112a96cd55ab8b5
moxy/moxy            -            a90e0dfbc830700efe28d2238bd2acb5bf8095dc
```

Plugin rows use `<flake-name>/<plugin-name>` format. Version and rev come
from `plugin.json` and the flake input respectively.

For the full specification, see [RFC 0001](docs/rfcs/0001-parameterized-plugin-loading.md).

### HTTP MCP Servers (`clown.json`)

Plugins can declare HTTP-based MCP servers that clown automatically launches
and manages. This enables MCP features unavailable over stdio, including
`notifications/tools/list_changed` and server-initiated requests.

A plugin ships a `clown.json` alongside `.claude-plugin/`:

```json
{
  "version": 1,
  "httpServers": {
    "my-server": {
      "command": "bin/my-server",
      "transport": "streamable-http",
      "healthcheck": {
        "path": "/healthz",
        "interval": "1s",
        "timeout": "30s"
      }
    }
  }
}
```

Servers must implement the clown plugin protocol: bind to an ephemeral port,
print a handshake line to stdout (`1|1|tcp|<addr>|streamable-http`), and
respond to health checks. See [RFC 0002](docs/rfcs/0002-clown-plugin-protocol.md)
for the full specification.

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
