package juggler

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSaveLoadRemoteModelsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "models.toml")
	in := []RemoteModel{{
		Name: "claude-openrouter", Style: "anthropic",
		URL: "https://openrouter.ai/api", Token: "${OPENROUTER_API_KEY}",
	}}
	if err := SaveRemoteModels(path, in); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	out, err := LoadRemoteModels(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip: %#v != %#v", in, out)
	}
}

func TestSaveRemoteModelsPreservesComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.toml")
	orig := "# my gateways\n\n[[model]]\n# openrouter via env token\nname = \"a\"\nstyle = \"anthropic\"\nurl = \"https://old\"\ntoken = \"${TOK}\"\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	in := []RemoteModel{
		{Name: "a", Style: "anthropic", URL: "https://new", Token: "${TOK}"},
		{Name: "b", Style: "openai-compat", URL: "https://b", Token: "lit"},
	}
	if err := SaveRemoteModels(path, in); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# my gateways", "# openrouter via env token", `"https://new"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("saved file missing %q:\n%s", want, got)
		}
	}
	out, err := LoadRemoteModels(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip: %#v != %#v", in, out)
	}
}

func TestLoadRemoteModelsMissingFileIsEmpty(t *testing.T) {
	out, err := LoadRemoteModels(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || len(out) != 0 {
		t.Fatalf("out=%#v err=%v, want empty/nil", out, err)
	}
}

func TestUpsertAndRemoveRemoteModel(t *testing.T) {
	base := []RemoteModel{{Name: "a"}, {Name: "b"}}
	up := UpsertRemoteModel(base, RemoteModel{Name: "b", URL: "new"})
	if len(up) != 2 || up[1].URL != "new" {
		t.Fatalf("upsert replace: %#v", up)
	}
	up = UpsertRemoteModel(up, RemoteModel{Name: "c"})
	if len(up) != 3 {
		t.Fatalf("upsert append: %#v", up)
	}
	rm, found := RemoveRemoteModel(up, "a")
	if !found || len(rm) != 2 {
		t.Fatalf("remove: %#v found=%v", rm, found)
	}
	if _, found := RemoveRemoteModel(rm, "nope"); found {
		t.Fatal("remove of absent name must report false")
	}
}

func TestRemoteModelsPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JUGGLER_MODELS_PATH", "")
	t.Setenv("HOME", dir)
	got, err := RemoteModelsPath()
	want := filepath.Join(dir, ".local", "share", "juggler", "models.toml")
	if err != nil || got != want {
		t.Fatalf("got %q err=%v, want %q", got, err, want)
	}
	t.Setenv("JUGGLER_MODELS_PATH", "/tmp/override.toml")
	got, err = RemoteModelsPath()
	if err != nil || got != "/tmp/override.toml" {
		t.Fatalf("override: got %q err=%v", got, err)
	}
}
