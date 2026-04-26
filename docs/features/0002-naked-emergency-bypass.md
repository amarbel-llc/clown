---
status: accepted
date: 2026-04-26
---

# `--naked`: emergency bypass of clown wrapping

## Problem Statement

clown wraps the underlying provider (claude, codex, circus, opencode, clownbox)
with several pieces of machinery — system-prompt injection, plugin host,
disallowed-tools defaults, post-exit resume hint, and others. When any of those
layers misbehave (a plugin server fails to start, a system-prompt fragment is
malformed, a buggy clown release blocks the session), users still need a fast
way to reach the provider directly. `--naked` is that escape hatch: a flag
that short-circuits clown's pipeline entirely and execs the provider with the
user's verbatim arguments.

## Interface

`clown --naked --provider <name> -- <provider-args...>` short-circuits clown's
pipeline and runs the provider with the user's verbatim arguments.

What is bypassed:

- Plugin host (no plugin servers started; `--plugin-dir` is not injected)
- System-prompt injection (no `--system-prompt-file` / `--append-system-prompt-file`)
- Disallowed-tools defaults (`Bash(*)`, `Agent(Explore)` are not added)
- Codex `--sandbox workspace-write` default
- Post-exit resume hint (no `clown resume clown://...` line printed)
- Custom keymap and confirmation dialogs
- Profile selection and the interactive profile picker

What is preserved:

- The current process environment, exactly as inherited
- Every argument after `--`, forwarded to the provider verbatim
- The user's choice of provider via `--provider` or `CLOWN_PROVIDER`

How it runs: clown locates the provider binary on `PATH` and replaces itself
via `syscall.Exec`. After the call there is no clown process — the provider
*is* the process, so signal delivery, PID, and `ps` output are direct.

Restrictions:

- `--naked` with `--provider opencode` is rejected: opencode requires the
  config-file injection that `--naked` bypasses, so the result would be
  unusable.
- `--naked` is silent. clown prints nothing of its own; everything visible to
  the user comes from the provider.

## Examples

```sh
# Plugin host is wedged; reach claude directly.
clown --naked -- --print "what's broken?"

# A system-prompt fragment is causing a parser error in claude.
clown --naked -- chat

# Run codex without clown's workspace-write sandbox default.
clown --naked --provider codex -- exec "ls"

# Confirm the provider sees no clown env or arg surface (no MCPs, no system
# prompt, just argv).
clown --naked -- --debug
```

## Limitations

**No resume hint.** clown's post-exit "resume with: clown resume clown://..."
line is suppressed. The provider's own resume hint (e.g. claude printing
`/resume <id>`) still fires if the provider prints one. The clown-flavored
URI form is not available because clown is not in the process tree to print
it.

**No plugin MCPs visible to the provider.** Plugin servers are not started,
and the `--plugin-dir` arguments that point claude at staged manifests are
not injected. Plugins authored against clown's `clown.json` are simply not
present in the session.

**No managed safety defaults.** The disallowed-tools list, codex's
workspace-write sandbox, and other clown-managed defaults are not applied.
The provider runs with whatever defaults it ships with.

**No profile selection.** Profiles are skipped: neither the interactive
picker nor the `--profile` lookup runs. `--provider` (or `CLOWN_PROVIDER`)
is the only way to choose a backend in `--naked` mode.

**`--provider opencode` is rejected.** opencode needs clown to write a
temporary `opencode.json` and point the binary at it via `XDG_CONFIG_HOME`;
without that injection there is no usable backend, so clown errors out
rather than launch a guaranteed-broken session.

**Not the everyday switch.** `--naked` exists for diagnostic or emergency
use when something in the main pipeline misbehaves. Disabling individual
layers (`--disable-clown-protocol`, `--skip-failed`) is preferable when only
one piece is the problem; `--naked` is the all-or-nothing fallback.

## More Information

- Implementation: `cmd/clown/main.go` — the `flags.naked` branch in `run()`
  dispatches to `execProcess(cliPath, flags.forwarded)` and returns
  immediately, skipping every downstream wrapping function.
- Sibling escape hatch: `--disable-clown-protocol` (or the env var
  `CLOWN_DISABLE_CLOWN_PROTOCOL=1`) bypasses only the plugin-host pipeline
  while keeping prompt injection, safety defaults, and the post-exit
  resume hint. Prefer it when only the plugin pipeline is the problem.
