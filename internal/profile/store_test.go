package profile

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "profiles.toml")
	in := []Profile{{
		Name: "or", Display: "Claude (OpenRouter)", Provider: "claude",
		Backend: "gateway", URL: "https://openrouter.ai/api", Token: "${OPENROUTER_API_KEY}",
	}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip: %#v != %#v", in, out)
	}
}

func TestSaveRoundTrip_ContextSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	in := []Profile{{
		Name: "trimmed", Provider: "claude", Backend: "anthropic",
		ContextServers:  []string{"moxy", "caldav"},
		ContextExcluded: map[string][]string{"moxy": {"folio.read", "grit.status"}},
	}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip: %#v != %#v", in, out)
	}
}

func TestSaveRoundTrip_NoContextSelectionOmitsFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	in := []Profile{{Name: "plain", Provider: "claude", Backend: "anthropic"}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "context_servers") || strings.Contains(string(raw), "context_excluded") {
		t.Errorf("omitempty failed; wrote context fields for a profile with no saved selection:\n%s", raw)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].ContextServers != nil || out[0].ContextExcluded != nil {
		t.Errorf("expected nil Context fields after round trip, got %#v", out[0])
	}
}

func TestSavePreservesComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	orig := "# my profiles\n\n[[profile]]\n# daily driver\nname = \"or\"\ndisplay = \"old\"\nprovider = \"claude\"\nbackend = \"gateway\"\nmodel = \"\"\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	in := []Profile{
		{
			Name: "or", Display: "new", Provider: "claude", Backend: "gateway",
			Env:             map[string]string{"FOO": "bar"},
			ContextServers:  []string{"moxy"},
			ContextExcluded: map[string][]string{"moxy": {"folio.read"}},
		},
		{Name: "second", Provider: "claude", Backend: "anthropic"},
	}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# my profiles", "# daily driver", `"new"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("saved file missing %q:\n%s", want, raw)
		}
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip: %#v != %#v\nfile:\n%s", in, out, raw)
	}
}

func TestUpsertAndRemove(t *testing.T) {
	base := []Profile{{Name: "a"}, {Name: "b"}}
	up := Upsert(base, Profile{Name: "b", Display: "B2"})
	if len(up) != 2 || up[1].Display != "B2" {
		t.Fatalf("upsert replace: %#v", up)
	}
	up = Upsert(up, Profile{Name: "c"})
	if len(up) != 3 {
		t.Fatalf("upsert append: %#v", up)
	}
	rm, found := Remove(up, "a")
	if !found || len(rm) != 2 {
		t.Fatalf("remove: %#v found=%v", rm, found)
	}
	if _, found := Remove(rm, "nope"); found {
		t.Fatal("remove of absent name must report false")
	}
}
