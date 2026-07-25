# OpenRouter Non-Anthropic Models — Design

Date: 2026-07-24
Status: approved (brainstorm with Sasha, session lucid-fir)

## Problem

The existing OpenRouter profile support (`claude-openrouter`, template key
`"claude-openrouter"`) routes Claude Code through OpenRouter's
Anthropic-compatible skin — it only reaches Anthropic/Claude-family models.
There is no path to
run non-Anthropic models (GPT-4o, Gemini, Llama, etc.) via OpenRouter through
any clown-managed provider.

opencode already knows how to consume OpenAI-compatible endpoints: it
synthesizes a `@ai-sdk/openai-compatible` provider config from a profile's
URL/Token/Model. OpenRouter exposes an OpenAI-compatible endpoint at
`https://openrouter.ai/api/v1`. The missing piece is a profile template that
pre-fills these fields so users don't have to discover the format themselves.

## Decision summary

**Phase A (this doc):** Add a single `"openrouter-opencode"` profile template
for `provider=opencode, backend=gateway`, pointing at OpenRouter's
OpenAI-compatible endpoint. Uses the existing profile schema and opencode
config synthesizer unchanged.

**Phase B (future):** Add `"openrouter"` as a first-class provider in
`validCombos`, with its URL hardcoded rather than stored per-profile, and
dedicated dispatch logic. Note: the template key `"openrouter"` is already
claimed by the existing `claude+gateway` template (the Anthropic-skin route).
Phase B must either rename that template's key (e.g. to `"claude-openrouter"`)
or repurpose it — it is not a free slot.

Decisions made with Sasha:

1. **Phase A only** for this implementation — template addition, no schema
   changes, no new provider axis.
2. **Static model field** — user types the OpenRouter slug manually. A
   dynamic `/models` API fetch is a confirmed fast-follow.
3. **Pre-filled default** — template pre-fills `openai/gpt-4o` as the model
   slug. User edits it to any OpenRouter slug.
4. **Phase B migration** — `"openrouter-opencode"` template key leaves
   `"openrouter"` unclaimed; when B lands, existing profiles either keep
   working via a compat shim or migrate through `clown profile edit`.

## Section 1 — Profile template

Add one entry to `profileTemplates` in `cmd/clown/profileform.go`:

```go
{
    key: "openrouter-opencode", display: "OpenRouter (opencode)", p: profile.Profile{
        Name: "opencode-openrouter", Display: "OpenRouter (opencode)",
        Provider: "opencode", Backend: "gateway",
        URL:   "https://openrouter.ai/api/v1",
        Token: "${OPENROUTER_API_KEY}",
        Model: "openai/gpt-4o",
    },
    note: "Model is an OpenRouter slug (e.g. openai/gpt-4o, google/gemini-2.0-flash-001). Browse at https://openrouter.ai/models",
}
```

No changes to `internal/profile`, `validCombos`, or the opencode config
synthesizer schema. The existing form already enforces URL + Token required for
`opencode+gateway`. The model field is pre-filled by the template and editable.

## Section 2 — Slash-in-slug verification

`writeOpencodeConfigFile` (opencode.go) emits `"model": "custom/" + model`,
so `openai/gpt-4o` produces `"model": "custom/openai/gpt-4o"`. This is
correct only if opencode's config parser splits on the **first** `/` (yielding
provider=`custom`, model=`openai/gpt-4o`).

Implementation must verify this with a smoke test (`clown --profile
<openrouter-opencode-profile>` against a real key, or by reading opencode's
model-resolution source). If opencode splits on all slashes, the fix is to
normalize the map key in `writeOpencodeConfigFile` — replace `/` with `-` in
the key only, keep the full slug in the `Name` field — without touching the
`"model"` string.

## Section 3 — Phase B migration path (implemented, smith#196)

- Added `"openrouter": {"gateway": true}` to `validCombos` in `profile.go`,
  with an `openrouter` branch in `Validate` requiring only `Token` (no `URL`).
- Profiles with `provider=openrouter` dispatch to the opencode runner
  (`cmd/clown/main.go`'s dispatch switch and `resolveProvider`) with `URL`
  hardcoded to `https://openrouter.ai/api/v1` in `runOpencode`
  (`cmd/clown/opencode.go`) rather than read from the profile — the profile
  stores only `Token` and `Model`.
- Existing `provider=opencode, backend=gateway, url=https://openrouter.ai/api/v1`
  profiles continue to work as-is (opencode+gateway path is unchanged);
  `"openrouter-opencode"` remains as its template.
- The pre-existing `"openrouter"` template key (`claude+gateway`, Anthropic
  skin) was renamed to `"claude-openrouter"` in `cmd/clown/profileform.go`,
  and a new `"openrouter"` template (`provider=openrouter`) was added in its
  place — avoiding the `templateByKey` first-match collision this doc
  originally flagged.

## Rollback

Purely additive — don't use the template. Profiles already created can be
removed with `clown profile remove <name>`. No existing path is altered.

## Tuning levers

- **`context=128000, output=16384`** hardcoded in `writeOpencodeConfigFile`.
  Wrong for some OpenRouter models. Change signal: user reports limit errors
  on a model with a smaller context window, or wants to use a model's extended
  output. Remedy: add optional `ContextWindow`/`OutputLimit` fields to profile,
  or have opencode query actual model limits from OpenRouter.
- **Pre-filled default model `openai/gpt-4o`** — may age poorly as the OpenRouter
  roster evolves. Change signal: the model is deprecated or repriced and a
  newer model becomes the obvious default.

## References

- `docs/plans/2026-07-06-openrouter-profiles-design.md` (Phase A ancestor:
  claude+gateway profile and TUI)
- OpenRouter OpenAI-compatible API: https://openrouter.ai/docs/api-reference
- OpenRouter model list: https://openrouter.ai/models
