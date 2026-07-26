# OpenRouter Dynamic Model Picker — Design

Date: 2026-07-26
Status: approved (brainstorm with Sasha, session lucid-fir; validated against a
live interactive `bubbles/list` demo with fixture data before write-up)

## Problem

Issue #195: the `openrouter`/`openrouter-opencode` profile templates
(`cmd/clown/profileform.go`) pre-fill `Model` as a static string
(`openai/gpt-4o`) the user edits by hand, having to look up valid OpenRouter
slugs at https://openrouter.ai/models themselves. OpenRouter's `/models` API
already has this data — the fast-follow confirmed during the Phase A/B
brainstorms (`docs/plans/2026-07-24-openrouter-non-anthropic-design.md`) is to
fetch it and offer a searchable picker instead, with the static field kept as
an escape hatch.

## Decision summary

1. **Auto-fetch, silent fallback** — no confirm prompt before the network
   call. If a token resolves, fetch happens automatically before the picker
   would show; any failure (no token, network error, non-2xx, empty/malformed
   response) falls back to today's plain `Model` text input with no error
   dialog interrupting the flow.
2. **bubbles/list, not huh.Select** — `huh.Option` is a single-line `Key`
   string (`vendor/github.com/charmbracelet/huh/option.go:6-10`), so it can't
   natively show a two-line title+description row. `cmd/clown/cheapcontext_picker.go`
   already establishes the pattern of dropping to a bare `bubbles/list` program
   when huh can't do what's needed; this picker reuses that escape hatch
   rather than cramming id+context+pricing+description onto one huh.Select
   line.
3. **Two-line rows via `list.DefaultDelegate`** (not a custom delegate like
   cheapcontext_picker.go's — no cross-row cascade behavior is needed here, a
   flat single-select list): Title = `id` + context length + pricing
   (`openai/gpt-4o    128k ctx · $2.50/$10 per M tok`), Description = a
   generated short blurb.
4. **Detail pane, not expand-on-focus** — `ItemDelegate.Height()` takes no
   per-item argument (`vendor/.../bubbles/list/list.go:38-53`); it's one fixed
   height applied to every row for the list's pagination math, so a row can't
   grow taller only when focused without wasting that height on every other
   row. Instead: a bordered pane to the right of the list (`lipgloss.JoinHorizontal`)
   shows the *full*, untruncated description of whatever item
   `list.Model.SelectedItem()` currently is, re-rendered on every cursor move.
5. **Short-description generation** — the API has no dedicated short-description
   field (confirmed against a live fetch of `https://openrouter.ai/api/v1/models`,
   345 models: schema is `id`, `canonical_slug`, `name`, `description`,
   `context_length`, `pricing`, `architecture`, `top_provider`,
   `supported_parameters`, etc. — `name` is a short *title*, not a description;
   `description` is prose, 190–260+ chars in the observed sample, containing
   markdown links/emphasis and HTML-escaped entities). The list row's blurb is
   derived from `description`: strip `[text](url)` to `text`, strip `*`/`_`
   emphasis markers, unescape `&lt;`/`&gt;`/`&amp;`, collapse whitespace, then
   cap at 72 chars on a word boundary with `…`.
6. **Basic markdown-ish rendering in the detail pane** — rather than stripping
   markdown entirely for the full-description view, render the two constructs
   OpenRouter's descriptions actually use: `[text](url)` → underlined text +
   dim `(url)`, and `*single*`/`**double**` asterisk emphasis → bold (the real
   data uses both forms interchangeably; distinguishing bold vs. italic isn't
   worth the added regex complexity for a description blurb). No new
   dependency — this is a small regex-based pass using `lipgloss` styles
   already in the module, not `glamour` (confirmed not currently a dependency;
   pulling in a full markdown renderer for a one-blurb feature would be
   over-scoped for what's being rendered here).
7. **No caching** — `clown profile add`/`edit` is an infrequent, interactive,
   one-shot action; a fresh ~1–2s fetch every time the picker opens is a
   non-issue and avoids cache-invalidation logic entirely.
8. **Escape hatch preserved** — the picker's chosen id just pre-fills `v.Model`
   before `buildProfileForm` runs; the Model field stays a plain, editable
   `huh.Input` afterward, so a manual edit (or a slug the picker doesn't have,
   e.g. a brand-new model) always still works.

## Section 1 — Trigger & flow

In `profileAddInteractive`/`profileEditInteractive` (`cmd/clown/profileform.go`),
after the template is picked (add) or the existing profile is loaded (edit),
but before `buildProfileForm` runs:

1. Detect an OpenRouter-shaped profile: `Provider == "openrouter"` OR
   (`Provider == "opencode"` AND `URL == "https://openrouter.ai/api/v1"`).
2. Resolve the token via the existing `clownfile.ResolveEnv(v.Token)` helper
   (handles both a literal token and a `${OPENROUTER_API_KEY}` reference —
   already used for this exact purpose in `resolveOpencodeGateway`,
   `cmd/clown/opencode.go:291-296`).
3. If a non-empty token resolves, call `fetchOpenRouterModels(ctx, token)`
   (Section 2) with a 5s timeout.
4. On success, run the picker (Section 3), pre-scrolled to the profile's
   current `Model` if it matches a fetched id. The chosen id overwrites
   `v.Model`; cancelling (`esc`/`ctrl+c`) leaves `v.Model` untouched.
5. On any failure or absent token, skip straight to `buildProfileForm` with
   `v.Model` unchanged (today's behavior, no behavior change for
   non-OpenRouter profiles or OpenRouter without a key set).

## Section 2 — Fetch & data model

New file `cmd/clown/openroutermodels.go`:

```go
type openRouterModel struct {
    ID          string
    ContextLen  int
    PromptPrice float64 // USD per token
    CompPrice   float64 // USD per token
    Description string  // raw, as returned by the API
}

func fetchOpenRouterModels(ctx context.Context, token string) ([]openRouterModel, error)
```

- Mirrors the existing HTTP pattern from `internal/jugglermodels/download.go`:
  `http.NewRequestWithContext`, `&http.Client{Timeout: 5 * time.Second}`,
  status-code check, `fmt.Errorf("...: %w")` wrapping throughout.
- Decodes `{data: [{id, context_length, pricing: {prompt, completion, ...},
  description, ...}], total_count, links}` — confirmed live against the real
  endpoint; `pricing.prompt`/`.completion` arrive as decimal USD-per-token
  strings, parsed with `strconv.ParseFloat`. Unknown/extra pricing keys
  (`web_search`, `image`, `input_cache_read`, etc.) and other fields
  (`architecture`, `top_provider`, `supported_parameters`, ...) are ignored —
  only the fields the picker renders are decoded.
- Short-description generation (Decision 5) and markdown-ish rendering
  (Decision 6) are formatting concerns, not fetch concerns — they live in the
  picker file, not here.

## Section 3 — Picker

New file `cmd/clown/openroutermodelpicker.go`, adapted from the validated
`cmd/clown-openrouter-picker-demo` prototype:

- `list.New` with `list.NewDefaultDelegate()` (two-line rows, no custom
  delegate needed — single-select, no cascade behavior).
- Layout: list on the left (`3/5` of window width), a bordered
  (`lipgloss.RoundedBorder()`) detail pane on the right showing the
  markdown-ish-rendered full description of the currently-selected item,
  updated on every `tea.WindowSizeMsg`/cursor move. The gap between them is a
  style margin (`MarginLeft(1)` on the detail box), not string concatenation —
  concatenating a literal space only offsets the pane's first line, breaking
  the border alignment on every other line (caught and fixed during the demo).
- `enter` confirms the highlighted item and quits; `esc`/`ctrl+c` cancels and
  quits; the caller (`profileAddInteractive`/`profileEditInteractive`)
  distinguishes the two via a returned `ok bool`, same shape as
  `runChecklistPicker`'s existing return convention.

## Rollback

Purely additive — the static `Model` text input is unchanged and always
present after the picker step, whether or not the picker ever runs. If the
picker misbehaves, unset `OPENROUTER_API_KEY` (or let the fetch fail) and the
flow falls back to exactly today's behavior. No existing profile or config
format changes.

## Tuning levers

- **72-char short-description cap** — arbitrary, chosen to fit the list row
  next to the id/context/pricing title without wrapping. Change signal: real
  usage shows it truncating mid-idea too often, or the id/pricing column
  width changes and leaves more/less room.
- **5s fetch timeout** — a guess at "long enough for a slow connection,
  short enough not to stall the add/edit flow." Change signal: reports of the
  fetch timing out on a normal connection, or of the flow hanging
  noticeably before falling back.
- **No caching** — revisit if `clown profile add/edit` usage patterns change
  (e.g. scripted/repeated invocations) such that a repeated ~1-2s fetch
  becomes an actual annoyance rather than a one-shot cost.

## References

- Issue #195: https://code.linenisgreat.com/clown/issues/195
- `docs/plans/2026-07-24-openrouter-non-anthropic-design.md` (Phase A/B
  ancestor: static model field, confirmed dynamic listing as fast-follow)
- `docs/plans/2026-07-06-openrouter-profiles-design.md` (Phase A ancestor:
  claude+gateway profile and TUI)
- OpenRouter models API (schema confirmed live 2026-07-26):
  `https://openrouter.ai/api/v1/models`
- `cmd/clown/cheapcontext_picker.go` — precedent for the bubbles/list
  escape hatch when huh can't render what's needed
