package circusmodels

import "testing"

func TestRegistry_ParsesAllFields(t *testing.T) {
	entries, err := Registry()
	if err != nil {
		t.Fatalf("Registry: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one registry entry")
	}
	for _, e := range entries {
		if e.Name == "" {
			t.Errorf("entry has empty name: %+v", e)
		}
		if e.URL == "" {
			t.Errorf("entry %q has empty url", e.Name)
		}
		if len(e.SHA256) != 64 {
			t.Errorf("entry %q sha256 must be 64 hex chars, got %d", e.Name, len(e.SHA256))
		}
		if e.Size == 0 {
			t.Logf("warning: entry %q has size=0 (placeholder)", e.Name)
		}
		if e.Description == "" {
			t.Errorf("entry %q has empty description", e.Name)
		}
	}
}

func TestRegistry_ContainsExpectedModels(t *testing.T) {
	entries, err := Registry()
	if err != nil {
		t.Fatalf("Registry: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 registry entries, got %d", len(entries))
	}
	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
	}
	for _, want := range []string{"qwen3-0.6b", "qwen3-1.7b", "qwen3-4b", "gemma3-1b", "gemma3-4b"} {
		if !names[want] {
			t.Errorf("expected model %q in registry", want)
		}
	}
}

func TestFindEntry_Found(t *testing.T) {
	entries := []RegistryEntry{
		{Name: "alpha", URL: "u1"},
		{Name: "beta", URL: "u2"},
	}
	got, ok := FindEntry("beta", entries)
	if !ok {
		t.Fatal("expected to find beta")
	}
	if got.URL != "u2" {
		t.Errorf("got url %q, want u2", got.URL)
	}
}

func TestFindEntry_NotFound(t *testing.T) {
	entries := []RegistryEntry{{Name: "alpha"}}
	if _, ok := FindEntry("missing", entries); ok {
		t.Fatal("expected not-found")
	}
}
