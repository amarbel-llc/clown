package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAppendPromptFragments confirms dynamic fragments are appended after the
// existing static content (append-last) and joined with the two-newline
// separator promptwalk uses.
func TestAppendPromptFragments(t *testing.T) {
	p := filepath.Join(t.TempDir(), "append.txt")
	if err := os.WriteFile(p, []byte("STATIC-IDENTITY\n\nSTATIC-USER\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendPromptFragments(p, []string{"DYN-A", "DYN-B"}); err != nil {
		t.Fatalf("appendPromptFragments: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// Exact output: static preserved verbatim, dynamic appended last, joined by
	// a single blank line — no extra newline between the static tail (which
	// already ends with "\n\n") and the first fragment.
	want := "STATIC-IDENTITY\n\nSTATIC-USER\n\nDYN-A\n\nDYN-B\n"
	if string(got) != want {
		t.Errorf("appendPromptFragments output\n got: %q\nwant: %q", string(got), want)
	}
}

func TestAppendPromptFragmentsMissingFile(t *testing.T) {
	if err := appendPromptFragments(filepath.Join(t.TempDir(), "nope.txt"), []string{"x"}); err == nil {
		t.Error("want error appending to nonexistent file, got nil")
	}
}
