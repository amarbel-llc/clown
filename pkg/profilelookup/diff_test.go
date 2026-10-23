package profilelookup

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"code.linenisgreat.com/clown/internal/profile"
)

func mustResolved(t *testing.T, p profile.Profile) *Resolved {
	t.Helper()
	r, err := resolved(p, SourceUser, RegistryStamp{})
	if err != nil {
		t.Fatalf("resolved(%s): %v", p.Name, err)
	}
	return r
}

func baseProfile() profile.Profile {
	return profile.Profile{Name: "p1", Provider: "claude", Backend: "anthropic", Model: "m1"}
}

func checkLines(t *testing.T, lines []string) {
	t.Helper()
	for _, l := range lines {
		if n := utf8.RuneCountInString(l); n > 80 {
			t.Errorf("line has %d runes (> 80): %q", n, l)
		}
		for _, r := range l {
			if !unicode.IsPrint(r) {
				t.Errorf("line has non-printable rune %U: %q", r, l)
			}
		}
	}
}

// Golden table: one change per line, in the fixed field order.
func TestDiffGolden(t *testing.T) {
	withTokenEnvURL := baseProfile()
	withTokenEnvURL.Token = "tok-SECRET"
	withTokenEnvURL.URL = "https://gw.example/v1"
	withTokenEnvURL.Env = map[string]string{"KEEP": "same", "GONE": "v", "CHG": "old-SECRET"}
	withTokenEnvURL.ContextServers = []string{"plug/a", "plug/b"}
	withTokenEnvURL.ContextExcluded = map[string][]string{"plug/a": {"t1"}}

	changed := withTokenEnvURL
	changed.Name = "p2"
	changed.Provider = "opencode"
	changed.Backend = "local"
	changed.Model = "m2"
	changed.URL = "https://gw2.example/v1"
	changed.Token = ""
	changed.Env = map[string]string{"KEEP": "same", "CHG": "new-SECRET", "NEW": "x"}
	changed.ContextServers = []string{"plug/b", "plug/c"}
	changed.ContextExcluded = map[string][]string{"plug/a": {"t2"}}
	changed.Base = "claude-anthropic"

	cases := []struct {
		name     string
		old, new profile.Profile
		want     []string
	}{
		{name: "identical", old: baseProfile(), new: baseProfile()},
		{
			name: "model only",
			old:  baseProfile(),
			new:  func() profile.Profile { p := baseProfile(); p.Model = "m2"; return p }(),
			want: []string{`model: "m1" -> "m2"`},
		},
		{
			name: "every field",
			old:  withTokenEnvURL,
			new:  changed,
			want: []string{
				`name: "p1" -> "p2"`,
				`provider: "claude" -> "opencode"`,
				`backend: "anthropic" -> "local"`,
				`model: "m1" -> "m2"`,
				`url: "https://gw.example/v1" -> "https://gw2.example/v1"`,
				`token: set -> unset`,
				`env "CHG": changed`,
				`env "GONE": removed`,
				`env "NEW": added`,
				`context_servers "plug/a": removed`,
				`context_servers "plug/c": added`,
				`context_excluded "plug/a" "t1": removed`,
				`context_excluded "plug/a" "t2": added`,
				`base: "" -> "claude-anthropic"`,
			},
		},
		{
			name: "selection turned on",
			old:  baseProfile(),
			new:  func() profile.Profile { p := baseProfile(); p.ContextServers = []string{"plug/x"}; return p }(),
			want: []string{
				`context_servers: all kept -> selection`,
				`context_servers "plug/x": added`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, more := Diff(mustResolved(t, tc.old), mustResolved(t, tc.new), 100)
			if more != 0 {
				t.Errorf("more = %d, want 0", more)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("Diff =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
			checkLines(t, got)
			joined := strings.Join(got, "\n")
			if strings.Contains(joined, "SECRET") {
				t.Errorf("a token or env value leaked: %s", joined)
			}
		})
	}
}

// Env values and their digests are never printed.
func TestDiffNeverPrintsEnvDigests(t *testing.T) {
	old, new := baseProfile(), baseProfile()
	old.Env = map[string]string{"K": "a"}
	new.Env = map[string]string{"K": "b"}
	ro, rn := mustResolved(t, old), mustResolved(t, new)
	got, _ := Diff(ro, rn, 10)
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, ro.Env["K"]) || strings.Contains(joined, rn.Env["K"]) {
		t.Errorf("env digest printed: %s", joined)
	}
}

func TestDiffControlCharactersInModel(t *testing.T) {
	old, new := baseProfile(), baseProfile()
	new.Model = "evil\x1b[31m\nmodel\tname‮"
	got, _ := Diff(mustResolved(t, old), mustResolved(t, new), 10)
	if len(got) != 1 || !strings.HasPrefix(got[0], "model: ") {
		t.Fatalf("Diff = %q, want one model line", got)
	}
	checkLines(t, got)
}

func TestDiffLongURLIsTruncated(t *testing.T) {
	old, new := baseProfile(), baseProfile()
	new.URL = "https://gw.example/" + strings.Repeat("x", 200)
	got, _ := Diff(mustResolved(t, old), mustResolved(t, new), 10)
	if len(got) != 1 || !strings.Contains(got[0], "…") || !strings.HasPrefix(got[0], `url: "" -> "https://`) || !strings.HasSuffix(got[0], `xxx"`) {
		t.Fatalf("Diff = %q, want one url line elided in the middle of the value", got)
	}
	checkLines(t, got)
}

// Clipping never drops the direction: values are elided in the middle,
// each side of `old -> new` keeps its start and end, and the verb tail
// (added/removed/changed) always survives.
func TestDiffClipsSubjectsNotVerbs(t *testing.T) {
	long := func(prefix string) string { return prefix + strings.Repeat("y", 120) + "END" }

	old, new := baseProfile(), baseProfile()
	old.Model, new.Model = long("OLDMODEL-"), long("NEWMODEL-")
	old.Env = map[string]string{long("ENVKEY_"): "v"}
	new.ContextServers = []string{"plug/x"}
	new.ContextExcluded = map[string][]string{long("plug/server-"): {long("tool-")}}
	got, _ := Diff(mustResolved(t, old), mustResolved(t, new), 20)
	checkLines(t, got)

	find := func(prefix string) string {
		for _, l := range got {
			if strings.HasPrefix(l, prefix) {
				return l
			}
		}
		t.Fatalf("no line starting %q in %q", prefix, got)
		return ""
	}
	m := find("model: ")
	if !strings.Contains(m, `"OLDMODEL-`) || !strings.Contains(m, `"NEWMODEL-`) || !strings.Contains(m, " -> ") || strings.Count(m, `END"`) != 2 {
		t.Errorf("model line lost a side or its ends: %q", m)
	}
	e := find("env ")
	if !strings.HasSuffix(e, ": removed") || !strings.Contains(e, `"ENVKEY_`) || !strings.Contains(e, `END"`) {
		t.Errorf("env line lost its verb or key ends: %q", e)
	}
	x := find("context_excluded ")
	if !strings.HasSuffix(x, ": added") || !strings.Contains(x, `"plug/server-`) || !strings.Contains(x, `"tool-`) {
		t.Errorf("context_excluded line lost its verb or a subject: %q", x)
	}
}

// Invisible-but-printable runes would make two different values look the
// same to the approver, so every non-ASCII rune is escaped.
func TestDiffEscapesInvisibleRunes(t *testing.T) {
	for _, r := range []rune{0x2800, 0x3164, 0x115F, 0x1160, 0xFFA0, 0x034F, 0x17B4, 0x17B5, 0xFE00, 0xFE0F, 0xE0100, 0xE01EF, 0x0301, 0x20DD, 0x00A0, 0x200B} {
		old, new := baseProfile(), baseProfile()
		new.Model = "gpt-4" + string(r)
		got, _ := Diff(mustResolved(t, old), mustResolved(t, new), 10)
		if len(got) != 1 {
			t.Fatalf("%U: Diff = %q", r, got)
		}
		for _, c := range got[0] {
			if c > unicode.MaxASCII && c != '…' {
				t.Errorf("%U: line carries non-ASCII %U unescaped: %q", r, c, got[0])
			}
		}
		if !strings.Contains(got[0], `"gpt-4\u`) && !strings.Contains(got[0], `"gpt-4\U`) {
			t.Errorf("%U: rune not shown as an escape: %q", r, got[0])
		}
	}
}

// More URL shapes that can hide a credential.
func TestDiffRedactsOddURLs(t *testing.T) {
	for _, u := range []string{
		"https:user:hunter2@gw.example",      // opaque
		"gw.example/v1",                      // no host
		"https://gw.example/v1;key=hunter2",  // path parameters
		"https://gw.example/user@hunter2/v1", // @ in the path
	} {
		old, new := baseProfile(), baseProfile()
		new.URL = u
		got, _ := Diff(mustResolved(t, old), mustResolved(t, new), 10)
		if len(got) != 1 || !strings.Contains(got[0], RedactedURL) || strings.Contains(got[0], "hunter2") {
			t.Errorf("url %q: Diff = %q, want one redacted line", u, got)
		}
	}
}

// A URL that can carry a credential is never shown: userinfo, any query
// string, any fragment, or a URL that does not parse.
func TestDiffRedactsCredentialURLs(t *testing.T) {
	for _, u := range []string{
		"https://user:hunter2@gw.example/v1",
		"https://user@gw.example/v1",
		"https://gw.example/v1?api_key=hunter2",
		"https://gw.example/v1#hunter2",
		"http://[::1:hunter2",
	} {
		old, new := baseProfile(), baseProfile()
		new.URL = u
		got, _ := Diff(mustResolved(t, old), mustResolved(t, new), 10)
		joined := strings.Join(got, "\n")
		if len(got) != 1 || !strings.Contains(joined, "<redacted: may contain credentials>") || strings.Contains(joined, "hunter2") || strings.Contains(joined, "user") {
			t.Errorf("url %q: Diff = %q, want one redacted line", u, got)
		}
		checkLines(t, got)
	}
	// Both sides redacted but different: still reported, still redacted.
	old, new := baseProfile(), baseProfile()
	old.URL, new.URL = "https://a:b@gw.example", "https://c:d@gw.example"
	got, _ := Diff(mustResolved(t, old), mustResolved(t, new), 10)
	if len(got) != 1 || strings.Contains(got[0], "a:b") || strings.Contains(got[0], "c:d") {
		t.Errorf("Diff = %q, want one redacted change line", got)
	}
}

func TestDiffCap(t *testing.T) {
	old, new := baseProfile(), baseProfile()
	new.Env = map[string]string{}
	for i := range 20 {
		new.Env[fmt.Sprintf("K%02d", i)] = "v"
	}
	ro, rn := mustResolved(t, old), mustResolved(t, new)

	got, more := Diff(ro, rn, 6)
	if len(got) != 6 || more != 15 || got[5] != "+15 more changes" || got[0] != `env "K00": added` {
		t.Errorf("Diff(6) = %q, more %d; want 5 changes then +15 more changes", got, more)
	}
	got, more = Diff(ro, rn, 1)
	if !slices.Equal(got, []string{"+20 more changes"}) || more != 20 {
		t.Errorf("Diff(1) = %q, more %d", got, more)
	}
	got, more = Diff(ro, rn, 20)
	if len(got) != 20 || more != 0 {
		t.Errorf("Diff(20) = %d lines, more %d; want all 20, more 0", len(got), more)
	}
	for _, n := range []int{0, -3} {
		got, more = Diff(ro, rn, n)
		if got != nil || more != 20 {
			t.Errorf("Diff(%d) = %q, more %d; want nil, 20", n, got, more)
		}
	}
	if got, more := Diff(ro, ro, 0); got != nil || more != 0 {
		t.Errorf("identical Diff(0) = %q, %d", got, more)
	}
}

func TestDiffNil(t *testing.T) {
	p := baseProfile()
	p.Token = "x"
	r := mustResolved(t, p)
	got, _ := Diff(nil, r, 20)
	want := []string{
		`name: "p1" (added)`,
		`provider: "claude" (added)`,
		`backend: "anthropic" (added)`,
		`model: "m1" (added)`,
		`token: set (added)`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("Diff(nil, r) = %q, want %q", got, want)
	}
	got, _ = Diff(r, nil, 20)
	if len(got) != 5 || got[0] != `name: "p1" (removed)` {
		t.Errorf("Diff(r, nil) = %q", got)
	}
	if got, more := Diff(nil, nil, 20); got != nil || more != 0 {
		t.Errorf("Diff(nil, nil) = %q, %d", got, more)
	}
}

// Map iteration order never leaks into the output.
func TestDiffDeterministic(t *testing.T) {
	old, new := baseProfile(), baseProfile()
	old.Env, new.Env = map[string]string{}, map[string]string{}
	old.ContextExcluded, new.ContextExcluded = map[string][]string{}, map[string][]string{}
	for i := range 30 {
		k := fmt.Sprintf("K%d", i)
		if i%2 == 0 {
			old.Env[k] = "a"
		}
		if i%3 == 0 {
			new.Env[k] = "b"
		}
		new.ContextExcluded[fmt.Sprintf("p/s%d", i)] = []string{"z", "a", "m"}
	}
	new.ContextServers = []string{"p/s9", "p/s1", "p/s5"}
	first, _ := Diff(mustResolved(t, old), mustResolved(t, new), 1000)
	for range 50 {
		again, _ := Diff(mustResolved(t, old), mustResolved(t, new), 1000)
		if !slices.Equal(first, again) {
			t.Fatalf("Diff not deterministic:\n%q\n%q", first, again)
		}
	}
	checkLines(t, first)
}

// Adding Diff must not change the canonical form or the digest (AGENTS.md:
// that would need a ProtocolVersion bump).
func TestDiffDoesNotTouchCanonical(t *testing.T) {
	r := mustResolved(t, baseProfile())
	canon, digest := string(r.Canonical), r.DigestSHA256
	Diff(r, mustResolved(t, baseProfile()), 10)
	if string(r.Canonical) != canon || r.DigestSHA256 != digest {
		t.Error("Diff mutated its input")
	}
}
