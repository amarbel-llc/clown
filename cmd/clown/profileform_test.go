package main

import (
	"testing"

	"github.com/amarbel-llc/clown/internal/profile"
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
	or := templateByName(t, "openrouter")
	if or.URL != "https://openrouter.ai/api" {
		t.Errorf("openrouter url = %q (must be /api, NOT /api/v1)", or.URL)
	}
	if or.Token != "${OPENROUTER_API_KEY}" || or.Model != "" || or.Backend != "gateway" {
		t.Errorf("openrouter template: %#v", or)
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
