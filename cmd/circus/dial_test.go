package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDialClient_MissingSocket points RINGMASTER_SOCKET at a path that
// does not exist and asserts dialClient returns an error AND prints the
// home-manager hint to stderr.
func TestDialClient_MissingSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "nope.sock")
	t.Setenv("RINGMASTER_SOCKET", socket)

	// Capture stderr. Restore the original os.Stderr immediately after
	// dialClient returns — leaving a closed pipe assigned to os.Stderr
	// for the rest of teardown would EBADF any later stderr writes
	// (the test framework's own output included).
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldStderr := os.Stderr
	os.Stderr = w

	_, dialErr := dialClient()
	os.Stderr = oldStderr
	w.Close()

	if dialErr == nil {
		t.Fatal("expected error from dialClient against nonexistent socket")
	}

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	got := buf.String()
	for _, want := range []string{
		"ringmaster is not running",
		"programs.ringmaster.enable",
		"home-manager switch",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr missing %q\nfull stderr:\n%s", want, got)
		}
	}
}
