package main

import (
	"strings"
	"testing"

	"github.com/amarbel-llc/clown/internal/profile"
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
