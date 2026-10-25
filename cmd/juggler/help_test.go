package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = saved }()
	fn()
	w.Close()
	os.Stdout = saved
	return <-done
}

// clown#56: -h, --help and help print the usage to stdout and exit 0,
// rather than "unknown command" and exit 1.
func TestHelpPrintsUsageAndExitsZero(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "help"} {
		var code int
		out := captureStdout(t, func() { code = run([]string{arg}) })
		if code != 0 {
			t.Errorf("juggler %s: exit %d, want 0", arg, code)
		}
		for _, want := range []string{"usage: juggler", "spawn", "daemon", "download"} {
			if !strings.Contains(out, want) {
				t.Errorf("juggler %s: stdout %q lacks %q", arg, out, want)
			}
		}
	}
}
