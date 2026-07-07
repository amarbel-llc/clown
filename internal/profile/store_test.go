package profile

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "profiles.toml")
	in := []Profile{{Name: "or", Display: "Claude (OpenRouter)", Provider: "claude",
		Backend: "gateway", URL: "https://openrouter.ai/api", Token: "${OPENROUTER_API_KEY}"}}
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
