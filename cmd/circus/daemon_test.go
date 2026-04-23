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
