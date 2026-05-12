# Tent — `--tent-pass-devshell` (interim PATH passthrough)

**Date:** 2026-05-12
**Related FDR:** [`docs/features/0007-tent.md`](../features/0007-tent.md), 2026-05-12 update
**Subsumed by:** the `--profile` system (~weeks out — see
[`2026-04-23-profiles-design.md`](./2026-04-23-profiles-design.md))

## Goal

Add an opt-in flag that makes the caller's nix-devshell tools reachable
on `PATH` inside `clown --tent`. Concretely: rewrite the host `$PATH` to
only its `/nix/store/<hash>-<name>/bin` entries and set that as the
container's `PATH`. No new bind mounts; every forwarded entry already
resolves through the existing `/nix/store` ro mount.

This is interim. The `--profile` design owns the durable shape; the
flag name (`--tent-pass-devshell`) telegraphs that.

## Non-goals

- `PKG_CONFIG_PATH`, `XDG_DATA_DIRS`, and other path-list devshell vars.
  Same filter mechanism, deferred to profiles.
- Non-`/nix/store` passthrough (e.g. `~/.local/bin`). Would require
  new bind-mount choreography; belongs in the profile schema.
- Per-binary allowlisting. The store mount already exposes the whole
  devshell — filtering at `PATH` grain wouldn't contain anything.
- Auto-detection from `IN_NIX_SHELL`. Explicit opt-in keeps the
  surface honest and short-lived.

## Design

### Behavioral contract

- **Off by default.** Container `PATH` keeps current behavior.
- **`--tent-pass-devshell` (or `CLOWN_TENT_PASS_DEVSHELL=1`)** filters
  host `$PATH` and sets container `PATH` to the result via
  `--env PATH=<filtered>` (explicit value, not value-by-reference).
- **Replace, not append.** The tent image bakes no baseline `PATH`,
  so there's nothing meaningful to preserve. If/when the image grows
  one, revisit this as "prepend filtered devshell entries to the
  image baseline."
- **Filter rule.** Keep entries matching `/nix/store/<hash>-<name>/bin`
  (and `/nix/store/<hash>-<name>/sbin` for completeness); drop
  everything else. Preserve order and duplicates — `PATH` semantics
  treat earlier-wins, so the filter is order-preserving.
- **Empty result.** If filtering yields zero entries (caller wasn't in
  a devshell), emit a stderr warning and proceed without setting
  `PathOverride` — agent gets the un-tent-pass-devshell experience.
  Don't fail; the flag is best-effort.
- **Without `--tent`.** `--tent-pass-devshell` without `--tent` is a
  hard error: "requires --tent."

### Code surface

Three files touched, one new file added.

**New:** `internal/tent/path.go`

```go
package tent

import "strings"

// FilterPathToNixStore returns the entries of hostPath whose prefix
// matches /nix/store/<hash>-<name>/bin (or /sbin), joined by ":" in
// original order. Used by --tent-pass-devshell to derive the container's
// PATH from the host's PATH without dragging in entries that won't
// resolve inside the tent's filesystem namespace.
func FilterPathToNixStore(hostPath string) string {
    if hostPath == "" {
        return ""
    }
    var kept []string
    for _, entry := range strings.Split(hostPath, ":") {
        if isNixStoreBinDir(entry) {
            kept = append(kept, entry)
        }
    }
    return strings.Join(kept, ":")
}

func isNixStoreBinDir(p string) bool {
    if !strings.HasPrefix(p, "/nix/store/") {
        return false
    }
    return strings.HasSuffix(p, "/bin") || strings.HasSuffix(p, "/sbin")
}
```

**Modified:** `internal/tent/tent.go`

Add `PathOverride string` to `Options`. In `BuildArgs`, after the
existing `EnvPassthrough` loop, emit `--env PATH=<value>` when
`opts.PathOverride != ""`. The existing `EnvPassthrough` list already
excludes `PATH`, so there's no double-emit risk.

```go
// (added to Options)
// PathOverride, when non-empty, sets the container's PATH explicitly
// via --env PATH=<value>. Use this when the caller wants a curated
// PATH (e.g. derived from the host devshell via FilterPathToNixStore)
// rather than the image's default.
PathOverride string
```

```go
// (added to BuildArgs, after the EnvPassthrough loop, before the image)
if opts.PathOverride != "" {
    args = append(args, "--env", "PATH="+opts.PathOverride)
}
```

**Modified:** `cmd/clown/main.go`

1. Add `passDevshell bool` to `parsedFlags`.
2. Parse `--tent-pass-devshell` and `CLOWN_TENT_PASS_DEVSHELL=1` in
   `parseFlags`. Note: env var honored only if it equals `"1"`,
   matching `CLOWN_TENT` convention.
3. In `runWithFlags`, after the existing `--tent`/`--naked`
   exclusivity guard, add:

   ```go
   if flags.passDevshell && !flags.tent {
       fmt.Fprintln(os.Stderr, "clown: --tent-pass-devshell requires --tent")
       return 1
   }
   ```

4. Plumb `passDevshell` into `newTentExecutor` as a parameter. Inside,
   after the existing `options_from_env` phase:

   ```go
   if passDevshell {
       filtered := tent.FilterPathToNixStore(os.Getenv("PATH"))
       if filtered == "" {
           fmt.Fprintln(os.Stderr,
               "clown: --tent-pass-devshell: no /nix/store entries in $PATH; "+
               "devshell forwarding skipped (are you inside a nix develop / direnv shell?)")
       } else {
           opts.PathOverride = filtered
           if logger != nil {
               logger.Info("tent path override",
                   "entries", strings.Count(filtered, ":")+1)
           }
       }
   }
   ```

5. Update `runClaude`'s call site to pass `flags.passDevshell` through.
6. Update the `--help` block to document the flag, marking it as
   "(interim; will be subsumed by --profile)".

### Tests

**New:** `internal/tent/path_test.go` — table-driven `TestFilterPathToNixStore`
covering: empty input; single store entry; mixed store + non-store
(verify order); all non-store → empty; duplicates preserved; trailing
colon (empty entry); store path that isn't a bin dir (drop); `/sbin`
variant kept.

**Modified:** `internal/tent/tent_test.go` — add `TestBuildArgs_PathOverride`
asserting `--env PATH=<value>` appears in argv when `opts.PathOverride`
is set, and `TestBuildArgs_NoPathOverride` asserting it doesn't appear
otherwise.

**Modified:** `cmd/clown/main_test.go` (or wherever `parseFlags` is
tested) — add cases for `--tent-pass-devshell`, the env-var form, and
the "requires --tent" error. If no `parseFlags` test file exists yet
(check before assuming), add one with just these cases — don't write
exhaustive coverage for the existing flags.

### Manual smoke

After `just build`:

```sh
# Linux native — from inside a project's direnv-loaded shell:
result/bin/clown --tent --tent-pass-devshell -- --version
# Inside the agent, `which git` / `which just` should resolve to
# /nix/store/.../bin entries.

# Negative case — explicit empty PATH:
PATH=/usr/bin result/bin/clown --tent --tent-pass-devshell -- --version
# Expect: stderr warning "no /nix/store entries in $PATH"; no override applied.

# Error case:
result/bin/clown --tent-pass-devshell -- --version
# Expect: "clown: --tent-pass-devshell requires --tent"; exit 1.
```

Darwin smoke is the same once a podman-machine session is running
(per [`2026-05-11-tent-darwin-v0-handoff.md`](./2026-05-11-tent-darwin-v0-handoff.md)).
podman-machine bind-mounts `/nix/store` into the VM, so filtered
entries resolve inside the container the same way they do on linux
native — no darwin-specific code path is needed.

## Implementation order

1. `internal/tent/path.go` + `path_test.go`.
2. `tent.Options.PathOverride` field + `BuildArgs` emit + test.
3. `parsedFlags` + `parseFlags` + `--tent-pass-devshell` parse + tests.
4. Exclusivity guard in `runWithFlags`.
5. `newTentExecutor` parameter + override wiring + warning path.
6. `--help` text update.
7. Manual smoke on linux.

Each step is independently shippable; (1)-(2) land the mechanism,
(3)-(6) expose it. If the FDR amendment lands first (which it has),
nothing downstream depends on ordering between these steps and the
profile system.

## Follow-ups (file as issues, not in this plan)

- File the tracking issue referenced by the FDR amendment's
  "Tracking: TBD issue."
- Profile system integration: when a profile declares
  `env.pass_devshell = true`, the profile runner sets the same
  `Options.PathOverride`. The interim flag can then be removed (or
  redirected to a one-shot profile).
- Optional: extend `FilterPathToNixStore` into a generic
  `FilterPathListToNixStore` that other devshell vars
  (`PKG_CONFIG_PATH`, `XDG_DATA_DIRS`) can reuse when profiles
  decide to pass them through.
