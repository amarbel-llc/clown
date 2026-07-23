package main

import (
	"io"
	"os"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything fn wrote. It restores os.Stdout before returning.
//
// WARNING: this mutates the process-global os.Stdout without synchronization.
// Tests that call captureStdout MUST NOT call t.Parallel() — concurrent tests
// would race on os.Stdout and interleave each other's captures.
func captureStdout(t *testing.T, fn func() int) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

// captureStderr is captureStdout for os.Stderr, with the same non-parallel
// warning: tests that call it MUST NOT call t.Parallel().
func captureStderr(t *testing.T, fn func() int) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return <-done
}
