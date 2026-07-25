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

// TestOpenrouterOpencodeUpgradeHint guards smith#196's migration-note scope
// item: a Phase A profile (provider=opencode, backend=gateway, url pointed
// at OpenRouter) gets flagged as upgradable to the Phase B openrouter
// provider; unrelated opencode+gateway profiles and non-OpenRouter urls must
// not be flagged.
func TestOpenrouterOpencodeUpgradeHint(t *testing.T) {
	user := []profile.Profile{
		{Name: "opencode-openrouter", Provider: "opencode", Backend: "gateway", URL: "https://openrouter.ai/api/v1"},
		{Name: "opencode-other-gateway", Provider: "opencode", Backend: "gateway", URL: "https://example.com/v1"},
		{Name: "already-openrouter", Provider: "openrouter", Backend: "gateway"},
	}
	hint := openrouterOpencodeUpgradeHint(nil, user)
	if !strings.Contains(hint, "opencode-openrouter") {
		t.Errorf("hint missing the upgradable profile name: %q", hint)
	}
	if strings.Contains(hint, "opencode-other-gateway") {
		t.Errorf("hint must not flag a non-OpenRouter opencode+gateway profile: %q", hint)
	}
	if strings.Contains(hint, "already-openrouter") {
		t.Errorf("hint must not flag a profile already on provider=openrouter: %q", hint)
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
