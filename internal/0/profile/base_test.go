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
		"one moxy": {[]Profile{plain, based, {Name: "c", Provider: "claude", Backend: "anthropic", MoxyPlacement: "in-unit"}}, 3},
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
			if got := reg.MisstampedMoxy(); len(got) != 0 {
				t.Errorf("a file Save wrote reads as misstamped for moxy: %v", got)
			}
		})
	}
}

// A moxy table under a stamp older than protocol 3 (or no stamp) means a
// writer that does not know the table; readers refuse it.
func TestMisstampedMoxy(t *testing.T) {
	ps := []Profile{{Name: "a"}, {Name: "b", MoxyPlacement: "in-unit"}}
	for stamp, want := range map[int][]string{0: {"b"}, 1: {"b"}, 2: {"b"}, 3: nil} {
		reg := Registry{Profiles: ps, Stamp: RegistryStamp{Protocol: stamp}}
		if got := reg.MisstampedMoxy(); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("stamp %d: MisstampedMoxy = %v, want %v", stamp, got, want)
		}
	}
}

// The moxy table validates its placement, the tool-name shape and that the
// effective set only narrows the ceiling.
func TestValidateMoxyTable(t *testing.T) {
	base := Profile{Name: "w", Provider: "claude", Backend: "anthropic"}
	ok := base
	ok.SetMoxy(&Moxy{Placement: "in-unit", AllowTools: []string{"mcp__plugin_moxy_moxy__folio_read", "mcp__plugin_moxy_moxy__folio_write"}, Tools: []string{"mcp__plugin_moxy_moxy__folio_read"}})
	if err := Validate(ok); err != nil {
		t.Fatalf("a valid table was refused: %v", err)
	}
	if base.MoxyTable() != nil {
		t.Fatal("a profile without moxy keys reports a table")
	}
	for name, m := range map[string]Moxy{
		"unknown placement":       {Placement: "sidecar"},
		"lists without placement": {AllowTools: []string{"mcp__plugin_moxy_moxy__folio_read"}},
		"tool not in ceiling": {
			Placement: "in-unit", AllowTools: []string{"mcp__plugin_moxy_moxy__folio_read"},
			Tools: []string{"mcp__plugin_moxy_moxy__folio_write"},
		},
		"bare name":    {Placement: "in-unit", AllowTools: []string{"folio_read"}},
		"blank part":   {Placement: "in-unit", AllowTools: []string{"mcp__plugin_moxy_moxy__ x"}},
		"missing part": {Placement: "in-unit", AllowTools: []string{"mcp__plugin_moxy_moxy"}},
		"extra part":   {Placement: "in-unit", AllowTools: []string{"mcp__plugin__moxy__folio_read"}},
	} {
		p := base
		m := m
		p.SetMoxy(&m)
		if err := Validate(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Round trip through TOML (tommy on save, BurntSushi on load) keeps the
	// keys and stamps protocol 3.
	path := filepath.Join(t.TempDir(), "profiles.toml")
	if err := Save(path, []Profile{ok}, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	m := reg.Profiles[0].MoxyTable()
	if len(reg.Profiles) != 1 || m == nil || m.Placement != "in-unit" || len(m.AllowTools) != 2 || len(m.Tools) != 1 || reg.Stamp.Protocol != 3 {
		t.Fatalf("round trip = %+v (stamp %d)", reg.Profiles, reg.Stamp.Protocol)
	}
	// A second save over the written file (tommy's in-place edit) keeps them.
	if err := Save(path, reg.Profiles, "1.2.4"); err != nil {
		t.Fatal(err)
	}
	if again, err := LoadRegistry(path); err != nil || again.Profiles[0].MoxyTable() == nil || again.Stamp.Protocol != 3 {
		t.Fatalf("second save = %+v, %v", again, err)
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
	for p, want := range map[int]bool{-1: false, 0: false, 1: true, 2: true, 3: true, 4: false} {
		if got := ReadableProtocol(p); got != want {
			t.Errorf("ReadableProtocol(%d) = %v, want %v", p, got, want)
		}
	}
}
