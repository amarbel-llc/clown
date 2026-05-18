# Clown `--backend=circus` Auto-Lifecycle — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** Add a top-level `--backend` flag to clown, auto-start a circus instance for `--backend=circus`, and prompt on exit to stop it. Drop `*-local` profile variants. Implements FDR-0011.

**Architecture:** Builds on FDR-0010 / plan 1 (ringmaster control plane). Clown uses `internal/ringmaster.Client` directly — it does NOT shell out to the `circus` CLI. The `--backend` flag joins `--provider`, `--profile`, and `--model` in `parsedFlags` and is resolved into an "effective backend" with CLI > env > profile > provider-default precedence. For `effective_backend == "circus"`, clown asks ringmaster for the requested alias; if missing, it prompts (interactive) or fails (non-interactive). On clown exit, if clown was the one that started the instance, it prompts to stop it.

**Tech Stack:** Reuses `internal/ringmaster.Client`, `github.com/charmbracelet/huh` for prompts (already a dependency).

**Rollback:** Revert merge commit. Pre-redesign profile system is in git history.

**Prerequisite:** Plan 1 (ringmaster + circus client) must be merged. This plan assumes `internal/ringmaster.Client` exists and ringmaster is running.

**Out of scope:**
- Reference-counted ownership across multiple clown sessions (flagged as future revisit in the interview).
- Tailnet-exposed control plane.
- Strict-proxy mode.

---

## Outline (each section becomes 2–5 TDD tasks at implementation time)

### Stage A: Flag plumbing

- **Add `--backend` to `parsedFlags`** with env override (`CLOWN_BACKEND`) and CLI parsing in `parseFlags`. Tests in `cmd/clown/main_test.go` for parse cases.
- **Compute effective backend.** New helper `effectiveBackend(flags, profile) string` with documented precedence. Unit-tested across the matrix: CLI > env > profile > provider default. Provider defaults: claude → `anthropic`; opencode → `gateway`; crush → `anthropic`.
- **Validate combination.** Mirror `profile.Validate` for `(provider, backend)` pairs — reject `claude + gateway`, etc.
- **Drop `*-local` profiles** from `cmd/clown/profiles/builtin.toml`. Update `profile.Validate` to drop the now-unused `local` backend keyword in favor of `circus`. Update interface in `profile.Profile` accordingly.

### Stage B: Gateway sourcing (`--url`, `--token`, file fallback)

- **Add `--url` / `--token` CLI flags** and `CLOWN_URL` / `CLOWN_TOKEN` env vars.
- **Implement precedence helper** `effectiveGatewayCreds(flags, profile, fileLoader) (url, token string, err error)`: CLI > env > profile > `~/.config/circus/<provider>.toml`. Per-field precedence (URL and Token can come from different sources).
- **Refactor opencode/crush gateway paths** in `cmd/clown/opencode.go` and `cmd/clown/crush.go` to use the new helper.

### Stage C: Circus backend connectivity (no UI yet)

- **Add `--circus-alias` CLI flag** (defaults to the provider profile's `model` field, or to `--model` if provided, or to a fixed default).
- **Add a `ringmaster.Client` getter** that reads `RINGMASTER_SOCKET` / default, dials, returns a usable client. Surface a friendly error if ringmaster is unreachable.
- **Refactor opencode local path** (`cmd/clown/opencode.go`): replace `readCircusPortfile` with `cli.GetInstance` returning `Instance.Bind:Port`. Same for crush.
- **Refactor `runCircus` into `runClaude`** gated on `effective_backend == "circus"`. The clown-protocol handshake from circus goes away — clown asks ringmaster for the port and sets `ANTHROPIC_BASE_URL` directly.
- **Add deprecation shim for `--provider=circus`**: in `parseFlags`, if `provider == "circus"`, set `provider=claude`, `backend=circus`, and print a one-line deprecation note to stderr.

### Stage D: Auto-start prompt (interactive only)

- **New helper `ensureCircusInstance(ctx, cli, alias, model, interactive) (Instance, startedByUs bool, err error)`** in a new `internal/circuslifecycle` package.
  - Calls `cli.GetInstance(alias)`.
  - If exists: probe `/slots` and inspect model. If model differs from `model` argument, prompt for restart (interactive) or warn + continue (non-interactive).
  - If missing: prompt to start (interactive) — pick model via huh from `cli.ListAvailableModels` — then call `cli.StartInstance`. Non-interactive: fail with diagnostic.
- **Tests** with a stub `ringmaster.Client` interface (extract one from the concrete client).

### Stage E: Auto-stop prompt (interactive only)

- **At end of `runClaude`/`runOpencode`/`runCrush`** when `effective_backend == "circus"` and `startedByUs == true`:
  - Probe `/slots`. If busy, default-no on the stop prompt and warn. If idle, default-yes.
  - Prompt with huh. On confirm, `cli.StopInstance(alias)`. Non-interactive: leave running.

### Stage F: Documentation

- **Update `man clown`** to describe `--backend`, `--circus-alias`, the deletion of `*-local` profiles, and the deprecation of `--provider=circus`.
- **Update `CLAUDE.md`** in repo root to describe the new flow (currently describes `*-local` profiles in passing).
- **Update `docs/features/0008-local-model-provider.md`** with a status note: superseded by FDR-0011 and the ringmaster work.

### Stage G: End-to-end test

- **bats test** that brings up ringmaster, starts a fake llama-server via `circus`, then runs `clown --provider=opencode --backend=circus --circus-alias=fake` (in `--naked` mode against a fake opencode that just echoes its OPENCODE_CONFIG and exits) and asserts the right URL was wired in.

### Stage H: Final pass

- Build, run all tests, run bats.
- Squash-merge via spinclass.

---

## Notes for the implementing agent

- **Do NOT shell out to the `circus` CLI from clown.** Use `internal/ringmaster.Client` directly. Two clients = two RPC paths = double maintenance.
- **Profile interpretation has changed slightly.** Today's `profile.Backend == "local"` is interpreted as "circus" in the new world. Either rename the field's semantics (`local` → `circus`) or drop the field entirely on profiles that no longer pin a backend.
- **`--provider=circus` deprecation:** prints once per invocation to stderr. Behavior must be unchanged for one full release. After the release, the deprecation note becomes an error.
- **Mismatch detection comparison:** the friendly model is `Instance.Model`, the `/v1/models` response shows `Instance.Alias`. Compare `Instance.Model` to the requested model when deciding whether to prompt for restart. Aliases drift from models intentionally (e.g., `coder-32k` aliased to `qwen3-coder` with a 32k ctx); the user's mental model is "which model is loaded," not "which alias is registered."
- **`startedByUs` is in-process state.** A clown crash leaks the instance forever. This is acknowledged in FDR-0011's limitations and flagged for a follow-up plan.
- **The interview that produced this plan is captured in chat history**, not in a separate design doc. If decisions need revisiting, recover them from FDR-0011's Examples and Limitations sections, which were written from that interview.
