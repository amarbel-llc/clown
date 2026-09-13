package main

import (
	"errors"
	"reflect"
	"testing"

	"code.linenisgreat.com/clown/internal/clownfile"
)

func TestResolveGroupID_InterpolatedGroupIDWinsWithoutRunningCommand(t *testing.T) {
	t.Setenv("CLOWN_TEST_GROUP", "repo/branch")
	a := clownfile.Attach{GroupID: "${CLOWN_TEST_GROUP}", GroupIDCommand: []string{"sc", "{session-id}"}}
	got := resolveGroupID(a, "/repo", "uuid-1", func([]string) (string, error) {
		t.Fatal("group-id-command must not run when group-id resolves non-empty")
		return "", nil
	})
	if got != "repo/branch" {
		t.Fatalf("got %q, want repo/branch", got)
	}
}

func TestResolveGroupID_FallsBackToCommandOutput(t *testing.T) {
	t.Setenv("CLOWN_TEST_GROUP", "")
	a := clownfile.Attach{
		GroupID:        "${CLOWN_TEST_GROUP}",
		GroupIDCommand: []string{"sc", "implicit-session-key", "--cwd", "{cwd}", "--claude-session-id", "{session-id}"},
	}
	var ran []string
	got := resolveGroupID(a, "/home/u/eng", "uuid-1", func(argv []string) (string, error) {
		ran = argv
		return "eng/37a88d451d600bbb\n", nil
	})
	if got != "eng/37a88d451d600bbb" {
		t.Fatalf("got %q, want eng/37a88d451d600bbb", got)
	}
	want := []string{"sc", "implicit-session-key", "--cwd", "/home/u/eng", "--claude-session-id", "uuid-1"}
	if !reflect.DeepEqual(ran, want) {
		t.Fatalf("ran %q, want %q", ran, want)
	}
}

func TestResolveGroupID_CommandFailureLeavesUngrouped(t *testing.T) {
	a := clownfile.Attach{GroupIDCommand: []string{"sc", "{session-id}"}}
	got := resolveGroupID(a, "/repo", "uuid-1", func([]string) (string, error) {
		return "", errors.New("exit status 3")
	})
	if got != "" {
		t.Fatalf("got %q, want ungrouped", got)
	}
}

func TestParseGroupIDCommandOutput_RejectsMultiLineOrSpaced(t *testing.T) {
	for _, out := range []string{"", "  \n", "a/b\nc/d\n", "a b"} {
		if got := parseGroupIDCommandOutput(out); got != "" {
			t.Errorf("parse(%q) = %q, want empty", out, got)
		}
	}
}
