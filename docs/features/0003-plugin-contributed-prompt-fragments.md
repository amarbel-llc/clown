---
status: testing
date: 2026-04-27
---

# Plugin-Contributed System Prompt Fragments

## Abstract

Plugins loaded via `mkCircus` today contribute only `--plugin-dir` content
(skills, hooks, MCP servers). They have no mechanism to ship prompt
fragments alongside clown's builtin `system-prompt-append.d/`. This record
specifies a convention by which a plugin MAY ship `*.md` fragments under
`<plugin>/.clown-plugin/system-prompt-append.d/`, and `mkCircus` resolves
those paths at build time so `clown(1)` injects them between its builtin
fragments and the user's `.circus/system-prompt.d/` fragments.

The design is motivated by issue #10. It introduces the `.clown-plugin/`
directory inside a plugin tree as the namespace for clown-specific
plugin metadata. Migrating `clown.json` into the same namespace is
deliberately deferred to a follow-up issue (#32).

## Motivation

Some plugins need to inject behavioral instructions into the system
prompt — a plugin shipping MCP tools may also need prompt context
explaining when/how to use those tools, and a plugin shipping skills
may need instructions about when to invoke them. Today this requires
the consumer to manually duplicate those instructions in their own
`.circus/system-prompt.d/` directory, which defeats the purpose of
self-contained plugins.

Plugins already have prompt-injection capability via skills and hooks
(per RFC 0001 §Security). Surfacing a first-class fragment path makes
the loud, declarative form available; not surfacing it forces plugin
authors into the quieter, more roundabout forms.

## Non-Goals

This design does **not** introduce per-plugin prompt opt-out at the
consumer level. Consumers who do not trust a plugin's prompt content
should not load that plugin at all — the threat model in RFC 0001
§Security treats prompt injection as an existing capability of every
loaded plugin.

This design does **not** migrate `clown.json` into the new
`.clown-plugin/` directory. Once `.clown-plugin/` exists as a real
organizing principle, leaving `clown.json` at the plugin root is
asymmetric, but moving it is a coordinated migration of every known
plugin and is tracked separately as #32.

This design does **not** add a "replace mode" for plugin-contributed
fragments. Plugins append; only the user's `.circus/system-prompt`
file may replace.

## Design Overview

### 1. Convention

A plugin MAY ship one or more `*.md` files under
`<plugin>/.clown-plugin/system-prompt-append.d/`. Each `.md` file's
contents are concatenated into the assembled append-mode prompt.

The directory is OPTIONAL. Plugins that ship no prompt fragments do
not need to create the directory. Empty directories are tolerated.

The directory MUST be at the plugin root (next to `.claude-plugin/`),
not inside `.claude-plugin/`. Mixing clown-specific files into the
claude-code manifest dir would conflate two protocols.

### 2. Build-time resolution

`mkCircus`'s `resolvePlugins` step (per RFC 0001 §2.1) is extended:
for every resolved plugin directory, if
`<plugin-dir>/.clown-plugin/system-prompt-append.d/` exists and is a
directory, its absolute store path is appended to a new file
`$out/plugin-fragment-dirs` inside the `CLOWN_PLUGIN_META`
derivation. The file's format mirrors `plugin-dirs`: one absolute
path per line, in plugin-list order (the order the consumer wrote
in `mkCircus`).

`plugin-fragment-dirs` is always present in `CLOWN_PLUGIN_META`,
even when no plugin contributes fragments — it MAY be empty.

### 3. Runtime layering

`cmd/clown` reads `$CLOWN_PLUGIN_META/plugin-fragment-dirs` (similar
to how it reads `plugin-dirs`) and assembles a list of "builtin"
append directories in this order:

1. `buildcfg.SystemPromptAppendD` — clown's compiled-in fragments
2. The plugin fragment dirs in plugin-list order

That list is passed to `promptwalk.WalkPrompts`. Within each
directory the existing rule applies: `*.md` files in
lexicographically sorted order, separated by two newlines. The
user's `.circus/system-prompt.d/` fragments still come last,
shallowest-first.

### 4. Replace-mode interaction

When the user supplies a `.circus/system-prompt` file (replace mode),
clown invokes `--system-prompt-file` AND `--append-system-prompt-file`.
Plugin-contributed fragments continue to flow through the latter and
are still injected. Replace mode applies to the user-supplied prompt,
not to the plugin-supplied "how to use my tool" instructions; silently
dropping plugin fragments because the user replaced their own prompt
would surprise plugin authors and break tool affordances.

### 5. Trust posture

Plugin-contributed fragments inherit the plugin's existing trust
tier (RFC 0001 §Security). Consumers who load a plugin via
`mkCircus` already trust it to register skills, hooks, and MCP
tools. Prompt fragments are a strictly weaker capability than any
of those.

## Compatibility

This is additive. Plugins that do not ship a
`.clown-plugin/system-prompt-append.d/` directory behave exactly as
before. The `plugin-fragment-dirs` file is new, so no existing
`CLOWN_PLUGIN_META` reader can be confused by it.

`promptwalk.WalkPrompts`'s signature changes from a single
`builtinAppendDir string` to a `builtinAppendDirs []string`. This is
an internal-only API change; callers are confined to the clown
repository.

## Future Work

- **#32 — Migrate `clown.json` into `.clown-plugin/`.** Now that
  `.clown-plugin/` exists for fragments, leaving `clown.json` at the
  plugin root is asymmetric. Tracked separately because it requires
  coordinated migration of every known plugin (moxy, bob and bundled,
  synthetic-plugin, and consumer flakes).
- **Per-fragment metadata.** A future need may emerge to attach
  metadata (priority, scope, conditional inclusion) to a fragment.
  Today's flat directory + lexicographic-sort convention has room
  to grow into a TOML/YAML index file under
  `.clown-plugin/system-prompt-append.d/index.toml` without
  invalidating existing plugins.

## Alternatives Considered

### A. Fragments at the plugin root, not under `.clown-plugin/`

Place fragments directly at `<plugin>/system-prompt-append.d/`,
mirroring clown's repo-level layout exactly.

Rejected. The plugin root is shared with claude-code's own
plugin-manifest concerns; introducing a clown-specific directory
there would risk a future name collision if claude-code ever defines
its own meaning for `system-prompt-append.d`. The `.clown-plugin/`
prefix scopes the directory unambiguously to clown.

### B. Opt-in per plugin in `mkCircus`

Require consumers to write
`{ flake = bob; dirs = [...]; promptFragments = true; }` to opt into
each plugin's prompt fragments.

Rejected. Plugin loading already implies trust in skill, hook, and
MCP-tool injection — gating the strictly weaker prompt-fragment
capability behind an extra flag adds friction without buying real
isolation. Consumers who mistrust a plugin should not load it.

### C. Plugins symlinked into a unified at-build-time directory

Have `mkCircus` create a single store-path directory containing
symlinks to clown's builtin fragments and every plugin's fragments,
then pass that single path through `buildcfg.SystemPromptAppendD`.

Rejected. The symlink-merge approach loses provenance (no way to
tell from the store path which plugin contributed which fragment)
and silently allows filename collisions to clobber each other.
Per-plugin directories preserve both, at the cost of a 15-line Go
loop.

## References

- Issue #10 — Explore plugin-contributed system-prompt-append.d
  fragments in mkCircus
- Issue #32 — Migrate clown.json into .clown-plugin/ namespace
- RFC 0001 — Parameterized Plugin Loading
- RFC 0002 — Clown Plugin Protocol
- `clown(1)` — SYSTEM PROMPT INJECTION section
