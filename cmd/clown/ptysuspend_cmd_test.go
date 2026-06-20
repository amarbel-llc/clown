package main

import (
	"testing"

	"github.com/amarbel-llc/clown/internal/clownfile"
)

func TestParseCaretKey(t *testing.T) {
	cases := []struct {
		in   string
		want byte
		ok   bool
	}{
		{"^Z", 0x1a, true},
		{"^z", 0x1a, true}, // case-insensitive
		{"^C", 0x03, true},
		{"^@", 0x00, true},
		{"^[", 0x1b, true},
		{"", 0, false},
		{"Z", 0, false},
		{"^", 0, false},
		{"^ZZ", 0, false},
	}
	for _, c := range cases {
		got, ok := parseCaretKey(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseCaretKey(%q) = (%#x, %v), want (%#x, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestResolvePtyOptions(t *testing.T) {
	t.Setenv("SHELL", "/bin/testsh")

	// Defaults: disabled, no command -> fallback $SHELL, default key (0 => ^Z).
	o := resolvePtyOptions(clownfile.Attach{})
	if o.Enabled {
		t.Error("default must be disabled")
	}
	if o.EscapeKey != 0 {
		t.Errorf("default EscapeKey = %#x, want 0 (=> ^Z)", o.EscapeKey)
	}
	if len(o.EscapeArgv) != 1 || o.EscapeArgv[0] != "/bin/testsh" {
		t.Errorf("fallback EscapeArgv = %v, want [/bin/testsh]", o.EscapeArgv)
	}

	// Enabled + custom key + env-interpolated command.
	t.Setenv("FOO", "bar")
	enabled := true
	o = resolvePtyOptions(clownfile.Attach{
		PtySuspend:    &enabled,
		EscapeKey:     "^G",
		EscapeCommand: []string{"sc", "exec", "${FOO}"},
	})
	if !o.Enabled {
		t.Error("must be enabled")
	}
	if o.EscapeKey != 0x07 {
		t.Errorf("EscapeKey = %#x, want 0x07 (^G)", o.EscapeKey)
	}
	if len(o.EscapeArgv) != 3 || o.EscapeArgv[2] != "bar" {
		t.Errorf("EscapeArgv = %v, want [sc exec bar] (env interpolated)", o.EscapeArgv)
	}
}
