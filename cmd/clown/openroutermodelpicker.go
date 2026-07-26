package main

import (
	"fmt"
	"html"
	"math"
	"regexp"
	"strings"

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
