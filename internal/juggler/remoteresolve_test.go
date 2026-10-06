package juggler

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRemoteModelFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.toml")
	t.Setenv("JUGGLER_MODELS_PATH", path)
	t.Setenv("RESOLVE_TEST_TOKEN", "sekret")
	if err := SaveRemoteModels(path, []RemoteModel{
		{Name: "gw", Style: StyleOpenAICompat, URL: "https://gw.example/v1", Token: "${RESOLVE_TEST_TOKEN}"},
		{Name: "jev", Style: StyleDecisions, URL: "https://or.example/api/alpha/decisions", Token: "lit"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveRemoteModelFromFile("gw")
	if err != nil {
		t.Fatal(err)
	}
	want := ResolveModelResult{Kind: ModelKindRemote, Style: "openai-compat", URL: "https://gw.example/v1", Token: "sekret"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}

	jev, err := ResolveRemoteModelFromFile("jev")
	if err != nil {
		t.Fatal(err)
	}
	if jev.Style != StyleDecisions || !IsRemoteStyle(jev.Style) {
		t.Errorf("decisions entry not accepted: %+v", jev)
	}
}

func TestResolveRemoteModelFromFileLocalNeedsDaemon(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.toml")
	t.Setenv("JUGGLER_MODELS_PATH", path)
	if err := SaveRemoteModels(path, []RemoteModel{{Name: "gw", Style: StyleAnthropic, URL: "u"}}); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveRemoteModelFromFile("qwen3-coder")
	if !errors.Is(err, ErrDaemonRequired) {
		t.Fatalf("err = %v, want ErrDaemonRequired", err)
	}

	// Absent models file: still daemon-required, not a load error.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRemoteModelFromFile("x"); !errors.Is(err, ErrDaemonRequired) {
		t.Fatalf("absent file: err = %v, want ErrDaemonRequired", err)
	}
}

func TestIsRemoteStyle(t *testing.T) {
	for _, s := range []string{"anthropic", "openai-compat", "decisions"} {
		if !IsRemoteStyle(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	if IsRemoteStyle("bogus") || IsRemoteStyle("") {
		t.Error("bogus/empty style accepted")
	}
}
