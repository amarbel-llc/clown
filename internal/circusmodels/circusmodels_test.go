package circusmodels

import (
	"os"
	"path/filepath"
	"testing"
)

func TestList_Empty(t *testing.T) {
	dir := t.TempDir()
	names, err := List(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected empty, got %v", names)
	}
}

func TestList_SomeFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha.gguf", "beta.gguf", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	names, err := List(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %v", names)
	}
	if names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("unexpected names: %v", names)
	}
}

func TestList_MissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	names, err := List(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected empty, got %v", names)
	}
}
