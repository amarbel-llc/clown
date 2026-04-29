package profile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amarbel-llc/clown/internal/profile"
)

func TestLoad_ParsesProfiles(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "profiles.toml")
	if err := os.WriteFile(f, []byte(`
[[profile]]
name     = "local-qwen"
display  = "Local (Qwen3-Coder)"
provider = "claude"
backend  = "local"
model    = "qwen3-coder"

[[profile]]
name     = "gw-gpt4o"
display  = "Gateway GPT-4o"
provider = "opencode"
backend  = "gateway"
model    = "gpt-4o"
url      = "https://example.com/v1"
token    = "tok"
`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	profiles, err := profile.Load(f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("want 2 profiles, got %d", len(profiles))
	}
	p := profiles[0]
	if p.Name != "local-qwen" || p.Provider != "claude" || p.Backend != "local" || p.Model != "qwen3-coder" {
		t.Errorf("unexpected profile[0]: %+v", p)
	}
	p2 := profiles[1]
	if p2.URL != "https://example.com/v1" || p2.Token != "tok" {
		t.Errorf("unexpected profile[1]: %+v", p2)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := profile.Load("/nonexistent/profiles.toml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestMerge_AdditionalOverridesBuiltin(t *testing.T) {
	builtin := []profile.Profile{
		{Name: "foo", Display: "Foo", Provider: "claude", Backend: "anthropic", Model: "claude-sonnet-4-6"},
	}
	additional := []profile.Profile{
		{Name: "foo", Display: "Foo Override", Provider: "claude", Backend: "anthropic", Model: "claude-opus-4-6"},
	}
	merged := profile.Merge(builtin, additional)
	if len(merged) != 1 {
		t.Fatalf("want 1 profile, got %d", len(merged))
	}
	if merged[0].Model != "claude-opus-4-6" {
		t.Errorf("override did not apply: %+v", merged[0])
	}
}

func TestMerge_AdditionalAdds(t *testing.T) {
	builtin := []profile.Profile{
		{Name: "foo", Provider: "claude", Backend: "anthropic"},
	}
	additional := []profile.Profile{
		{Name: "bar", Provider: "opencode", Backend: "local"},
	}
	merged := profile.Merge(builtin, additional)
	if len(merged) != 2 {
		t.Fatalf("want 2 profiles, got %d", len(merged))
	}
}

func TestValidate_ValidCombos(t *testing.T) {
	cases := []profile.Profile{
		{Name: "a", Provider: "claude", Backend: "anthropic"},
		{Name: "b", Provider: "claude", Backend: "local"},
		{Name: "c", Provider: "opencode", Backend: "anthropic"},
		{Name: "d", Provider: "opencode", Backend: "gateway", URL: "http://x", Token: "t"},
		{Name: "e", Provider: "opencode", Backend: "local"},
	}
	for _, p := range cases {
		if err := profile.Validate(p); err != nil {
			t.Errorf("Validate(%q): unexpected error: %v", p.Name, err)
		}
	}
}

func TestValidate_InvalidCombos(t *testing.T) {
	t.Run("claude+gateway invalid backend", func(t *testing.T) {
		p := profile.Profile{Name: "bad1", Provider: "claude", Backend: "gateway"}
		err := profile.Validate(p)
		if err == nil {
			t.Fatal("Validate(bad1): expected error, got nil")
		}
		if !strings.Contains(err.Error(), "gateway") {
			t.Errorf("Validate(bad1): error %q does not mention gateway", err.Error())
		}
	})

	t.Run("opencode+gateway missing url/token", func(t *testing.T) {
		p := profile.Profile{Name: "bad2", Provider: "opencode", Backend: "gateway"}
		err := profile.Validate(p)
		if err == nil {
			t.Fatal("Validate(bad2): expected error, got nil")
		}
		if !strings.Contains(err.Error(), "url") && !strings.Contains(err.Error(), "token") {
			t.Errorf("Validate(bad2): error %q does not mention url or token", err.Error())
		}
	})
}
