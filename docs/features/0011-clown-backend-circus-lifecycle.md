---
status: exploring
date: 2026-05-18
promotion-criteria: `clown --provider=<claude|opencode|crush> --backend=circus` works end-to-end against ringmaster; user can launch a session, opencode/crush/claude exchanges complete inferences, and on exit a prompt offers to stop the instance; --provider=circus alias still works for one full release for muscle-memory users
---

# Clown `--backend=circus` Lifecycle

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
- The existing `runCircus` path (`cmd/clown/main.go:940`) folds into `runClaude` gated on `effective_backend == "circus"`. The clown-protocol handshake from circus to clown goes away; ringmaster replaces it.
- The opencode and crush local-backend branches (`cmd/clown/opencode.go`, `cmd/clown/crush.go`) replace `readCircusPortfile` calls with ringmaster RPC calls.
