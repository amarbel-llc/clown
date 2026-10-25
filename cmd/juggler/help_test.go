package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// captureOutput runs fn with os.Stdout and os.Stderr redirected and returns
// what each received.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	read := func(r *os.File, into chan<- string) {
		b, _ := io.ReadAll(r)
		into <- string(b)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	savedOut, savedErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	outC, errC := make(chan string, 1), make(chan string, 1)
	go read(outR, outC)
	go read(errR, errC)
	defer func() { os.Stdout, os.Stderr = savedOut, savedErr }()
	fn()
	outW.Close()
	errW.Close()
	os.Stdout, os.Stderr = savedOut, savedErr
	return <-outC, <-errC
}

// clown#56: -h, --help and help print the usage to stdout and exit 0,
// rather than "unknown command" and exit 1.
func TestHelpPrintsUsageAndExitsZero(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "help"} {
		var code int
		out, _ := captureOutput(t, func() { code = run([]string{arg}) })
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

// A bare juggler prints the usage to stderr and exits 1 (usage); an unknown
// command does too, naming it.
func TestBareAndUnknownCommandAreUsageErrors(t *testing.T) {
	var code int
	out, errOut := captureOutput(t, func() { code = run(nil) })
	if code != 1 || out != "" || !strings.Contains(errOut, "usage: juggler") {
		t.Errorf("bare juggler: exit %d, stdout %q, stderr %q; want 1, nothing, the usage", code, out, errOut)
	}
	out, errOut = captureOutput(t, func() { code = run([]string{"frobnicate"}) })
	if code == 0 || out != "" || !strings.Contains(errOut, `unknown command "frobnicate"`) {
		t.Errorf("juggler frobnicate: exit %d, stdout %q, stderr %q; want non-zero, nothing, unknown command", code, out, errOut)
	}
}

// Every verb the dispatch accepts (the verbs table is the dispatch) has a
// usage line naming it, appears in the top-level usage, and answers
// `<verb> -h` / `--help` with exactly that line and exit 0, without running
// (daemon and mcp included: before the help check they would have started a
// daemon and served MCP on stdin).
func TestEveryVerbHasHelp(t *testing.T) {
	if len(verbs) == 0 {
		t.Fatal("no verbs")
	}
	for name, v := range verbs {
		if !strings.HasPrefix(v.usage, "usage: juggler "+name+" ") && v.usage != "usage: juggler "+name {
			t.Errorf("verb %s: usage %q does not start with %q", name, v.usage, "usage: juggler "+name)
		}
		if !strings.Contains(usage, "|"+name+"|") && !strings.Contains(usage, "<"+name+"|") && !strings.Contains(usage, "|"+name+">") {
			t.Errorf("verb %s is missing from the top-level usage %q", name, usage)
		}
		for _, flag := range []string{"-h", "--help"} {
			code, out := runBounded(t, name, flag)
			if code != 0 || out != v.usage+"\n" {
				t.Errorf("juggler %s %s: exit %d, stdout %q; want 0 and exactly %q", name, flag, code, out, v.usage)
			}
		}
	}
}

// runBounded runs juggler with args under captureOutput, failing the test
// (with os.Stdout/os.Stderr restored first, so later tests are not
// redirected) if it does not return within 5s.
func runBounded(t *testing.T, args ...string) (int, string) {
	t.Helper()
	savedOut, savedErr := os.Stdout, os.Stderr
	done := make(chan struct{})
	var code int
	var out string
	go func() {
		out, _ = captureOutput(t, func() { code = run(args) })
		close(done)
	}()
	select {
	case <-done:
		return code, out
	case <-time.After(5 * time.Second):
		os.Stdout, os.Stderr = savedOut, savedErr
		t.Fatalf("juggler %v did not return", args)
		return 0, ""
	}
}

// clown#56 review: -h/--help anywhere before a literal -- asks for the
// subcommand's usage (so `start foo -h` and `run --brief f -h` do too); one
// after -- is passed through untouched.
func TestHelpAnywhereBeforeDoubleDash(t *testing.T) {
	for _, args := range [][]string{
		{"start", "foo", "-h"},
		{"run", "--brief", "f", "-h"},
		{"spawn", "--new-run", "--help"},
		{"prompt", "m", "--max-tokens", "5", "-h"},
	} {
		code, out := runBounded(t, args...)
		if code != 0 || out != verbs[args[0]].usage+"\n" {
			t.Errorf("juggler %v: exit %d, stdout %q; want 0 and exactly %q", args, code, out, verbs[args[0]].usage)
		}
	}
	// models is read-only, so this is harmless even where a daemon answers.
	if _, out := runBounded(t, "models", "--", "-h"); out == verbs["models"].usage+"\n" {
		t.Errorf("juggler models -- -h printed the usage; a -h after -- is not a help request")
	}
}

// A verb a test enumerates by hand would drift from the table; the table
// test above covers them all. This keeps the explicit list as a check that
// the known verbs are all still in the table.
func TestKnownVerbsAreDispatched(t *testing.T) {
	for _, sub := range []string{"daemon", "start", "stop", "status", "list", "models", "model", "prompt", "mcp", "decide", "run", "spawn", "exit-wake", "resolve", "clean", "job-ledger", "handles", "download"} {
		if _, ok := verbs[sub]; !ok {
			t.Errorf("verb %s is no longer dispatched", sub)
		}
	}
}
