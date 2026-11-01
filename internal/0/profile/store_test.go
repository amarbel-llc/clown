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
	if err := Save(path, in, "0.0.0-test"); err != nil {
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
	if err := Save(path, in, "0.0.0-test"); err != nil {
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
	if err := Save(path, in, "0.0.0-test"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"context_servers", "context_excluded", "url", "token", "env"} {
		if strings.Contains(string(raw), key) {
			t.Errorf("omitempty failed; wrote %q for a profile that leaves it empty:\n%s", key, raw)
		}
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
	if err := Save(path, in, "0.0.0-test"); err != nil {
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

func TestSaveStampsRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	in := []Profile{{Name: "a", Provider: "claude", Backend: "anthropic"}}
	if err := Save(path, in, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	// No profile uses a protocol-2 key, so the file stays readable by older clowns.
	want := RegistryStamp{ClownVersion: "1.2.3", Protocol: 1}
	if reg.Stamp != want {
		t.Fatalf("stamp = %#v, want %#v", reg.Stamp, want)
	}
	if !reflect.DeepEqual(reg.Profiles, in) || len(reg.Undecoded) != 0 {
		t.Fatalf("registry = %#v", reg)
	}

	// A later write by another clown replaces the stamp.
	if err := Save(path, in, "1.2.4"); err != nil {
		t.Fatal(err)
	}
	reg, err = LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Stamp.ClownVersion != "1.2.4" {
		t.Fatalf("stamp after re-save = %#v", reg.Stamp)
	}
}

// A pre-stamping file (profiles, comments, no [registry]) must come out of
// Save still holding every profile under the right table — the stamp may not
// swallow or be swallowed by the last [[profile]].
func TestSaveStampsLegacyFileWithoutLosingProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	orig := "# mine\n[[profile]]\nname = \"a\"\nprovider = \"claude\"\nbackend = \"anthropic\"\n\n[[profile]]\nname = \"b\"\nprovider = \"claude\"\nbackend = \"anthropic\"\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Stamp != (RegistryStamp{}) {
		t.Fatalf("legacy file has stamp %#v", before.Stamp)
	}
	if err := Save(path, before.Profiles, "9.9.9"); err != nil {
		t.Fatal(err)
	}
	after, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Stamp.ClownVersion != "9.9.9" || !reflect.DeepEqual(after.Profiles, before.Profiles) || len(after.Undecoded) != 0 {
		raw, _ := os.ReadFile(path)
		t.Fatalf("after = %#v\nfile:\n%s", after, raw)
	}
}

func TestLoadRegistryReportsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	body := "[registry]\nclown_version = \"9\"\nprotocol = \"1\"\n\n[[profile]]\nname = \"a\"\nprovider = \"claude\"\nbackend = \"anthropic\"\nfrom_the_future = true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Undecoded) != 1 || !strings.Contains(reg.Undecoded[0], "from_the_future") {
		t.Fatalf("Undecoded = %v", reg.Undecoded)
	}
}

func TestBuiltinParses(t *testing.T) {
	ps, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) == 0 {
		t.Fatal("no builtin profiles")
	}
	for _, p := range ps {
		if err := Validate(p); err != nil {
			t.Errorf("builtin %q: %v", p.Name, err)
		}
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
