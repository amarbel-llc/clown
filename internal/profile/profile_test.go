package profile_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"code.linenisgreat.com/clown/internal/profile"
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
		{Name: "f", Provider: "crush", Backend: "anthropic"},
		{Name: "g", Provider: "crush", Backend: "gateway", URL: "http://x", Token: "t"},
		{Name: "h", Provider: "crush", Backend: "local"},
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

	t.Run("crush+gateway missing url/token", func(t *testing.T) {
		p := profile.Profile{Name: "bad3", Provider: "crush", Backend: "gateway"}
		err := profile.Validate(p)
		if err == nil {
			t.Fatal("Validate(bad3): expected error, got nil")
		}
		if !strings.Contains(err.Error(), "url") && !strings.Contains(err.Error(), "token") {
			t.Errorf("Validate(bad3): error %q does not mention url or token", err.Error())
		}
	})

	t.Run("crush+nonsense backend", func(t *testing.T) {
		p := profile.Profile{Name: "bad4", Provider: "crush", Backend: "nope"}
		err := profile.Validate(p)
		if err == nil {
			t.Fatal("Validate(bad4): expected error, got nil")
		}
	})
}

func TestValidateClaudeGateway(t *testing.T) {
	ok := profile.Profile{
		Name: "or", Provider: "claude", Backend: "gateway",
		URL: "https://openrouter.ai/api", Token: "${OPENROUTER_API_KEY}",
	}
	if err := profile.Validate(ok); err != nil {
		t.Fatalf("claude+gateway with url+token should validate: %v", err)
	}
	for _, p := range []profile.Profile{
		{Name: "no-url", Provider: "claude", Backend: "gateway", Token: "x"},
		{Name: "no-token", Provider: "claude", Backend: "gateway", URL: "x"},
	} {
		if err := profile.Validate(p); err == nil {
			t.Errorf("profile %q should fail validation", p.Name)
		}
	}
}

func TestValidateGatewayViaJugglerModel(t *testing.T) {
	// A gateway profile with no inline URL/Token, resolving instead through
	// juggler by Model (applyNamedProfile's juggler-fallback branch) must
	// validate — this is the shape the juggler model registry feature
	// depends on being reachable through --profile / clown profile add.
	p := profile.Profile{Name: "juggler-gw", Provider: "claude", Backend: "gateway", Model: "remote-model-a"}
	if err := profile.Validate(p); err != nil {
		t.Fatalf("gateway+model (no url) should validate: %v", err)
	}
}

func TestValidateGatewayNeitherURLNorModelErrors(t *testing.T) {
	p := profile.Profile{Name: "empty-gw", Provider: "claude", Backend: "gateway"}
	err := profile.Validate(p)
	if err == nil {
		t.Fatal("gateway with neither url nor model should fail validation")
	}
	if !strings.Contains(err.Error(), "url") || !strings.Contains(err.Error(), "model") {
		t.Errorf("error should mention both url and model, got: %v", err)
	}
}

func TestValidateGatewayViaJugglerModelOnlyForClaude(t *testing.T) {
	// opencode/crush have no juggler-resolution fallback (cmd/clown's
	// runOpencode/runCrush read prof.URL/prof.Token directly for the
	// gateway case) — a Model must not exempt them from the pre-existing
	// url+token requirement, or they'd silently launch with an empty base
	// URL/API key.
	for _, prov := range []string{"opencode", "crush"} {
		p := profile.Profile{Name: "juggler-gw-" + prov, Provider: prov, Backend: "gateway", Model: "remote-model-a"}
		err := profile.Validate(p)
		if err == nil {
			t.Errorf("%s+gateway+model (no url/token): expected error, got nil", prov)
			continue
		}
		if !strings.Contains(err.Error(), "url") {
			t.Errorf("%s: error should mention url, got: %v", prov, err)
		}
	}
}

func TestValidateGatewayURLWithoutTokenStillErrors(t *testing.T) {
	// A Model being present must not exempt an inline URL from also
	// requiring a Token — the "URL present" branch is unconditional.
	p := profile.Profile{
		Name: "url-no-token", Provider: "claude", Backend: "gateway",
		URL: "https://example.com", Model: "some-model",
	}
	err := profile.Validate(p)
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("gateway with url but no token should fail validation naming token, got: %v", err)
	}
}

func TestValidate_RejectsEmptyButNonNilContextServers(t *testing.T) {
	// An empty (len==0), non-nil ContextServers can never round-trip through
	// Save/Load: BurntSushi/toml's "context_servers,omitempty" tag drops ANY
	// zero-length slice on write, so it would silently come back as nil on
	// the next Load — indistinguishable from "no saved selection." Validate
	// is the one place every caller constructing/mutating a Profile is
	// expected to pass through, so this rejection protects every caller,
	// not just cmd/clown/cheapcontext.go's promptSaveSelection.
	p := profile.Profile{Name: "empty", Provider: "claude", Backend: "anthropic", ContextServers: []string{}}
	if err := profile.Validate(p); err == nil {
		t.Fatal("expected an error for empty-but-non-nil ContextServers")
	}
}

func TestValidate_NilContextServersIsFine(t *testing.T) {
	p := profile.Profile{Name: "plain", Provider: "claude", Backend: "anthropic"}
	if err := profile.Validate(p); err != nil {
		t.Fatalf("nil ContextServers (no saved selection) should validate: %v", err)
	}
}

func TestValidate_NonEmptyContextServersIsFine(t *testing.T) {
	p := profile.Profile{Name: "trimmed", Provider: "claude", Backend: "anthropic", ContextServers: []string{"moxy/moxy"}}
	if err := profile.Validate(p); err != nil {
		t.Fatalf("non-empty ContextServers should validate: %v", err)
	}
}

func TestBackendsForProvider(t *testing.T) {
	got := profile.Backends("claude")
	want := []string{"anthropic", "gateway", "local"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Backends(claude) = %v, want %v", got, want)
	}
	if profile.Backends("nope") != nil {
		t.Fatal("unknown provider should return nil")
	}
}

func TestEnvDecodes(t *testing.T) {
	var f struct {
		Profile []profile.Profile `toml:"profile"`
	}
	src := "[[profile]]\nname = \"x\"\n[profile.env]\nANTHROPIC_DEFAULT_HAIKU_MODEL = \"m\"\n"
	if _, err := toml.Decode(src, &f); err != nil {
		t.Fatal(err)
	}
	if f.Profile[0].Env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "m" {
		t.Fatalf("env not decoded: %#v", f.Profile[0])
	}
}

func TestContextSelectionDecodes(t *testing.T) {
	var f struct {
		Profile []profile.Profile `toml:"profile"`
	}
	src := `
[[profile]]
name = "trimmed"
context_servers = ["moxy", "caldav"]
[profile.context_excluded]
moxy = ["folio.read", "grit.status"]
`
	if _, err := toml.Decode(src, &f); err != nil {
		t.Fatal(err)
	}
	p := f.Profile[0]
	if !reflect.DeepEqual(p.ContextServers, []string{"moxy", "caldav"}) {
		t.Errorf("context_servers not decoded: %#v", p.ContextServers)
	}
	if !reflect.DeepEqual(p.ContextExcluded["moxy"], []string{"folio.read", "grit.status"}) {
		t.Errorf("context_excluded not decoded: %#v", p.ContextExcluded)
	}
}
