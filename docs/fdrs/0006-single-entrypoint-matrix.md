---
status: exploring
date: 2026-05-08
---

# Single-Entrypoint Matrix — Harness × Provider × Model × API

## Abstract

This record sketches the design space for treating clown as a single
entrypoint to the Cartesian product of {harness, model provider, model,
API endpoint}. Status is **exploring** — questions below are
intentionally open and will be answered in future revisions before
this becomes implementable.

The current shape is partially there: the `profile` package already
encodes a 2-axis selector (`provider` × `backend`) and the picker UI
surfaces it, but the axes are conflated, validation is hand-rolled,
and dispatch is harness-specific.

## Mental model

Picking how clown talks to a model means picking a *profile*, which
bundles four conceptually independent things:

1. **Harness** — the local CLI agent clown shells out to: `claude`,
   `codex`, `circus`, `opencode`, `clownbox`. (`internal/buildcfg`,
   `cmd/clown/main.go:resolveProvider`.)
2. **Provider** — the model vendor: Anthropic, OpenAI, a local
   llama.cpp/Ollama, a corp gateway. Currently encoded as the
   `backend` field with values `anthropic`, `local`, `gateway`.
3. **Model** — a name string (`claude-sonnet-4-6`, `qwen3-coder`, …).
4. **API** — the wire endpoint and auth: `https://api.anthropic.com`,
   `http://localhost:11434/v1`, `https://gateway.example.com/v1`.
   Today this is implied by `backend` plus optional `url`/`token`.

A profile fixes a point in this 4-space. The user-visible vocabulary
("clown is a single entrypoint to the matrix") is straightforward;
the internal structure is not.

## Where today's code matches and where it doesn't

What works:

- `profile.Profile` has the four-ish fields and `profile.Validate`
  enforces a small allowlist: `claude × {anthropic, local}` and
  `opencode × {anthropic, gateway, local}`.
- `cmd/clown/profiles/builtin.toml` ships four pre-baked points and
  the bubbletea picker (`pickProfile`) handles selection when no
  flag/env pins one.
- `runWithFlags` dispatches by `flags.provider`, with each harness
  branch wiring up the API differently (claude via env vars,
  opencode via a generated `opencode.json`, circus via portfile
  handshake).

What doesn't:

- **`opencode-anthropic` is declared in the builtin profile list and
  passes `Validate`, but `runOpencode` has no `anthropic` branch.**
  A user picking that profile falls through to the bare path that
  reads `~/.config/circus/opencode.toml` — the path that this branch
  just made interactive via huh. The *profile* says Claude, the
  *runtime* writes whatever the user typed.
- The `backend` axis mixes "where does the model live" (anthropic vs
  local) with "how do we reach it" (gateway). They're orthogonal —
  a corp Anthropic-compatible proxy is `provider=anthropic,
  api=gateway`, not a third backend type.
- Each harness invents its own auth surface: claude uses
  `ANTHROPIC_BASE_URL` plus managed-settings, opencode uses
  `XDG_CONFIG_HOME` indirection over a temp `opencode.json`,
  circus uses a portfile handshake. There's no shared "given matrix
  point P, here are the env + config artifacts" function.
- Adding a new harness (e.g. `gemini-cli`, `crush`, `goose-cli` from
  numtide/llm-agents.nix) means writing a fresh `runFoo` and a new
  entry in `validCombos`. The matrix is an `O(harness × provider)`
  hand-write, not a lookup.

## Key design axes

1. **Are harness and provider one axis or two?** Today they are
   conflated under `provider` (the field name) but the values
   (`claude`, `codex`, `opencode`) are *harnesses*, and the model
   vendor is `backend`. Renaming clarifies intent but breaks
   existing TOML and env-var contracts.
2. **Generated config vs native config.** opencode's matrix point
   resolves to a JSON config clown writes into a temp dir; claude's
   resolves to env vars + managed settings. Should this unify under
   a single "render config artifacts for matrix point P" abstraction,
   or are the harnesses too different to share one?
3. **Provider as data vs provider as code.** Each (harness, provider)
   pair currently needs a Go branch. Alternative: a declarative
   table saying "for harness=opencode, provider=anthropic, render
   config stanza X" — clown becomes a template engine over the
   matrix.
4. **Auth & secrets.** API keys live in
   `~/.config/circus/opencode.toml` (clown), `~/.local/share/opencode/auth.json`
   (opencode's `/connect` OAuth), `ANTHROPIC_API_KEY` (env),
   pivy-agent (ssh/gpg). Does clown promise a single secrets-loading
   path, or just document where each harness expects them?
5. **Discovery vs explicit list.** Builtin profiles enumerate four
   points by hand. Should clown enumerate the matrix from a
   structured source (one row per (harness, provider) capability)
   and let the user pick model + endpoint, or stick with
   hand-curated profiles?

## Provisional recommendation

Split today's `provider` field into two: `harness` (claude, codex,
opencode, …) and `provider` (anthropic, openai, local, gateway-foo).
Add an explicit `api` block carrying `{url, auth_method, env_keys}`
so the wire endpoint stops being implied by `backend`. Keep profiles
as hand-curated TOML; defer the "matrix as data table"
generalization until a third harness needs Anthropic support.

Tradeoff: forces a one-time migration of `builtin.toml` and any
user `profiles.toml`, in exchange for making "add Claude support to
opencode" a config edit instead of a Go branch.

## Most load-bearing clarifier — what's driving this?

- **Plug a real gap** (opencode-anthropic doesn't actually work
  today)?
- **Reduce per-harness onboarding cost** (each new CLI agent from
  numtide/llm-agents.nix needs a fresh `runFoo`)?
- **User-facing clarity** (the picker shows "OpenCode (Anthropic)"
  and the user expects that to mean Claude-via-opencode)?
- **All three?**

Each motivation pushes the design differently. (1) is fixable inside
`runOpencode` without renaming anything. (2) calls for the
declarative-table generalization. (3) calls for the harness/provider
split and explicit API block.

## Open questions for a future revision

- Answer the load-bearing clarifier above.
- Pin each of the five design axes.
- Decide whether `opencode × anthropic` ships as a one-off
  `runOpencode` branch (mirroring the `gateway` branch but rendering
  the native `provider.anthropic` stanza per
  https://opencode.ai/docs/providers/#anthropic) or waits for the
  matrix refactor.
- Resolve interaction with existing mechanisms: profile validation,
  the bubbletea picker's display string, `CLOWN_PROVIDER` /
  `CLOWN_PROFILE` env vars, the build-time `DefaultProvider` /
  `DefaultProfile` ldflags, the `--naked` opt-out path.
- Decide whether this stays a single FDR or splits — e.g. one for
  the harness/provider rename, one for the declarative matrix
  table, one for the auth-and-secrets story.
