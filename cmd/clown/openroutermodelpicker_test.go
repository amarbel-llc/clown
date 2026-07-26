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
		{524288, "524k ctx"}, // 2^19, same family as 1048576/262144 — must not round up to "1M ctx"
		{1999, "2k ctx"},     // k-branch rounds too, same rule as the M-branch fix above
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
// the #195 design review --- see docs/plans/2026-07-26-openrouter-model-picker-design.md.
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
		{
			"entities outside a link are unescaped via stdlib html.UnescapeString, not just the &lt;/&gt;/&amp; subset",
			"Anthropic&#39;s model excels at &quot;agentic&quot; workflows.",
			`Anthropic's model excels at "agentic" workflows.`,
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
