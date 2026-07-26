package main

import (
	"fmt"
	"html"
	"math"
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---- context/pricing labels ----

// formatContextLen renders a model's context_length as a compact label.
// Real API values are exact byte-power sizes (1048576, 262144, ...), not
// round thousands/millions, so both branches round rather than truncate —
// 1048576 -> "1M ctx" not "1.048576M ctx", 1999 -> "2k ctx" not "1k ctx".
func formatContextLen(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%dM ctx", int(math.Round(float64(n)/1_000_000)))
	case n >= 1_000:
		return fmt.Sprintf("%dk ctx", int(math.Round(float64(n)/1_000)))
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
	reLink     = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reEmphasis = regexp.MustCompile(`\*{1,2}([^*]+)\*{1,2}`)
	wsCollapse = regexp.MustCompile(`\s+`)

	markdownishBold = lipgloss.NewStyle().Bold(true)
	markdownishLink = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("39"))
	markdownishDim  = lipgloss.NewStyle().Faint(true)
)

// walkMarkdownish drives both renderMarkdownish and cleanDescription: same
// two-pass traversal (links, then emphasis — an emphasis marker can't split
// a link's brackets/parens, and neither styled Render nor a bare capture
// contains a literal asterisk, so the passes can't cross-contaminate), with
// the caller supplying what happens to each captured span. Kept as one
// shared walk rather than two independent copies so a future regex change
// (e.g. adding `_underscore_` emphasis) only has to happen once.
func walkMarkdownish(s string, link func(text, url string) string, emphasis func(text string) string) string {
	s = reLink.ReplaceAllStringFunc(s, func(match string) string {
		g := reLink.FindStringSubmatch(match)
		return link(g[1], g[2])
	})
	return reEmphasis.ReplaceAllStringFunc(s, func(match string) string {
		g := reEmphasis.FindStringSubmatch(match)
		return emphasis(g[1])
	})
}

// renderMarkdownish applies basic terminal styling for the detail pane's
// full-description view: links become underlined text plus a dim "(url)",
// emphasis becomes bold. Anything else passes through as plain text.
func renderMarkdownish(s string) string {
	s = walkMarkdownish(
		s,
		func(text, url string) string {
			return markdownishLink.Render(text) + markdownishDim.Render(" ("+url+")")
		},
		func(text string) string { return markdownishBold.Render(text) },
	)
	return html.UnescapeString(s)
}

// cleanDescription strips markdown/entities to PLAIN text (no styling
// codes) — used for the short list-row blurb, which lipgloss wraps/pads by
// character count; embedded ANSI escapes from renderMarkdownish would
// corrupt that math, so this is a separate, styling-free pass rather than
// renderMarkdownish reused with styles disabled.
func cleanDescription(s string) string {
	s = walkMarkdownish(
		s,
		func(text, url string) string { return text },
		func(text string) string { return text },
	)
	s = html.UnescapeString(s)
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

// ---- picker program ----

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
	chosen     string // empty means cancelled — enter is the only path that sets it
	detailBox  lipgloss.Style
	detailWrap lipgloss.Style
}

// selectedModel returns the item currently under the cursor, if any.
func (m openRouterPickerModel) selectedModel() (openRouterModel, bool) {
	it, ok := m.list.SelectedItem().(openRouterModelItem)
	if !ok {
		return openRouterModel{}, false
	}
	return it.model, true
}

func (m openRouterPickerModel) Init() tea.Cmd { return nil }

func (m openRouterPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// enter/esc are gated on list.FilterState() because bubbles/list
		// binds those same keys itself while the filter box is active
		// (list.Filtering: enter applies the filter, esc clears it —
		// list/keys.go) — the picker must let those two-step interactions
		// through rather than treating every enter/esc as "confirm/cancel
		// the whole picker". ctrl+c is NOT gated: it's the unconditional
		// "abandon everything" key regardless of what the list is doing.
		switch msg.String() {
		case "enter":
			if m.list.FilterState() != list.Filtering {
				if mo, ok := m.selectedModel(); ok {
					m.chosen = mo.ID
				}
				return m, tea.Quit
			}
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.list.FilterState() == list.Unfiltered {
				return m, tea.Quit
			}
		}
	case tea.WindowSizeMsg:
		listWidth := msg.Width*3/5 - 1
		detailWidth := msg.Width - listWidth - 3 // border(2) + gap(1); Width() already includes padding
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
	mo, ok := m.selectedModel()
	if !ok {
		return ""
	}
	return m.detailBox.Render(m.detailWrap.Render(renderMarkdownish(mo.Description)))
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
	if final.chosen == "" {
		return "", false, nil
	}
	return final.chosen, true, nil
}
