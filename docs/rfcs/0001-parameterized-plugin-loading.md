---
status: testing
date: 2026-04-20
---

# Parameterized Plugin Loading

## Abstract

Clown currently hardcodes two Claude Code plugins (moxy and bob) as flake
inputs. This specification defines a `mkCircus` function interface that allows
consumers to supply an arbitrary list of plugin definitions — each consisting
of a flake input and a list of plugin directory paths (literal or glob) within
that flake's package output. Plugin directories, version metadata, and
`--plugin-dir` flags are all resolved at build time and burned into the
resulting artifact.

## Introduction

Clown wraps Claude Code with system prompt injection, managed settings, and
plugin loading. Today, plugin loading is hardcoded in `flake.nix`:

```nix
moxyPluginDir = "${moxy.packages.${system}.default}/share/purse-first/moxy";
bobPluginDir = "${bob.packages.${system}.default}/share/purse-first/bob";
# ...
extra_args+=(--plugin-dir "${moxyPluginDir}" --plugin-dir "${bobPluginDir}")
```

This means consumers cannot add, remove, or replace plugins without forking
clown. It also means clown's flake carries transitive dependency weight (moxy
and bob input trees) that not all consumers need.

The goal is to make clown plugin-agnostic: ship with zero default plugins and
let the consumer's flake (e.g. `~/eng/flake.nix`) supply plugin definitions
that get burned into the build. This is analogous to how nixpkgs parameterizes
language ecosystems — `python3.withPackages (ps: [ ps.requests ])`,
`php.withExtensions`, etc.

### Pre-existing issues discovered during investigation

Bob's default package (`bob-all`) bundles four plugins under
`share/purse-first/`: `bob`, `caldav`, `lux`, and `tap-dancer`. However, clown
currently only injects `share/purse-first/bob`, silently ignoring the other
three. The explicit-path interface specified below forces consumers to
enumerate which plugin directories they want (or explicitly opt into glob
discovery), preventing this class of silent omission.

## Requirements Language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in RFC 2119.

## Specification

### 1. Plugin Definition Schema

Each plugin definition is a Nix attribute set with the following fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `flake` | flake input | Yes | The flake input value from the consumer's `outputs` function |
| `dirs` | list of strings | Yes | Paths relative to the flake's default package output. Each entry is either a literal path to a plugin directory or a glob pattern (see section 1.1) |

Example with literal paths:

```nix
{
  flake = bob;
  dirs = [
    "share/purse-first/bob"
    "share/purse-first/caldav"
    "share/purse-first/lux"
    "share/purse-first/tap-dancer"
  ];
}
```

Example with glob:

```nix
{
  flake = bob;
  dirs = [ "share/purse-first/*" ];
}
```

The `dirs` field MUST contain at least one entry. Each entry MUST be a
relative path (no leading `/`). Paths are resolved against
`${flake.packages.${system}.default}` at build time.

#### 1.1 Glob Expansion

Entries in `dirs` that contain shell glob characters (`*`, `?`, `[`) MUST
be expanded at build time using shell globbing within a `runCommand`. Each
path produced by expansion MUST be validated per the rules in section 2.1
(i.e., must contain `.claude-plugin/`). Paths that expand but do not
contain `.claude-plugin/` MUST be silently skipped — they may be
non-plugin data such as documentation.

If a glob pattern expands to zero valid plugin directories, the build MUST
fail with an error message indicating the glob matched no plugins.

Glob expansion is an opt-in convenience for consumers who trust all plugins
in a flake's output. Consumers who want full control over which plugins are
loaded SHOULD use literal paths.

#### 1.2 Plugin Manifest

Each plugin directory MUST contain a `.claude-plugin/plugin.json` manifest.
The manifest MUST include at minimum a `name` field (non-empty string). The
manifest MAY include a `version` field (string). These fields are used for
version output (section 2.2).

Current state of known plugins:

| Plugin | `name` | `version` |
|--------|--------|-----------|
| moxy | `"moxy"` | absent |
| bob | `"bob"` | absent |
| caldav | `"caldav"` | absent |
| lux | `"lux"` | absent |

The Claude Code plugin.json schema supports a `version` field (see
claude-code-settings(5), PLUGIN MANIFEST section). Plugins SHOULD populate
this field to enable per-plugin version tracking.

### 2. mkCircus Interface

Clown's flake MUST export a `lib.mkCircus` function within the
`eachDefaultSystem` callback. At the flake output level, this appears as
`lib.${system}.mkCircus` due to how `eachDefaultSystem` merges per-system
attribute sets.

This follows the nixpkgs stdlib pattern where builder functions like
`pkgs.mkShell` are defined within the per-system scope (capturing `system`
and `pkgs` from lexical closure) and accessed through the package set. Since
clown is a standalone flake rather than a nixpkgs overlay, the equivalent
export path is `lib.${system}`.

The function accepts a single attribute set argument:

```nix
mkCircus {
  plugins = [
    {
      flake = moxy;
      dirs = [ "share/purse-first/moxy" ];
    }
    {
      flake = bob;
      dirs = [ "share/purse-first/*" ];
    }
  ];
}
```

The function MUST return an attribute set containing at minimum:

| Attribute | Type | Description |
|-----------|------|-------------|
| `packages.default` | derivation | The clown wrapper with plugins burned in |
| `devShells.default` | derivation | Development shell |
| `checks.*` | derivation(s) | Managed-settings and plugin-validation tests |

Note: these are per-system attributes (without the `${system}` key) because
`mkCircus` is called inside `eachDefaultSystem`, which handles the
per-system wrapping.

#### 2.1 Plugin Directory Resolution

For each element in `plugins`, `mkCircus` MUST:

1. Resolve the default package: `plugin.flake.packages.${system}.default`
2. For each path in `plugin.dirs`:
   a. If the path contains glob characters, expand it via shell globbing
   b. Otherwise, treat it as a literal path
3. For each resolved path, compute the absolute store path: `${pkg}/${path}`
4. At **build time** (inside a derivation or `runCommand`), verify that
   each resolved path contains `.claude-plugin/plugin.json`
5. Extract `name` and `version` (if present) from each `plugin.json`
6. Emit a `--plugin-dir ${resolved_path}` flag for each valid directory
7. Write a version-info file (see section 2.2) containing metadata for
   all discovered plugins

Steps 2-7 MUST happen at build time, not at Nix evaluation time, because
store paths are not available for inspection during pure evaluation.

If a literal path (non-glob) fails validation, the build MUST fail with
an error message of the form:

```
clown: plugin directory does not contain .claude-plugin/:
  flake: <derivation name>
  path: <resolved store path>
```

If a glob path expands to entries without `.claude-plugin/`, those entries
MUST be silently skipped. If the glob produces zero valid plugin
directories, the build MUST fail.

#### 2.2 Version Output

The `clown version` command MUST display version information at two levels:
per-plugin-directory and per-flake.

At build time, the plugin resolution step (section 2.1) MUST produce a
version-info file containing one entry per discovered plugin directory.
Each entry MUST include:

| Field | Source |
|-------|--------|
| plugin name | `name` from `.claude-plugin/plugin.json` |
| plugin version | `version` from `.claude-plugin/plugin.json`, or `-` if absent |
| flake name | `plugin.flake.packages.${system}.default.name` (passed as a build arg) |
| flake rev | `plugin.flake.rev or plugin.flake.dirtyRev or "dirty"` (passed as a build arg) |

The flake name and rev MUST be passed into the build step as arguments
(e.g., via `runCommand` args or environment variables), since flake
attributes are available at evaluation time but the version-info file is
produced at build time.

The wrapper MUST read this file at runtime to produce the version table.
The output format MUST be:

```
COMPONENT      VERSION      REV
bob/bob        -            474d0c4fac8a084c5378dba51337fea45b86ee2d
bob/caldav     -            474d0c4fac8a084c5378dba51337fea45b86ee2d
bob/lux        -            474d0c4fac8a084c5378dba51337fea45b86ee2d
bob/tap-dancer -            474d0c4fac8a084c5378dba51337fea45b86ee2d
claude-code    1.0.46       b2b9662ffe1e9a5702e7bfbd983595dd56147dbf
clown          0.0.1        edc5db5...
codex          0.0.1-beta   e2dde111aea2c0699531dc616112a96cd55ab8b5
moxy/moxy      -            a90e0dfbc830700efe28d2238bd2acb5bf8095dc
```

Where the COMPONENT column uses the format `<flake-name>/<plugin-name>` for
plugins, distinguishing them from built-in components (claude-code, clown,
codex) which have no prefix.

Rows MUST be sorted alphabetically by COMPONENT name. The built-in
components (claude-code, clown, codex) MUST always be present regardless
of which plugins are supplied.

When a plugin's `plugin.json` includes a `version` field, that value MUST
appear in the VERSION column instead of `-`.

### 3. Consumer Usage

A consumer flake MUST structure its inputs to include both clown and any
desired plugin flakes:

```nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";
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
      devShells.${system}.default = circus.devShells.default;
    };
}
```

The consumer is responsible for:

- Pinning plugin flake inputs to specific revisions (RECOMMENDED)
- Managing `follows` relationships to ensure compatible nixpkgs trees
- Choosing between explicit paths and globs per plugin flake

### 4. Migration

When this specification is implemented:

1. The `moxy` and `bob` flake inputs MUST be removed from clown's `flake.nix`
2. The `packages.moxy` export MUST be removed (currently unused)
3. The hardcoded `moxyPluginDir`, `bobPluginDir`, and associated rev/shortRev
   variables MUST be removed
4. The hardcoded `--plugin-dir` flags in the wrapper MUST be replaced with
   flags read from the build-time plugin resolution output
5. The hardcoded version printf lines for bob and moxy MUST be replaced with
   the version-info file mechanism
6. The existing consumer (`~/eng/flake.nix`) MUST be updated to call
   `mkCircus` with explicit plugin definitions
7. Clown's README and `clown(1)` manpage MUST be updated to document the
   new plugin interface

## Security Considerations

Plugins are Claude Code extensions that can register MCP tools, hooks, and
system prompt fragments. A plugin has the same trust level as any tool
available to the Claude Code session — it can read files, make network
requests (if permitted), and influence model behavior through prompt
injection.

Consumers MUST only supply plugin flake inputs from sources they trust.
Pinning plugin flake inputs to specific revisions (rather than following
a branch HEAD) is RECOMMENDED to prevent supply-chain drift.

Clown's existing managed-settings guardrails (Bash disabled, auto-mode
disabled) apply regardless of which plugins are loaded. Plugins cannot
override managed settings because those are burned into the patched
claude-code derivation at a higher precedence tier.

When using literal `dirs` paths, the consumer explicitly controls which
plugin directories are loaded, preventing a malicious or buggy plugin
flake from silently injecting additional plugin directories. When using
glob patterns, the consumer explicitly opts into trusting all plugin
directories within the matched path — this trades granularity for
convenience and SHOULD only be used with trusted flake inputs.

## Compatibility

This is a breaking change to clown's flake interface. Consumers that
currently depend on `clown.packages.${system}.default` receiving moxy
and bob automatically will need to update their flakes to call `mkCircus`
with explicit plugin definitions.

After migration, `clown.packages.${system}.default` will be a bare clown
wrapper with no plugins. Consumers MUST use `mkCircus` to produce a
plugin-equipped build.

Since clown has a small number of known consumers (primarily
`~/eng/flake.nix`), this migration can be coordinated directly. No
deprecation period is necessary.

## References

### Normative

- [Claude Code --plugin-dir](https://docs.anthropic.com/en/docs/claude-code) — Plugin directory structure and `plugin.json` manifest format
- [claude-code-settings(5), PLUGIN MANIFEST](man/man5/claude-code-settings.5) — Plugin manifest schema including `name`, `version`, `description`, `skills`, `commands`, `hooks` fields
- [RFC 2119](https://www.rfc-editor.org/rfc/rfc2119) — Requirement keyword definitions

### Informative

- [Nix flake outputs schema](https://nix.dev/manual/nix/latest/command-ref/new-cli/nix3-flake.html) — Standard flake output attributes
- [nixpkgs python3.withPackages](https://nixos.org/manual/nixpkgs/stable/#python) — Analogous parameterization pattern in nixpkgs
