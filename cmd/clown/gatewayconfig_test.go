package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteLocalGatewayConfigFile_CreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "opencode.toml")
	if err := writeLocalGatewayConfigFile(path, "http://localhost:11434/v1", "local"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`url = "http://localhost:11434/v1"`, `token = "local"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("new file missing %q:\n%s", want, got)
		}
	}
}

func TestWriteLocalGatewayConfigFile_PreservesComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crush.toml")
	orig := "# my local gateway\nurl = \"http://old\" # keep me\ntoken = \"t\"\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeLocalGatewayConfigFile(path, "http://new", "t2"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# my local gateway", "# keep me", `"http://new"`, `"t2"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("rewritten file missing %q:\n%s", want, got)
		}
	}
}
