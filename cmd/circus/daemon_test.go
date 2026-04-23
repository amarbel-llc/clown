package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveModel_AbsolutePath(t *testing.T) {
	path := "/absolute/path/to/model.gguf"
	got, err := resolveModel(path, "/some/dir")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != path {
		t.Errorf("got %q, want %q", got, path)
	}
}

func TestResolveModel_FoundInDir(t *testing.T) {
	dir := t.TempDir()
	name := "mymodel"
	expected := filepath.Join(dir, name+".gguf")
	if err := os.WriteFile(expected, []byte("fake"), 0o644); err != nil {
		t.Fatalf("creating temp file: %v", err)
	}

	got, err := resolveModel(name, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestResolveModel_NotFound(t *testing.T) {
	dir := t.TempDir()
	name := "missing"

	_, err := resolveModel(name, dir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `model "missing" not found in`) {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestListModels_Empty(t *testing.T) {
	dir := t.TempDir()
	names, err := listModels(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected empty, got %v", names)
	}
}

func TestListModels_SomeFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha.gguf", "beta.gguf", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	names, err := listModels(dir)
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
