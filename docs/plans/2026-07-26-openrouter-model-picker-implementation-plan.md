# OpenRouter Dynamic Model Picker Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** Replace the static free-text `Model` field with a live, searchable
OpenRouter model picker in `clown profile add`/`clown profile edit`, falling
back to today's text field whenever no token or the fetch fails (issue #195).

**Architecture:** A pure fetch+parse function (`cmd/clown/openroutermodels.go`)
hits `https://openrouter.ai/api/v1/models`; a bubbles/list TUI
(`cmd/clown/openroutermodelpicker.go`, two-line rows + a right-side
markdown-ish detail pane) lets the user search and pick one; a small glue
function wires both into `profileAddInteractive`/`profileEditInteractive` in
`cmd/clown/profileform.go`, running before `buildProfileForm` and only ever
pre-filling `v.Model` (the text field stays present and editable either way).

**Tech Stack:** Go, `github.com/charmbracelet/bubbles/list`,
`github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/lipgloss`
(all already module dependencies — no new deps).

**Rollback:** Purely additive. Revert the `profileform.go` call sites (or
delete the two new files) to fall back to exactly today's static-text-field
behavior; no config/profile-format changes.

**Design doc:** `docs/plans/2026-07-26-openrouter-model-picker-design.md` —
read it first; every decision below traces back to a section there.
**Prototype:** `cmd/clown-openrouter-picker-demo/main.go` — the validated
layout/regex logic this plan adapts (don't re-derive from scratch; Task 5
deletes it once the real picker lands).

---

### Task 1: Fetch + parse OpenRouter's `/models` API

**Promotion criteria:** N/A (new capability, no old approach).

**Files:**
- Create: `cmd/clown/openroutermodels.go`
- Test: `cmd/clown/openroutermodels_test.go`

**Step 1: Write the failing test**

```go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const fixtureModelsJSON = `{"data":[` +
	`{"id":"openai/gpt-4o","context_length":128000,"description":"OpenAI's flagship multimodal model.","pricing":{"prompt":"0.0000025","completion":"0.00001"}},` +
	`{"id":"anthropic/claude-3.5-sonnet","context_length":200000,"description":"Anthropic's mid-tier model.","pricing":{"prompt":"0.000003","completion":"0.000015"}}` +
	`],"total_count":2,"links":{"next":null}}`

func TestFetchOpenRouterModelsFrom_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization header = %q, want %q", got, "Bearer test-token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixtureModelsJSON))
	}))
	defer srv.Close()

	models, err := fetchOpenRouterModelsFrom(context.Background(), srv.URL, "test-token")
	if err != nil {
		t.Fatalf("fetchOpenRouterModelsFrom: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	got := models[0]
	if got.ID != "openai/gpt-4o" || got.ContextLen != 128000 {
		t.Errorf("models[0] = %+v", got)
	}
	if got.PromptPrice != 0.0000025 || got.CompPrice != 0.00001 {
		t.Errorf("models[0] pricing = %+v", got)
	}
}

func TestFetchOpenRouterModelsFrom_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, err := fetchOpenRouterModelsFrom(context.Background(), srv.URL, "bad-token"); err == nil {
		t.Fatal("expected error on HTTP 401, got nil")
	}
}

func TestFetchOpenRouterModelsFrom_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()

	if _, err := fetchOpenRouterModelsFrom(context.Background(), srv.URL, "token"); err == nil {
		t.Fatal("expected error on malformed JSON, got nil")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test -mod=mod ./cmd/clown/ -run TestFetchOpenRouterModelsFrom -v`
(the `-mod=mod` flag works around the pre-existing, unrelated vendor-drift
issue described in `AGENTS.md`'s "Agent gotchas" section — `clown#174`)
Expected: FAIL (`undefined: fetchOpenRouterModelsFrom` / `openRouterModel`)

**Step 3: Write the implementation**

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// openRouterModelsURL is OpenRouter's model-listing endpoint (issue #195,
// docs/plans/2026-07-26-openrouter-model-picker-design.md Section 2).
const openRouterModelsURL = "https://openrouter.ai/api/v1/models"

// openRouterModel is the subset of OpenRouter's /models response fields the
// picker (openroutermodelpicker.go) actually renders. The real response
// carries many more fields (architecture, top_provider,
// supported_parameters, ...) — deliberately ignored.
type openRouterModel struct {
	ID          string
	ContextLen  int
	PromptPrice float64 // USD per token
	CompPrice   float64 // USD per token
	Description string  // raw, as returned by the API (markdown + entities)
}

type openRouterModelsResponse struct {
	Data []struct {
		ID            string `json:"id"`
		ContextLength int    `json:"context_length"`
		Description   string `json:"description"`
		Pricing       struct {
			Prompt     string `json:"prompt"`
			Completion string `json:"completion"`
		} `json:"pricing"`
	} `json:"data"`
}

// fetchOpenRouterModels fetches the live model list, using token as a
// bearer credential. Mirrors internal/jugglermodels/download.go's HTTP
// pattern (context-aware request, timeout client, status check, wrapped
// errors) with a 5s timeout appropriate for an interactive prompt rather
// than a multi-GB download.
func fetchOpenRouterModels(ctx context.Context, token string) ([]openRouterModel, error) {
	return fetchOpenRouterModelsFrom(ctx, openRouterModelsURL, token)
}

// fetchOpenRouterModelsFrom is fetchOpenRouterModels with the base URL
// overridable, so tests can point it at an httptest.Server instead of the
// real API.
func fetchOpenRouterModelsFrom(ctx context.Context, url, token string) ([]openRouterModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var parsed openRouterModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	out := make([]openRouterModel, 0, len(parsed.Data))
	for _, d := range parsed.Data {
		m := openRouterModel{ID: d.ID, ContextLen: d.ContextLength, Description: d.Description}
		m.PromptPrice, _ = strconv.ParseFloat(d.Pricing.Prompt, 64)
		m.CompPrice, _ = strconv.ParseFloat(d.Pricing.Completion, 64)
		out = append(out, m)
	}
	return out, nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test -mod=mod ./cmd/clown/ -run TestFetchOpenRouterModelsFrom -v`
Expected: PASS (3 tests)

**Step 5: Commit**

```
git add cmd/clown/openroutermodels.go cmd/clown/openroutermodels_test.go
git commit -m "clown: fetch OpenRouter's /models API (#195)"
```

---

### Task 2: Formatting helpers (context/pricing labels, short description, markdown-ish rendering)

**Promotion criteria:** N/A.

**Files:**
- Create: `cmd/clown/openroutermodelpicker.go` (formatting half only this task — the picker program itself is Task 3, same file)
- Test: `cmd/clown/openroutermodelpicker_test.go`

**Step 1: Write the failing test**

```go
package main

import "testing"

func TestFormatContextLen(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{128000, "128k ctx"},
		{1000000, "1M ctx"},
		{1048576, "1M ctx"}, // real API value (2^20) for several models
		{262144, "262k ctx"},
		{500, "500 ctx"},
	}
	for _, c := range cases {
		if got := formatContextLen(c.in); got != c.want {
			t.Errorf("formatContextLen(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatPricing(t *testing.T) {
	cases := []struct {
		prompt, comp float64
		want         string
	}{
		{0.0000025, 0.00001, "$2.50/$10.00 per M tok"}, // openai/gpt-4o, real values
		{0, 0, "free"},
	}
	for _, c := range cases {
		if got := formatPricing(c.prompt, c.comp); got != c.want {
			t.Errorf("formatPricing(%v,%v) = %q, want %q", c.prompt, c.comp, got, c.want)
		}
	}
}

// Test inputs below are verbatim (link/emphasis cases) or lightly trimmed
// (last case, dropped a trailing "..." for a cleaner exact-match assertion)
// from real https://openrouter.ai/api/v1/models descriptions pulled during
// the #195 design review — see docs/plans/2026-07-26-openrouter-model-picker-design.md.
func TestShortDescription(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			"link stripped and truncated at a word boundary",
			"Fast-mode variant of [Opus 5](https://openrouter.ai/anthropic/claude-opus-5) - identical capabilities with higher output speed at 2x pricing relative to regular Opus 5.",
			"Fast-mode variant of Opus 5 - identical capabilities with higher output…",
		},
		{
			"emphasis stripped, short enough to not truncate",
			"*Ling-3.0-flash* is a *124B-parameter model*.",
			"Ling-3.0-flash is a 124B-parameter model.",
		},
		{
			"html-escaped link URL discarded, entities never surface",
			"Laguna S 2.1 is the latest coding agent model from [Poolside](&lt;https://poolside.ai/&gt;).",
			"Laguna S 2.1 is the latest coding agent model from Poolside.",
		},
	}
	for _, c := range cases {
		if got := shortDescription(c.in); got != c.want {
			t.Errorf("%s:\n shortDescription() = %q\n              want = %q", c.name, got, c.want)
		}
	}
}

func TestRenderMarkdownish(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			"bold double-asterisk",
			"**Claude Opus 5** is Anthropic's flagship model.",
			markdownishBold.Render("Claude Opus 5") + " is Anthropic's flagship model.",
		},
		{
			"link becomes underlined text + dim url",
			"See [Poolside](https://poolside.ai/) for details.",
			"See " + markdownishLink.Render("Poolside") + markdownishDim.Render(" (https://poolside.ai/)") + " for details.",
		},
	}
	for _, c := range cases {
		if got := renderMarkdownish(c.in); got != c.want {
			t.Errorf("%s:\n renderMarkdownish() = %q\n                want = %q", c.name, got, c.want)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test -mod=mod ./cmd/clown/ -run 'TestFormatContextLen|TestFormatPricing|TestShortDescription|TestRenderMarkdownish' -v`
Expected: FAIL (undefined functions)

**Step 3: Write the implementation** (create `cmd/clown/openroutermodelpicker.go` with this content; Task 3 appends the picker program to the same file)

```go
package main

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ---- context/pricing labels ----

// formatContextLen renders a model's context_length as a compact label.
// Real API values are exact byte-power sizes (1048576, 262144, ...), not
// round millions, so this rounds rather than truncates — 1048576 -> "1M
// ctx", not "1.048576M ctx".
func formatContextLen(n int) string {
	switch {
	case n >= 500_000:
		return fmt.Sprintf("%dM ctx", int(math.Round(float64(n)/1_000_000)))
	case n >= 1_000:
		return fmt.Sprintf("%dk ctx", n/1000)
	default:
		return fmt.Sprintf("%d ctx", n)
	}
}

// formatPricing renders per-token USD pricing (as decoded by
// fetchOpenRouterModelsFrom) as a per-million-token label, matching how
// OpenRouter's own site displays pricing.
func formatPricing(promptPerToken, compPerToken float64) string {
	if promptPerToken == 0 && compPerToken == 0 {
		return "free"
	}
	return fmt.Sprintf("$%.2f/$%.2f per M tok", promptPerToken*1_000_000, compPerToken*1_000_000)
}

// ---- description cleaning/styling ----
//
// OpenRouter descriptions use exactly two markdown constructs in practice:
// [text](url) links and *single*/**double* asterisk emphasis (treated the
// same — distinguishing bold vs. italic isn't worth the regex complexity
// for a description blurb). Confirmed against a live fetch of all 345
// models during the #195 design review.

var (
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reEmphasis   = regexp.MustCompile(`\*{1,2}([^*]+)\*{1,2}`)
	wsCollapse   = regexp.MustCompile(`\s+`)
	htmlEntities = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")

	markdownishBold = lipgloss.NewStyle().Bold(true)
	markdownishLink = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("39"))
	markdownishDim  = lipgloss.NewStyle().Faint(true)
)

// renderMarkdownish applies basic terminal styling for the detail pane's
// full-description view: links become underlined text plus a dim "(url)",
// emphasis becomes bold. Anything else passes through as plain text. Order
// matters: links first, so an emphasis marker can't split a link's
// brackets/parens; emphasis's inserted ANSI escapes contain no asterisks,
// so it can't double-match its own output.
func renderMarkdownish(s string) string {
	s = reLink.ReplaceAllStringFunc(s, func(match string) string {
		g := reLink.FindStringSubmatch(match)
		return markdownishLink.Render(g[1]) + markdownishDim.Render(" ("+g[2]+")")
	})
	s = reEmphasis.ReplaceAllStringFunc(s, func(match string) string {
		g := reEmphasis.FindStringSubmatch(match)
		return markdownishBold.Render(g[1])
	})
	return htmlEntities.Replace(s)
}

// cleanDescription strips markdown/entities to PLAIN text (no styling
// codes) — used for the short list-row blurb, which lipgloss wraps/pads by
// character count; embedded ANSI escapes from renderMarkdownish would
// corrupt that math, so this is a separate, styling-free pass rather than
// renderMarkdownish reused with styles disabled.
func cleanDescription(s string) string {
	s = reLink.ReplaceAllStringFunc(s, func(match string) string {
		return reLink.FindStringSubmatch(match)[1]
	})
	s = reEmphasis.ReplaceAllStringFunc(s, func(match string) string {
		return reEmphasis.FindStringSubmatch(match)[1]
	})
	s = htmlEntities.Replace(s)
	return strings.TrimSpace(wsCollapse.ReplaceAllString(s, " "))
}

const shortDescriptionCap = 72

// shortDescription caps a cleaned description at shortDescriptionCap chars
// on a word boundary, appending an ellipsis when truncated. Tuning lever
// (docs/plans/2026-07-26-openrouter-model-picker-design.md): revisit the cap
// if real usage shows it truncating mid-idea too often.
func shortDescription(desc string) string {
	s := cleanDescription(desc)
	if len(s) <= shortDescriptionCap {
		return s
	}
	cut := s[:shortDescriptionCap]
	if idx := strings.LastIndexAny(cut, " \t\n"); idx > 0 {
		cut = cut[:idx]
	}
	return cut + "…"
}
```

**Step 4: Run test to verify it passes**

Run: `go test -mod=mod ./cmd/clown/ -run 'TestFormatContextLen|TestFormatPricing|TestShortDescription|TestRenderMarkdownish' -v`
Expected: PASS (4 tests, some table-driven with multiple cases each)

**Step 5: Commit**

```
git add cmd/clown/openroutermodelpicker.go cmd/clown/openroutermodelpicker_test.go
git commit -m "clown: OpenRouter description cleaning/formatting helpers (#195)"
```

---

### Task 3: The picker program (bubbles/list + detail pane)

**Promotion criteria:** N/A.

**Files:**
- Modify: `cmd/clown/openroutermodelpicker.go` (append to the file from Task 2)

No new automated test — this is an interactive `bubbletea` program (same as
`cmd/clown/cheapcontext_picker.go`, which also has no unit test); verified by
the manual smoke test in Task 6. This step is compile-checked only.

**Step 1: Append the picker implementation**

```go
import (
	// add to the existing import block from Task 2:
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// openRouterModelItem adapts openRouterModel to bubbles/list's Item
// interface. Two-line rows via list.DefaultDelegate (not a custom delegate
// like cheapcontext_picker.go's checklistDelegate — this is a flat
// single-select list, no cross-row cascade behavior needed).
type openRouterModelItem struct {
	model openRouterModel
}

func (it openRouterModelItem) Title() string {
	return fmt.Sprintf("%-38s %s · %s", it.model.ID,
		formatContextLen(it.model.ContextLen), formatPricing(it.model.PromptPrice, it.model.CompPrice))
}
func (it openRouterModelItem) Description() string { return shortDescription(it.model.Description) }
func (it openRouterModelItem) FilterValue() string { return it.model.ID }

type openRouterPickerModel struct {
	list       list.Model
	chosen     string
	quit       bool
	detailBox  lipgloss.Style
	detailWrap lipgloss.Style
}

func (m openRouterPickerModel) Init() tea.Cmd { return nil }

func (m openRouterPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if it, ok := m.list.SelectedItem().(openRouterModelItem); ok {
				m.chosen = it.model.ID
			}
			return m, tea.Quit
		case "ctrl+c", "esc":
			m.quit = true
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		listWidth := msg.Width*3/5 - 1
		detailWidth := msg.Width - listWidth - 5 // border(2) + padding(2) + gap(1)
		m.list.SetWidth(listWidth)
		m.list.SetHeight(msg.Height - 2)
		// MarginLeft on the style, not string concatenation, for the gap —
		// concatenating a literal space only offsets the pane's first
		// line, breaking border alignment on every other line (caught
		// during the #195 demo review).
		m.detailBox = m.detailBox.Width(detailWidth).Height(msg.Height - 4).MarginLeft(1)
		m.detailWrap = m.detailWrap.Width(detailWidth - 2) // minus the box's own padding
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// detailPane renders the FULL, untruncated description of whatever item the
// cursor is currently on. This lives outside list.Model's own View/height
// accounting: bubbles/list's ItemDelegate.Height() is one fixed value
// applied to every row for its pagination math (vendor/.../bubbles/list/list.go:38-53),
// so a row can't grow taller only when focused — a separate pane is the only
// way to show more text on focus without wasting that height on every row.
func (m openRouterPickerModel) detailPane() string {
	it, ok := m.list.SelectedItem().(openRouterModelItem)
	if !ok {
		return ""
	}
	return m.detailBox.Render(m.detailWrap.Render(renderMarkdownish(it.model.Description)))
}

func (m openRouterPickerModel) View() string {
	return lipgloss.JoinHorizontal(lipgloss.Top, m.list.View(), m.detailPane())
}

// runOpenRouterModelPicker drives the picker over models, pre-scrolled to
// current if it matches one of models' ids. ok is false if the user
// cancelled (esc/ctrl+c) — same return convention as cheapcontext_picker.go's
// runChecklistPicker; the caller must leave the profile's Model field
// untouched when ok is false.
func runOpenRouterModelPicker(models []openRouterModel, current string) (id string, ok bool, err error) {
	items := make([]list.Item, len(models))
	startIndex := 0
	for i, mo := range models {
		items[i] = openRouterModelItem{model: mo}
		if mo.ID == current {
			startIndex = i
		}
	}
	l := list.New(items, list.NewDefaultDelegate(), 100, 20)
	l.Title = "Select an OpenRouter model (/ to filter, enter to confirm, esc to cancel)"
	l.SetShowStatusBar(false)
	l.Styles.Title = lipgloss.NewStyle().Bold(true)
	l.Select(startIndex)

	m := openRouterPickerModel{
		list:       l,
		detailBox:  lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Width(96),
		detailWrap: lipgloss.NewStyle().Width(92),
	}
	res, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return "", false, err
	}
	final := res.(openRouterPickerModel)
	if final.quit || final.chosen == "" {
		return "", false, nil
	}
	return final.chosen, true, nil
}
```

**Step 2: Compile-check**

Run: `go build -mod=mod ./cmd/clown/...`
Expected: builds with no errors (fixing any import-grouping issues from
merging the Task 3 import block into Task 2's `import (...)` block — Go
requires one `import` block per file, so combine them rather than having
two).

**Step 3: Commit**

```
git add cmd/clown/openroutermodelpicker.go
git commit -m "clown: OpenRouter model picker TUI (#195)"
```

---

### Task 4: Wire the picker into profile add/edit

**Promotion criteria:** N/A — this *is* the new default flow; the static
text field remains, unconditionally, as the fallback (not a separate old
path to phase out).

**Files:**
- Modify: `cmd/clown/profileform.go`

**Step 1: Add the trigger/glue function**

Add near the top of `cmd/clown/profileform.go` (after the existing type/func
declarations, e.g. right after `templateByKey`):

```go
// maybeApplyOpenRouterModelPicker offers the dynamic OpenRouter model picker
// (issue #195) when v looks like an OpenRouter-backed profile. No-op,
// silently, on any failure — a no-op leaves v.Model exactly as the template
// or existing profile set it, and buildProfileForm's plain text Model field
// remains fully functional either way (docs/plans/2026-07-26-openrouter-model-picker-design.md
// Section 1).
func maybeApplyOpenRouterModelPicker(v *profileFormValues) {
	isOpenRouter := v.Provider == "openrouter" ||
		(v.Provider == "opencode" && strings.TrimSpace(v.URL) == openrouterGatewayURL)
	if !isOpenRouter {
		return
	}
	token := clownfile.ResolveEnv(v.Token)
	if token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	models, err := fetchOpenRouterModels(ctx, token)
	if err != nil || len(models) == 0 {
		return
	}
	id, ok, err := runOpenRouterModelPicker(models, v.Model)
	if err != nil || !ok {
		return
	}
	v.Model = id
}
```

**Step 2: Update the import block**

`cmd/clown/profileform.go` currently starts:

```go
import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/huh"

	"code.linenisgreat.com/clown/internal/pluginhost"
	"code.linenisgreat.com/clown/internal/profile"
)
```

Change to:

```go
import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/huh"

	"code.linenisgreat.com/clown/internal/clownfile"
	"code.linenisgreat.com/clown/internal/pluginhost"
	"code.linenisgreat.com/clown/internal/profile"
)
```

**Step 3: Call it from `profileAddInteractive`**

Find (in `profileAddInteractive`):

```go
	v := valuesFromProfile(tpl.p)
	existing := userNameSet(user)
```

Change to:

```go
	v := valuesFromProfile(tpl.p)
	maybeApplyOpenRouterModelPicker(&v)
	existing := userNameSet(user)
```

**Step 4: Call it from `profileEditInteractive`**

Find (in `profileEditInteractive`):

```go
	v := valuesFromProfile(*found)
	if err := buildProfileForm(&v, existing, editOriginal, destPath).Run(); err != nil {
```

Change to:

```go
	v := valuesFromProfile(*found)
	maybeApplyOpenRouterModelPicker(&v)
	if err := buildProfileForm(&v, existing, editOriginal, destPath).Run(); err != nil {
```

**Step 5: Compile-check**

Run: `go build -mod=mod ./cmd/clown/...`
Expected: builds cleanly.

**Step 6: Commit**

```
git add cmd/clown/profileform.go
git commit -m "clown: wire OpenRouter model picker into profile add/edit (#195)"
```

---

### Task 5: Delete the design-review prototype

**Promotion criteria:** The real picker (Tasks 1–4) is in and compiles —
the demo's job (validating the design interactively) is done.

**Files:**
- Delete: `cmd/clown-openrouter-picker-demo/` (whole directory)
- Modify: `justfile` — remove the `debug-openrouter-picker-demo` and
  `debug-openrouter-picker-demo-build` recipes (and their doc comments)
  added next to `debug-cheap-context-moxy`

**Step 1: Delete the demo directory and justfile recipes**

```
rm -r cmd/clown-openrouter-picker-demo
```

Then edit `justfile` to remove the two `[group("debug")]` recipe blocks
(`debug-openrouter-picker-demo` and `debug-openrouter-picker-demo-build`,
including their leading comment blocks) that were inserted between
`debug-cheap-context-moxy` and `debug-stdio-bridge-plugin`.

**Step 2: Compile-check the whole module**

Run: `go build -mod=mod ./...`
Expected: builds cleanly (confirms nothing else referenced the demo package).

**Step 3: Commit**

```
git add -A cmd/clown-openrouter-picker-demo justfile
git commit -m "clown: remove OpenRouter picker design-review prototype (#195)"
```

---

### Task 6: Manual smoke test + final full build

**Promotion criteria:** N/A — this is verification, not a code change.

**Files:** none (verification only)

**Step 1: Run the full unit test suite for the package**

Run: `go test -mod=mod ./cmd/clown/... -v`
Expected: all tests pass, including the new ones from Tasks 1–2.

**Step 2: Manual smoke test — picker path**

With a real `OPENROUTER_API_KEY` exported in the shell:

```
! export OPENROUTER_API_KEY=<your real key>
! ./result/bin/clown profile add
```

(build first via `just build` if `./result` is stale.) Pick the `openrouter`
template, confirm the picker opens automatically, type to filter, move the
cursor and watch the right-hand detail pane update, press `enter`, and
confirm the chosen id lands in the `Model` field of the form that follows.

**Step 3: Manual smoke test — fallback path**

```
! unset OPENROUTER_API_KEY
! ./result/bin/clown profile add
```

Pick the `openrouter` template again; confirm it falls straight through to
the plain `Model` text input (no picker, no error message) — today's
unchanged behavior.

**Step 4: Full build gate**

Do **not** run `just build` here if you're about to hand off to
`spinclass merge-this-session` — its pre-merge hook already runs the full
`just` gate (build + test + bats + analyzers); running it twice wastes
cycles (see this repo's session guidance). If you're validating standalone
outside a merge flow, `just build` is the authoritative check.
