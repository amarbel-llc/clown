---
status: exploring
date: 2026-05-18
promotion-criteria: `clown --provider=<claude|opencode|crush> --backend=circus` works end-to-end against ringmaster; user can launch a session, opencode/crush/claude exchanges complete inferences, and on exit a prompt offers to stop the instance; --provider=circus alias still works for one full release for muscle-memory users
---

# Clown `--backend=circus` Lifecycle

> **Status note (2026-05-19):** FDR-0010 (ringmaster control plane)
> has shipped — see master between commits c4e84db..1751998. The
> dependency chain this FDR called out is fully satisfied: ringmaster
> daemon, in-memory registry, JSON-RPC over UDS, `rm.Client` SDK,
> full launcher with reap-on-exit, home-manager module, manpages,
> bats e2e, and real-binary CI lane. The `runCircus` path in
> `cmd/clown/main.go` is stubbed pending this FDR's implementation
> — see [Phase-1 outcomes that affect phase 2](#phase-1-outcomes-that-affect-phase-2)
> at the bottom of this page for the surface a phase-2 agent should
> consume.

## Problem Statement

Today clown has no first-class concept of "which transport am I using?". Backend selection is tangled into the profile system via `*-local` profile variants whose only difference from their `-anthropic` peers is the `backend` field. Worse:

- `--provider=claude` ignores `selectedProfile` entirely, so `clown --profile=claude-local` silently behaves identically to `clown --provider=claude` against Anthropic.
- `--provider=circus` is a parallel top-level dispatch that hand-rolls circus start, the handshake, and ANTHROPIC_BASE_URL wiring.
- Local-backend code paths for opencode and crush expect the user to have started circus manually; if not running, clown exits with an unfriendly error.
- Once a session is over, circus keeps running silently. The user has no automation around stopping it.

Users want to launch any provider against any backend without manual circus juggling, and they want clown to stop the instance when they're done if they're the one who started it.

This FDR depends on FDR-0010 (ringmaster control plane). Without ringmaster's RPC, clown can't reliably introspect "what's running?" or coordinate multi-instance state.

## Interface

Three changes:

**1. New `--backend` flag.** `clown --backend <anthropic|circus|gateway>` selects the transport. Precedence for choosing the effective backend:

```
CLI --backend  >  env CLOWN_BACKEND  >  profile.backend  >  provider default
```

**2. Profile cleanup.** Drop `claude-local`, `opencode-local`, `crush-local` from `cmd/clown/profiles/builtin.toml`. Keep the `-anthropic` variants. Users get the local equivalent via `--backend=circus`.

**3. `--provider=circus` becomes a deprecation alias** that maps to `--provider=claude --backend=circus`. One-release grace period with a stderr deprecation warning; then removed.

The auto-lifecycle behavior, gated on `effective_backend == "circus"`:

- **On launch, no matching instance running:**
  - Interactive: confirm "Start circus instance?", run model picker (`--model` or profile.model preselected; huh over installed GGUFs from ringmaster's `ListAvailableModels`), then call ringmaster `StartInstance`. Wait for the instance to report healthy via `/health`.
  - Non-interactive: fail with `clown: circus instance not running; start it: circus start <model>`.

- **On launch, an instance is running but the requested model differs:**
  - Probe ringmaster `GetInstance` then llama-server `/slots` to count in-flight requests.
  - Interactive: confirm "circus serves X (N in flight); restart with Y?" with safe defaults (default "keep" if slots busy, "restart" if idle).
  - Non-interactive: warn and continue using the running instance.

- **On launch, portfile-stale equivalent (instance entry exists in registry but llama-server is unreachable):**
  - Treat as not running; offer to start fresh (interactive) or fail (non-interactive). Ringmaster will reap dead children, so this case should be rare.

- **On exit, only if clown started the instance:**
  - Probe `/slots`. If busy, default "keep running" and warn. If idle, default "stop instance".
  - Interactive prompt; non-interactive leaves it running.

Ownership tracking is **in-process only**. If clown crashes or is killed before exit, the instance survives. Reference-counted ownership (state-dir markers, refcounting) is flagged as future work but not in scope.

## Examples

```sh
# Anthropic backend (current default for *-anthropic profiles)
$ clown --provider=opencode --backend=anthropic
# uses ANTHROPIC_API_KEY directly

# Gateway backend (URL/Token from CLI, env, profile, or ~/.config/circus/<provider>.toml)
$ clown --provider=opencode --backend=gateway --url=https://gw/v1 --token=...

# Circus backend, instance already running
$ clown --provider=opencode --backend=circus --circus-alias=qwen3-coder
# clown asks ringmaster for the port, points opencode at it

# Circus backend, no instance running
$ clown --provider=opencode --backend=circus --model qwen3-coder
? Start a circus instance with qwen3-coder? [Y/n]: y
ringmaster: started qwen3-coder on 127.0.0.1:43219
# opencode launches normally
# [user finishes the session, exits opencode]
? Stop the qwen3-coder instance you started? [Y/n]: y
ringmaster: stopped qwen3-coder

# Circus backend, mismatch
$ circus start gemma-3-270m
$ clown --provider=opencode --backend=circus --model qwen3-coder
? circus is serving gemma-3-270m (0 in flight); restart with qwen3-coder? [Y/n]: y
ringmaster: stopped gemma-3-270m
ringmaster: started qwen3-coder on 127.0.0.1:43219
```

## Limitations

**In-process ownership tracking is lossy.** A clown crash leaks the instance (kept running forever). Acceptable for v1; a follow-on plan can introduce refcounted client markers in `~/.local/state/circus/clients/`.

**No automatic alias selection.** When multiple instances run, `--backend=circus` without `--circus-alias` and without `--model` is ambiguous. Behaviors: (a) refuse and list aliases; (b) pick the only instance if exactly one exists; (c) prompt to choose interactively. The plan should pick one explicitly — for v1, single-instance behavior is most common, so prefer (a) when ambiguous, (b) when unambiguous.

**`/v1/models` returns the alias, not the model.** llama-server sees the `--alias` value as the model id. Clown's mismatch detection therefore compares aliases-to-aliases or models-to-aliases as configured; care needed in the implementation plan to be unambiguous about which field is compared.

**`--backend=gateway` precedence is multi-source.** CLI > env > profile > `~/.config/circus/<provider>.toml`. Each source can supply URL, Token, or both, and the precedence applies per-field, not per-source. This is documented in the plan but worth flagging here.

**Profile deletion breaks muscle memory.** Users who alias `clown --profile=opencode-local` need to update their aliases. We accept this; the `*-local` profiles were partially broken (claude-local in particular never worked) and the new flag is clearer.

**Concurrent clown sessions and ownership.** Two clown sessions can both call `StartInstance` for the same alias. Second call gets "already running" from ringmaster and both proceed. The second clown's ownership flag stays false (since it didn't start the instance), so it won't prompt on exit. The first clown's flag stays true and it will prompt. This is correct.

## More Information

- Depends on FDR-0010 (Ringmaster Control Plane) for the `StartInstance`/`StopInstance`/`GetInstance`/`ListInstances`/`ListAvailableModels` RPCs.
- Profile schema is defined in `internal/profile/profile.go`. The `Backend` field continues to exist but becomes one input among several to the effective-backend computation.
- The existing `runCircus` path in `cmd/clown/main.go` folds into `runClaude` gated on `effective_backend == "circus"`. The clown-protocol handshake from circus to clown goes away; ringmaster replaces it. (Phase 1 already stubbed `runCircus` and deleted `readCircusHandshake` — see "Phase-1 outcomes" below.)
- The opencode and crush local-backend branches (`cmd/clown/opencode.go`, `cmd/clown/crush.go`) replace `readCircusPortfile` calls with ringmaster RPC calls.

## Phase-1 outcomes that affect phase 2

Phase 1 shipped between commits c4e84db..1751998 (2026-05-18 to
2026-05-19). The following are the load-bearing details for phase 2:

### Public surfaces a phase-2 agent should consume

- **`internal/ringmaster` package** is the only API phase 2 should
  touch for control-plane work. Key entry points:
  - `rm.NewClient(socket string) (*Client, error)` — dial UDS.
  - `rm.SocketPath() (string, error)` — resolves `$RINGMASTER_SOCKET`
    or `$XDG_STATE_HOME/circus/control.sock` (default
    `~/.local/state/circus/control.sock`).
  - `cli.StartInstance(ctx, rm.StartInstanceParams{...})` —
    blocks until `/health` is healthy or the 60 s launcher timeout
    elapses. Returns `rm.StartInstanceResult{Instance: rm.Instance{
    Alias, Model, Bind, Port, PID, StartedAt}}`.
  - `cli.StopInstance(ctx, rm.StopInstanceParams{Alias})` — SIGTERM
    with 5 s grace, then SIGKILL. Returns nil on success or alias-
    not-running error.
  - `cli.GetInstance(ctx, rm.GetInstanceParams{Alias})` — returns
    `rm.GetInstanceResult{Instance}` or RPC error code `-32001`
    when alias is not registered. Use this for the "is the
    requested alias running?" probe.
  - `cli.ListInstances(ctx)` — full registry snapshot. Use this
    when the user passes neither `--circus-alias` nor `--model`
    and you want to enumerate options.
  - `cli.ListAvailableModels(ctx)` — scans the models dir; phase 2's
    "pick a model" prompt should source from this, not a separate
    filesystem scan.
- **`rm.Client` is goroutine-safe.** Each RPC takes a context with
  deadline; the client propagates it to `conn.SetDeadline` and
  validates response IDs match the request. Multiple goroutines
  can share one client instance.
- **Error code conventions** (already used by `circus` CLI):
  - `-32601` method not found
  - `-32602` invalid params
  - `-32603` internal/marshal error
  - `-32000` general application error (launcher failure, etc.)
  - `-32001` alias not found (only `GetInstance` returns this)

  Phase 2 should special-case `-32001` to distinguish "no such
  alias, offer to start one" from "ringmaster is broken."

### Stub to replace

- `cmd/clown/main.go` has a `runCircus(circusPath, flags, prompts,
  pluginDirs) int` stub starting at the function declaration. It
  currently prints an FDR-0011 pointer to stderr and returns 1.
  The dispatch at `case "circus":` in the provider switch calls
  it (still using `--provider=circus`). Phase 2 needs to either
  delete this stub and fold its callers into `runClaude` (per the
  FDR), or rewrite it as a real implementation calling ringmaster.
- The `readCircusHandshake` function and the `io` import that
  supported it were already deleted in phase 1. The
  `cmd/clown/circus.go pickCircusModel` helper that consumed
  `circusmodels.List()` directly **also still exists** and may
  need to switch to `cli.ListAvailableModels`. Verify the call
  graph before deciding.

### Phase-1 design choices to preserve in phase 2

- **`Instance.Model == ""` means "no model loaded" (router mode).**
  Real `llama-server` accepts launching without `--model` — the
  launcher already skips that flag when `Model` is empty. Phase 2
  shouldn't reject an empty model in `StartInstance` calls if it
  intentionally wants router mode (rare for the user-facing CLI,
  but possible for diagnostic flows).
- **`circusmodels.Dir()` resolves the models directory.** It honors
  `$XDG_DATA_HOME` and defaults to `~/.local/share/circus/models`.
  Phase 2's "pick a model" prompt should NOT re-implement this —
  go through `ListAvailableModels`.
- **Bind defaults to `127.0.0.1` in the launcher.** Phase 2's
  `ANTHROPIC_BASE_URL` wiring should consume `Instance.Bind` and
  `Instance.Port` from the `Start`/`Get` result, not assume
  loopback — though loopback is currently the only path.
- **Stale-socket cleanup is daemon-side.** `ringmaster daemon`
  removes any stale socket file before bind. Phase 2 doesn't need
  to coordinate this.
- **`circus stopall` exists in the RPC surface (`MethodStopAll`)
  but is not currently bound to a `circus` subcommand.** Phase 2's
  exit-prompt path may want to use `StopAll` for the multi-instance
  cleanup case — design TBD in the implementation plan.

### Phase-1 design choices NOT to redo

- **Don't add a portfile/pidfile back.** The whole point of phase 1
  was eliminating those side channels. If phase 2 needs another
  process to find an instance, it goes through `rm.Client`.
- **Don't shell out to `circus` from `clown`.** Use `rm.Client`
  directly. The plan-2 doc already flags this but it bears repeating.
- **Don't re-add the clown→circus stdout handshake.** The
  `readCircusHandshake` function and the `1|1|tcp|...|streamable-http`
  format are gone from `cmd/clown`. Ringmaster is the only
  introspection path.

### Test pyramid (see `ringmaster-testing(7)`)

The full pyramid is documented in `man 7 ringmaster-testing`. The
short version for phase 2:

- New unit tests for `effectiveBackend` etc. go in
  `cmd/clown/main_test.go` (layer 1).
- New integration tests for the lifecycle helper
  (`internal/circuslifecycle`) should mock `rm.Client` via an
  interface. Don't bring up a real daemon for these.
- The end-to-end test in Stage G of plan 2 should live in
  `zz-tests_bats/`, alongside `ringmaster.bats`. Stage it the same
  way: a Go fake llama-server + the real ringmaster, with `clown`
  pointed at a temp socket via `RINGMASTER_SOCKET`. Reuse the
  bats lane's `RINGMASTER_BIN` / `CIRCUS_BIN` / `FAKE_LLAMA_SERVER_BIN`
  env vars — they're already wired in `bats.nix`.

### Sundry footguns the phase-1 work uncovered

- **macOS sun_path 104-byte limit.** Any test using `t.TempDir()`
  for a UDS path will fail on macOS — paths exceed 104 bytes. Use
  the `shortTempSocket`/`shortTempDir` helpers in
  `cmd/ringmaster/server_test.go` or `cmd/circus/dial.go`'s
  pattern.
- **The pre-merge hook (`just`) IS the CI lane.** Don't run
  `just test` redundantly before `merge-this-session` — the hook
  runs the full suite. Cheap per-package `hamster.go-build` is
  fine for compile-checks.
- **`circus` no longer reads `CIRCUS_PORT` / `CIRCUS_MODEL`.** Those
  env vars were tied to the old single-instance daemon and have no
  meaning in the ringmaster world. Phase 2 should not reintroduce
  them.

### Issues to close on phase-2 merge

- **#58** — pidfile/portfile replacement is fully done in phase 1.
  Add `Closes #58` to the phase-2 merge commit so it auto-closes,
  or close it manually if phase 2 is a long sequence.
