# OpenRouter via Named Profiles + Profile TUI — Design

Date: 2026-07-06
Status: approved (brainstorm with Sasha, session live-catalpa)

## Problem

clown cannot launch the claude provider against openrouter.ai (or any
remote Anthropic-compatible gateway) as a first-class configuration.
OpenRouter exposes an Anthropic-compatible endpoint ("Anthropic Skin")
at `https://openrouter.ai/api` — Claude Code speaks its native
`/v1/messages` protocol to it directly, requiring only:

- `ANTHROPIC_BASE_URL=https://openrouter.ai/api` (note: `/api`, not `/api/v1`)
- `ANTHROPIC_AUTH_TOKEN=<openrouter key>`
- `ANTHROPIC_API_KEY=""` — present but **explicitly empty**, or claude
  falls back to Anthropic auth
- optional `ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL` /
  `CLAUDE_CODE_SUBAGENT_MODEL` for routing to non-Anthropic models

Today the only routes are hand-rolled env (clownfile `[profile.env]`,
smoke recipes) or the opencode/crush OpenAI-compat gateway TOMLs. The
named-profile registry (`internal/profile`, `--profile`, the startup
picker) has a `gateway` backend for opencode/crush but **not** for
claude, and a claude profile currently applies nothing beyond the
provider name (`selectedProfile` is only threaded to
`runOpencode`/`runCrush`).

## Decision summary

Approach A from the brainstorm: extend the existing profile system.
OpenRouter is a new **backend for the claude provider**, not a new
provider. The TUI manages named registry entries (registry-first), with
an optional clownfile pin.

Decisions made with Sasha:

1. **TUI target**: the named-profile registry (user `profiles.toml`),
   plus a new clownfile `[profile] profile = "<name>"` pin key.
2. **Entry point**: a `clown profile` subcommand family (list/add/edit/
   remove) AND a `+ add profile…` hook in the existing startup picker.
3. **Secrets**: token stored plaintext (0600) or as a `${VAR}` env
   reference resolved at launch (`clownfile.ResolveEnv`).
4. **Config-dir drift**: fixed now — canonical
   `$XDG_CONFIG_HOME/clown/`, legacy `~/.config/juggler/` read-fallback
   with a warning; applies to `profiles.toml`, `opencode.toml`,
   `crush.toml`.

## Section 1 — schema, gateway semantics, launch plumbing

**Schema.** `internal/profile.Profile` gains `Env map[string]string`
(`toml:"env"`). `url`/`token` already exist. `Validate` adds `gateway`
to claude's backend set with the same url+token-required rule
opencode/crush gateway has. `url` and `token` pass through
`clownfile.ResolveEnv` (`${VAR}` expansion) at resolution time.

**Resolution step.** A new `applyNamedProfile(&flags, selectedProfile)`
runs right after profile validation in `run` (both the `--profile` and
picker paths), applying the profile uniformly to all providers:

- `model` → prepend `--model` to forwarded args unless the user passed
  one (same guard as `applyClownfileProfile`).
- `backend` → NOT applied to `flags.backend`: that field is the tent
  container backend (podman|lima), a different axis from the profile
  registry's API backend (anthropic|gateway|local). The profile
  `Backend` only selects the claude gateway-env behavior below, or is
  consumed inside `runOpencode`/`runCrush` (as today).
- claude + `gateway` → set **authoritatively** (plain `os.Setenv`,
  ambient does NOT win): `ANTHROPIC_BASE_URL=<url>`,
  `ANTHROPIC_AUTH_TOKEN=<resolved token>`, `ANTHROPIC_API_KEY=""`.
  Authoritative because a named profile is an explicit user selection;
  this deliberately differs from clownfile `[profile.env]`, which stays
  only-if-unset as an ambient defaults layer.
- `env` map → only-if-unset, same as clownfile env. Escape hatch for
  `ANTHROPIC_DEFAULT_*_MODEL` routing; hand-edited, not in the TUI v1.

Process-env inheritance (not per-child threading) is correct here: the
whole claude subtree, subagents included, needs the same base URL —
unlike the deliberately-unshared session key (clown#136).

**Rides for free.** The clown#160 attach re-exec already carries
`--profile <name>` into the inner clown, so gateway env is re-derived
inside the mux. `--naked` works: env is set before the exec-replace.

**No new builtin profile.** Builtins stay secrets-free; OpenRouter
arrives as a TUI template writing a user profile with
`token = "${OPENROUTER_API_KEY}"` by default. An env-ref resolving
empty fails fast naming the variable, not as an opaque 401.

## Section 2 — the TUI

**Subcommands** (new `cmd/clown/profilecmd.go`):

- `clown profile list` — one line per profile: name, display,
  provider/backend, source (`builtin` / `user` / `user override`). No
  TTY needed.
- `clown profile add` — huh flow: first a template select (*OpenRouter*,
  *Custom gateway*, *Anthropic*, *Local*), then pre-filled fields:
  name, display, provider (select), backend (select constrained to
  `validCombos[provider]`), model, and — gateway only — url and token
  (`EchoModePassword`, help "literal or `${VAR}` reference"). The
  OpenRouter template pre-fills `url = "https://openrouter.ai/api"`,
  `token = "${OPENROUTER_API_KEY}"`, and leaves model empty (empty ⇒
  claude's own defaults, mapped by OpenRouter's skin). Final confirm
  shows the destination path (mirrors `promptOpencodeLocalConfig`).
- `clown profile edit <name>` — same form, prefilled. Editing a builtin
  creates a user override (merge-by-name gives override semantics).
- `clown profile remove <name>` — confirm + rewrite; removing an
  override restores the builtin.

**Write mechanics.** Load user file → mutate `[]Profile` → re-encode
(BurntSushi) → atomic temp+rename, 0600 file / 0700 dir. Hand-written
comments are clobbered: the file is declaredly TUI-managed. Non-TTY
add/edit refuses with a hint; no scripting flags in v1.

**Picker hook.** `pickProfile` gains one synthetic trailing
`+ add profile…` item. Selecting it exits the list, runs the add form,
reloads profiles, and continues the launch with the just-created
profile. Cancel in the form behaves like quitting the picker.

## Section 3 — pin, migration, errors, testing

**Pin.** clownfile `[profile]` gains `profile = "<name>"`, merged like
the other scalars (deeper file wins). Precedence: `--profile` flag >
clownfile pin > `buildcfg.DefaultProfile` > interactive picker. A pin
resolves through the identical lookup/Validate path as `--profile`
(unknown name = same hard error listing available profiles) and
suppresses the picker.

**Config-dir migration.** New `userProfilesPath()`: canonical
`$XDG_CONFIG_HOME/clown/profiles.toml` (XDG-aware, like
`clownfile.XDGPath`; the current hardcoded `~/.config` goes away); if
absent, fall back to legacy `~/.config/juggler/profiles.toml` with a
one-line stderr warning. The TUI always writes canonical; a shadowed
legacy file is called out as deletable. `opencode.toml`/`crush.toml`
get the same read-canonical-fallback-legacy treatment.

**Errors.** Gateway profile whose `${VAR}` token resolves empty →
launch fails naming the variable. The cached-OAuth conflict (claude
warns when an Anthropic login coexists with `ANTHROPIC_AUTH_TOKEN`)
cannot be fixed from env — the OpenRouter template shows a one-line
note about `/logout`.

**Testing.** Unit: `Validate` combos, `applyNamedProfile` (args + env
via `t.Setenv`), env-ref resolution, legacy-path fallback, pin
precedence, writer TOML round-trip. huh validation funcs stay plain
functions (unit-testable); the interactive flow itself is not
e2e-tested (same posture as the opencode/crush prompts). Bats:
`profile.bats` — `list`, unknown `edit` target, non-TTY `add` refusal.
Justfile: `smoke-clown-against-openrouter` (needs
`OPENROUTER_API_KEY`; drives `clown --profile claude-openrouter`
one-shot), mirroring `smoke-clown-against-tailnet`.

## Rollback

Purely additive; no existing path is replaced. Rollback = don't select
the profile (delete the pin line or pick another profile). The
anthropic/local backends are untouched. The legacy `~/.config/juggler/`
read-fallback keeps old files working during the path migration.

## Tuning levers

- **Gateway env authoritative** (current: plain Setenv beats ambient).
  Signal: a real need for one-off ambient overrides; remedy is an
  explicit escape flag, not flipping the default.
- **OpenRouter template leaves model empty** (current: empty ⇒ claude
  defaults, skin-mapped). Signal: OpenRouter slug conventions make an
  explicit default less surprising.
- **Post-add launch behavior** (current: continue launch with the new
  profile). Signal: users report wanting to land back in the picker.
- **Legacy `~/.config/juggler/` fallback** (current: read with
  warning). Removal criterion: one release with the warning, no
  reports.

## References

- OpenRouter Claude Code integration:
  https://openrouter.ai/docs/cookbook/coding-agents/claude-code-integration
- `docs/plans/2026-04-23-profiles-design.md` (original profile design;
  this extends its matrix with claude+gateway)
- RFC-0013 §1 (clownfile), clown#136 (env hygiene), clown#160
  (profile re-exec propagation), clown#149 (XDG clownfile)
