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

// `daemon -h` and `mcp -h` print their usage and return, rather than binding
// a control socket or serving MCP on stdin. Not run red: before the fix they
// would have started for real.
func TestDaemonAndMCPHelpDoNotStartAnything(t *testing.T) {
	for _, sub := range []string{"daemon", "mcp"} {
		for _, flag := range []string{"-h", "--help"} {
			code, out := runBounded(t, sub, flag)
			if code != 0 || out != subcommandUsage[sub]+"\n" {
				t.Errorf("juggler %s %s: exit %d, stdout %q; want 0 and exactly its usage", sub, flag, code, out)
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
		if code != 0 || out != subcommandUsage[args[0]]+"\n" {
			t.Errorf("juggler %v: exit %d, stdout %q; want 0 and exactly %q", args, code, out, subcommandUsage[args[0]])
		}
	}
	// models is read-only, so this is harmless even where a daemon answers.
	if _, out := runBounded(t, "models", "--", "-h"); out == subcommandUsage["models"]+"\n" {
		t.Errorf("juggler models -- -h printed the usage; a -h after -- is not a help request")
	}
}

// clown#56: <subcommand> -h / --help prints that subcommand's usage to
// stdout and exits 0, instead of being taken as a positional (start, stop,
// status: a model or alias named "-h") or exiting 1 on flag.ErrHelp (run,
// spawn, decide, ...). daemon and mcp are covered in
// TestDaemonAndMCPHelpDoNotStartAnything: before the fix, `daemon -h`
// started a daemon and `mcp -h` served on stdin.
func TestSubcommandHelpPrintsItsUsage(t *testing.T) {
	for _, sub := range []string{"start", "stop", "status", "list", "models", "model", "prompt", "decide", "run", "spawn", "exit-wake", "resolve", "clean", "job-ledger", "handles", "download"} {
		for _, flag := range []string{"-h", "--help"} {
			var code int
			out, _ := captureOutput(t, func() { code = run([]string{sub, flag}) })
			if code != 0 {
				t.Errorf("juggler %s %s: exit %d, want 0", sub, flag, code)
			}
			if out != subcommandUsage[sub]+"\n" {
				t.Errorf("juggler %s %s: stdout %q, want exactly %q", sub, flag, out, subcommandUsage[sub])
			}
		}
	}
}
