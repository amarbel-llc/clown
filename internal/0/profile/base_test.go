package profile

import (
	"path/filepath"
	"strings"
	"testing"
)

const pinHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestParseBase(t *testing.T) {
	for in, want := range map[string]struct{ name, pin string }{
		"canary":                       {"canary", ""},
		"canary@sha256:" + pinHex:      {"canary", pinHex},
		"canary-grit@sha256:" + pinHex: {"canary-grit", pinHex},
	} {
		name, pin, err := ParseBase(in)
		if err != nil || name != want.name || pin != want.pin {
			t.Errorf("ParseBase(%q) = %q, %q, %v; want %q, %q", in, name, pin, err, want.name, want.pin)
		}
	}
	for _, bad := range []string{
		"", "@sha256:" + pinHex, "canary@", "canary@sha256:", "canary@sha256:abc",
		"canary@sha512:" + pinHex, "canary@sha256:" + strings.ToUpper(pinHex), "canary@" + pinHex,
		"canary@sha256:" + pinHex + "00", " canary",
	} {
		if _, _, err := ParseBase(bad); err == nil {
			t.Errorf("ParseBase(%q) accepted", bad)
		}
	}
}

func TestValidateRefusesAMalformedBase(t *testing.T) {
	p := Profile{Name: "p", Provider: "claude", Backend: "anthropic", Base: "canary@sha256:nope"}
	if err := Validate(p); err == nil || !strings.Contains(err.Error(), "base") {
		t.Errorf("err = %v, want a base error", err)
	}
	p.Base = "canary"
	if err := Validate(p); err != nil {
		t.Errorf("a well-formed base was refused: %v", err)
	}
}

// Builtins are roots: a base names a builtin, so a builtin with one is invalid.
func TestBuiltinsCarryNoBase(t *testing.T) {
	if _, err := parseBuiltins([]byte("[[profile]]\nname = \"x\"\nprovider = \"claude\"\nbackend = \"anthropic\"\nbase = \"y\"\n")); err == nil {
		t.Error("a builtin with a base parsed")
	}
	ps, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.Base != "" {
			t.Errorf("builtin %q has base %q", p.Name, p.Base)
		}
	}
}

// Save writes protocol 2 only when a profile uses a protocol-2 key, so an
// untouched registry stays readable by older clowns on other hosts.
func TestSaveStampsTheLowestProtocolThatHoldsTheFile(t *testing.T) {
	plain := Profile{Name: "a", Provider: "claude", Backend: "anthropic"}
	based := Profile{Name: "b", Provider: "claude", Backend: "anthropic", Base: "canary"}
	for name, c := range map[string]struct {
		ps   []Profile
		want int
	}{
		"no base":  {[]Profile{plain}, 1},
		"one base": {[]Profile{plain, based}, 2},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profiles.toml")
			if err := Save(path, c.ps, "1.2.3"); err != nil {
				t.Fatal(err)
			}
			reg, err := LoadRegistry(path)
			if err != nil {
				t.Fatal(err)
			}
			if reg.Stamp.Protocol != c.want {
				t.Errorf("stamp protocol = %d, want %d", reg.Stamp.Protocol, c.want)
			}
			if got := reg.MisstampedBase(); len(got) != 0 {
				t.Errorf("a file Save wrote reads as misstamped: %v", got)
			}
		})
	}
}

// A base under a stamp older than protocol 2 (or no stamp) means a writer that
// does not know what base means; readers refuse it.
func TestMisstampedBase(t *testing.T) {
	based := []Profile{{Name: "a"}, {Name: "b", Base: "canary"}}
	for stamp, want := range map[int][]string{0: {"b"}, 1: {"b"}, 2: nil} {
		reg := Registry{Profiles: based, Stamp: RegistryStamp{Protocol: stamp}}
		if got := reg.MisstampedBase(); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("stamp %d: MisstampedBase = %v, want %v", stamp, got, want)
		}
	}
}

func TestReadableProtocol(t *testing.T) {
	for p, want := range map[int]bool{-1: false, 0: false, 1: true, 2: true, 3: false} {
		if got := ReadableProtocol(p); got != want {
			t.Errorf("ReadableProtocol(%d) = %v, want %v", p, got, want)
		}
	}
}
