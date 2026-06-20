//go:build linux

package ptysuspend

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// TestRelayInputInterceptsEscapeKey is the core proof: the escape key (here
// ctrl-z, 0x1a) in the input stream is NOT forwarded to the child and instead
// triggers onEscape (the shell-out hook). This is what reclaims the key from a
// raw-mode TUI that would otherwise swallow the byte.
func TestRelayInputInterceptsEscapeKey(t *testing.T) {
	in := bytes.NewReader([]byte("ab\x1acd\x1aef"))
	var out bytes.Buffer
	calls := 0
	relayInput(in, &out, DefaultEscapeKey, func() { calls++ })

	if got := out.String(); got != "abcdef" {
		t.Errorf("forwarded = %q, want %q (escape key stripped, surrounding bytes kept)", got, "abcdef")
	}
	if calls != 2 {
		t.Errorf("onEscape calls = %d, want 2", calls)
	}
}

// TestRelayInputForwardsPlain confirms ordinary input (incl. other control
// bytes like ^C=0x03, which is not the escape key) passes through untouched and
// never triggers the escape.
func TestRelayInputForwardsPlain(t *testing.T) {
	in := bytes.NewReader([]byte("hello\x03 world"))
	var out bytes.Buffer
	calls := 0
	relayInput(in, &out, DefaultEscapeKey, func() { calls++ })

	if got := out.String(); got != "hello\x03 world" {
		t.Errorf("forwarded = %q, want unchanged", got)
	}
	if calls != 0 {
		t.Errorf("onEscape calls = %d, want 0", calls)
	}
}

// TestRelayInputCustomKey confirms a configured non-default escape key is the
// one intercepted (and ctrl-z then passes through).
func TestRelayInputCustomKey(t *testing.T) {
	const ctrlG = 0x07
	in := bytes.NewReader([]byte("a\x07b\x1ac"))
	var out bytes.Buffer
	calls := 0
	relayInput(in, &out, ctrlG, func() { calls++ })

	if got := out.String(); got != "ab\x1ac" {
		t.Errorf("forwarded = %q, want %q (^G stripped, ^Z forwarded)", got, "ab\x1ac")
	}
	if calls != 1 {
		t.Errorf("onEscape calls = %d, want 1", calls)
	}
}

// TestOpenInnerPTYPair confirms the /dev/ptmx allocator returns a connected
// master/slave pair (a byte written to the slave is read from the master).
// Skipped where /dev/ptmx is unavailable (some sandboxes).
func TestOpenInnerPTYPair(t *testing.T) {
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		t.Skip("no /dev/ptmx on this host")
	}
	m, s, err := openInnerPTY()
	if err != nil {
		t.Fatalf("openInnerPTY: %v", err)
	}
	defer m.Close()
	defer s.Close()

	// Slave -> master is the output path (no canonical line buffering); "hello"
	// has no newline so output post-processing (ONLCR) leaves it intact.
	if _, err := s.Write([]byte("hello")); err != nil {
		t.Fatalf("slave write: %v", err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(m, buf); err != nil {
		t.Fatalf("master read: %v", err)
	}
	if string(buf) != "hello" {
		t.Errorf("master read %q, want %q (pty pair not connected)", buf, "hello")
	}
}
