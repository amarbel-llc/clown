package main

import (
	"testing"

	"code.linenisgreat.com/clown/internal/profile"
)

func templateByName(t *testing.T, key string) profile.Profile {
	t.Helper()
	for _, tpl := range profileTemplates {
		if tpl.key == key {
			return tpl.p
		}
	}
	t.Fatalf("no template %q", key)
	return profile.Profile{}
}

func TestProfileTemplates(t *testing.T) {
	or := templateByName(t, "claude-openrouter")
	if or.URL != "https://openrouter.ai/api" {
		t.Errorf("claude-openrouter url = %q (must be /api, NOT /api/v1)", or.URL)
	}
	if or.Token != "${OPENROUTER_API_KEY}" || or.Model != "" || or.Backend != "gateway" {
		t.Errorf("claude-openrouter template: %#v", or)
	}
}

// TestProfileTemplates_Openrouter guards the Phase B first-class openrouter
// provider template (docs/plans/2026-07-24-openrouter-non-anthropic-design.md
// Section 3): no URL stored (hardcoded by runOpencode), Token+Model present,
// and it must pass profile.Validate.
func TestProfileTemplates_Openrouter(t *testing.T) {
	p := templateByName(t, "openrouter")
	if p.Provider != "openrouter" {
		t.Errorf("provider = %q, want openrouter", p.Provider)
	}
	if p.Backend != "gateway" {
		t.Errorf("backend = %q, want gateway", p.Backend)
	}
	if p.URL != "" {
		t.Errorf("url = %q, want empty (hardcoded by runOpencode)", p.URL)
	}
	if p.Token != "${OPENROUTER_API_KEY}" {
		t.Errorf("token = %q, want ${OPENROUTER_API_KEY}", p.Token)
	}
	if p.Model == "" {
		t.Error("model must be pre-filled (e.g. openai/gpt-4o)")
	}
	if err := profile.Validate(p); err != nil {
		t.Errorf("template profile must pass validation: %v", err)
	}
}

// TestIsOpenRouterProfile guards the trigger condition that gates the
// dynamic OpenRouter model picker (#195, maybeApplyOpenRouterModelPicker):
// it must fire for exactly the two OpenRouter-backed shapes and stay silent
// for everything else, including profiles that merely resemble one (e.g. an
// opencode+gateway profile pointed at a different, non-OpenRouter URL).
func TestIsOpenRouterProfile(t *testing.T) {
	cases := []struct {
		name string
		v    profileFormValues
		want bool
	}{
		{"first-class openrouter provider", profileFormValues{Provider: "openrouter"}, true},
		{
			"opencode+gateway pointed at OpenRouter's endpoint",
			profileFormValues{Provider: "opencode", URL: "https://openrouter.ai/api/v1"},
			true,
		},
		{
			"opencode+gateway URL padded with whitespace still matches",
			profileFormValues{Provider: "opencode", URL: "  https://openrouter.ai/api/v1  "},
			true,
		},
		{
			"opencode+gateway pointed elsewhere does not match",
			profileFormValues{Provider: "opencode", URL: "https://api.example.com/v1"},
			false,
		},
		{
			"claude+gateway on the Anthropic-skin OpenRouter URL does not match",
			profileFormValues{Provider: "claude", URL: "https://openrouter.ai/api"},
			false,
		},
		{"crush provider never matches", profileFormValues{Provider: "crush", URL: "https://openrouter.ai/api/v1"}, false},
	}
	for _, c := range cases {
		if got := isOpenRouterProfile(c.v); got != c.want {
			t.Errorf("%s: isOpenRouterProfile(%#v) = %v, want %v", c.name, c.v, got, c.want)
		}
	}
}

func TestValidateProfileNameField(t *testing.T) {
	existing := map[string]bool{"taken": true}
	v := validateProfileName(existing, "")
	if v("taken") == nil {
		t.Error("duplicate must be rejected on add")
	}
	if v("") == nil || v("has space") == nil {
		t.Error("empty / spaced names must be rejected")
	}
	if v("claude-openrouter2") != nil {
		t.Error("plain kebab name must pass")
	}
	// edit mode: own original name allowed
	if validateProfileName(existing, "taken")("taken") != nil {
		t.Error("edit may keep its own name")
	}
}

func TestFormValuesToProfile(t *testing.T) {
	v := profileFormValues{
		Name: " or ", Display: "X", Provider: "claude",
		Backend: "gateway", URL: " https://openrouter.ai/api ", Token: "${OPENROUTER_API_KEY}",
	}
	p := v.toProfile()
	if p.Name != "or" || p.URL != "https://openrouter.ai/api" {
		t.Errorf("fields must be trimmed: %#v", p)
	}
	if err := profile.Validate(p); err != nil {
		t.Fatal(err)
	}
}

// TestFormValuesToProfile_GatewayViaJugglerModel guards the shape the
// juggler model registry feature depends on: a gateway profile with no
// inline URL/Token, built through `clown profile add`/`edit`, must produce
// a Profile that passes profile.Validate — mirroring the profile.go-level
// TestValidateGatewayViaJugglerModel, but exercised through the form's own
// toProfile() path.
func TestFormValuesToProfile_GatewayViaJugglerModel(t *testing.T) {
	v := profileFormValues{
		Name: "juggler-gw", Provider: "claude",
		Backend: "gateway", Model: "remote-model-a",
	}
	p := v.toProfile()
	if err := profile.Validate(p); err != nil {
		t.Fatalf("gateway+model (no url) built via the form should validate: %v", err)
	}
}

// TestFormValuesToProfile_GatewayViaJugglerModelOnlyForClaude guards the
// other half of that gate: opencode/crush have no juggler-resolution
// fallback, so a Model must not exempt them from the form's url+token
// requirement either — mirrors profile.go's
// TestValidateGatewayViaJugglerModelOnlyForClaude via the form's toProfile()
// path.
func TestFormValuesToProfile_GatewayViaJugglerModelOnlyForClaude(t *testing.T) {
	for _, prov := range []string{"opencode", "crush"} {
		v := profileFormValues{
			Name: "juggler-gw-" + prov, Provider: prov,
			Backend: "gateway", Model: "remote-model-a",
		}
		p := v.toProfile()
		if err := profile.Validate(p); err == nil {
			t.Errorf("%s+gateway+model (no url/token) built via the form should still fail validation", prov)
		}
	}
}

func TestProfileTemplates_OpenRouterOpencode(t *testing.T) {
	p := templateByName(t, "openrouter-opencode")
	if p.Provider != "opencode" {
		t.Errorf("provider = %q, want opencode", p.Provider)
	}
	if p.Backend != "gateway" {
		t.Errorf("backend = %q, want gateway", p.Backend)
	}
	if p.URL != "https://openrouter.ai/api/v1" {
		t.Errorf("url = %q, want https://openrouter.ai/api/v1", p.URL)
	}
	if p.Token != "${OPENROUTER_API_KEY}" {
		t.Errorf("token = %q, want ${OPENROUTER_API_KEY}", p.Token)
	}
	if p.Model == "" {
		t.Error("model must be pre-filled (e.g. openai/gpt-4o)")
	}
	if err := profile.Validate(p); err != nil {
		t.Errorf("template profile must pass validation: %v", err)
	}
}

// TestEditRoundTripPreservesUneditableFields guards against the exact
// failure mode profileFormValues.toProfile/valuesFromProfile must avoid:
// editing a profile's form-visible fields (name/provider/model/etc.) via
// `clown profile edit` silently wiping Env or a saved --cheap-context
// selection, since the huh form has no field for either.
func TestEditRoundTripPreservesUneditableFields(t *testing.T) {
	original := profile.Profile{
		Name: "trimmed", Provider: "claude", Backend: "anthropic",
		Env:             map[string]string{"FOO": "bar"},
		ContextServers:  []string{"moxy/moxy"},
		ContextExcluded: map[string][]string{"moxy/moxy": {"grit.status"}},
	}
	v := valuesFromProfile(original)
	// Simulate the form editing only Display (the form has no Env/Context
	// fields at all, so v's env/contextServers/contextExcluded are left
	// exactly as valuesFromProfile set them).
	v.Display = "Trimmed (edited)"
	got := v.toProfile()

	if got.Env["FOO"] != "bar" {
		t.Errorf("Env dropped across edit round trip: %#v", got.Env)
	}
	if len(got.ContextServers) != 1 || got.ContextServers[0] != "moxy/moxy" {
		t.Errorf("ContextServers dropped across edit round trip: %#v", got.ContextServers)
	}
	if len(got.ContextExcluded["moxy/moxy"]) != 1 || got.ContextExcluded["moxy/moxy"][0] != "grit.status" {
		t.Errorf("ContextExcluded dropped across edit round trip: %#v", got.ContextExcluded)
	}
}
