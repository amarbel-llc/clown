package main

import (
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/profile"
)

func TestFormatProfileList(t *testing.T) {
	builtin := []profile.Profile{{Name: "claude-anthropic", Display: "Claude (Anthropic)", Provider: "claude", Backend: "anthropic"}}
	user := []profile.Profile{
		{Name: "claude-anthropic", Display: "Mine", Provider: "claude", Backend: "anthropic"}, // override
		{Name: "claude-openrouter", Display: "Claude (OpenRouter)", Provider: "claude", Backend: "gateway"},
	}
	out := formatProfileList(builtin, user)
	for _, want := range []string{"user override", "claude-openrouter", "user", "claude / gateway"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestFormatProfileList_ContextColumn(t *testing.T) {
	user := []profile.Profile{
		{Name: "plain", Provider: "claude", Backend: "anthropic"},
		{
			Name: "trimmed", Provider: "claude", Backend: "anthropic",
			ContextServers: []string{"moxy/moxy", "bob/caldav"},
		},
	}
	out := formatProfileList(nil, user)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	var plainLine, trimmedLine string
	for _, l := range lines {
		if strings.HasPrefix(l, "plain\t") || strings.Contains(l, "plain ") {
			plainLine = l
		}
		if strings.HasPrefix(l, "trimmed\t") || strings.Contains(l, "trimmed ") {
			trimmedLine = l
		}
	}
	if !strings.Contains(plainLine, "-") {
		t.Errorf("profile with no saved selection should show '-' in CONTEXT column: %q", plainLine)
	}
	if !strings.Contains(trimmedLine, "2 server(s)") {
		t.Errorf("profile with a saved selection should show its server count: %q", trimmedLine)
	}
}
