package profilelookup_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/profile"
	"code.linenisgreat.com/clown/pkg/profilelookup"
)

// writeRegistry saves profiles through the real writer, so tests exercise the
// stamp clown itself leaves behind.
func writeRegistry(t *testing.T, version string, ps ...profile.Profile) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.toml")
	if err := profile.Save(path, ps, version); err != nil {
		t.Fatal(err)
	}
	return path
}

func trimmed() profile.Profile {
	return profile.Profile{
		Name: "trimmed", Display: "Trimmed", Provider: "claude", Backend: "anthropic",
		Model:          "opus",
		ContextServers: []string{"clown-builtin-jobs/troupe", "moxy/moxy", "clown-builtin-jobs/ringmaster", "moxy/moxy"},
		ContextExcluded: map[string][]string{
			"moxy/moxy": {"folio.write", "folio.read", "folio.read"},
		},
	}
}

func TestResolveUserProfile(t *testing.T) {
	path := writeRegistry(t, "1.2.3", trimmed())
	got, err := profilelookup.Resolve("trimmed", profilelookup.Options{RegistryPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != profilelookup.SourceUser || got.Provider != "claude" || got.Model != "opus" {
		t.Fatalf("resolved = %#v", got)
	}
	wantSel := profilelookup.Selection{
		Selective: true,
		Kept:      []string{"clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe", "moxy/moxy"},
		Excluded:  map[string][]string{"moxy/moxy": {"folio.read", "folio.write"}},
	}
	if !reflect.DeepEqual(got.Selection, wantSel) {
		t.Fatalf("selection = %#v, want %#v", got.Selection, wantSel)
	}
	// Save stamps protocol 1 when no profile uses base.
	if got.Registry != (profilelookup.RegistryStamp{Present: true, ClownVersion: "1.2.3", ProtocolVersion: 1}) {
		t.Fatalf("registry stamp = %#v", got.Registry)
	}
}

func TestResolveBuiltinWithoutUserRegistry(t *testing.T) {
	builtin, err := profile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "absent.toml")
	got, err := profilelookup.Resolve(builtin[0].Name, profilelookup.Options{RegistryPath: missing})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != profilelookup.SourceBuiltin || got.Registry.Present {
		t.Fatalf("resolved = %#v", got)
	}
	if got.Selection.Selective {
		t.Fatalf("builtin %q unexpectedly selective: %#v", got.Name, got.Selection)
	}
}

func TestResolveUserShadowsBuiltin(t *testing.T) {
	builtin, err := profile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	shadow := builtin[0]
	shadow.Display = "shadowed by user"
	path := writeRegistry(t, "1.2.3", shadow)
	got, err := profilelookup.Resolve(shadow.Name, profilelookup.Options{RegistryPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != profilelookup.SourceUser || got.Display != "shadowed by user" {
		t.Fatalf("resolved = %#v", got)
	}
}

// A registry repeating a builtin's name resolves as the binary's Merge does:
// the last entry wins.
func TestResolveDuplicateBuiltinNameMatchesMerge(t *testing.T) {
	builtin, err := profile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	first, second := builtin[0], builtin[0]
	first.Display, second.Display = "first", "second"
	path := writeRegistry(t, "1.2.3", first, second)
	got, err := profilelookup.Resolve(builtin[0].Name, profilelookup.Options{RegistryPath: path})
	if err != nil {
		t.Fatal(err)
	}
	want := profile.Merge(builtin, []profile.Profile{first, second})[0]
	if got.Display != want.Display || got.Display != "second" {
		t.Fatalf("display = %q, Merge picks %q", got.Display, want.Display)
	}
}

func TestResolveNotFound(t *testing.T) {
	path := writeRegistry(t, "1.2.3", trimmed())
	_, err := profilelookup.Resolve("nope", profilelookup.Options{RegistryPath: path})
	if !errors.Is(err, profilelookup.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "trimmed") {
		t.Errorf("error should list known profiles: %v", err)
	}
}

func TestResolveRejectsInvalidDefinition(t *testing.T) {
	path := writeRegistry(t, "1.2.3", profile.Profile{Name: "bad", Provider: "claude", Backend: "nonsense"})
	if _, err := profilelookup.Resolve("bad", profilelookup.Options{RegistryPath: path}); err == nil {
		t.Fatal("invalid profile resolved")
	}
}

func TestSelectionNilIsNotEmpty(t *testing.T) {
	plain := profile.Profile{Name: "plain", Provider: "claude", Backend: "anthropic"}
	path := writeRegistry(t, "1.2.3", plain, trimmed())
	opts := profilelookup.Options{RegistryPath: path}

	all, err := profilelookup.Resolve("plain", opts)
	if err != nil {
		t.Fatal(err)
	}
	if all.Selection.Selective || all.Selection.Kept != nil {
		t.Fatalf("no saved selection must read as keeps-everything: %#v", all.Selection)
	}
	if m := all.Selection.Missing(profilelookup.RequiredServers()); len(m) != 0 {
		t.Fatalf("keeps-everything misses %v", m)
	}
}

func TestSelectionMissingAndCrippled(t *testing.T) {
	req := profilelookup.RequiredServers()
	if len(req) != 2 {
		t.Fatalf("required = %v", req)
	}
	for _, r := range req {
		if !strings.HasPrefix(r, profilelookup.JobsPluginName+"/") {
			t.Errorf("required server %q is not in plugin %q", r, profilelookup.JobsPluginName)
		}
	}

	dropsTroupe := profilelookup.Selection{Selective: true, Kept: []string{req[0], "moxy/moxy"}}
	if got := dropsTroupe.Missing(req); !reflect.DeepEqual(got, []string{req[1]}) {
		t.Fatalf("missing = %v, want [%s]", got, req[1])
	}

	stripped := profilelookup.Selection{
		Selective: true,
		Kept:      req,
		Excluded:  map[string][]string{req[1]: {"chat_send"}, "moxy/moxy": {"x"}},
	}
	if got := stripped.Missing(req); len(got) != 0 {
		t.Fatalf("missing = %v", got)
	}
	if got := stripped.CrippledTools(req); !reflect.DeepEqual(got, map[string][]string{req[1]: {"chat_send"}}) {
		t.Fatalf("crippled = %v", got)
	}
}

func TestSkewUnstampedRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	legacy := "[[profile]]\nname = \"a\"\nprovider = \"claude\"\nbackend = \"anthropic\"\n"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := profilelookup.Resolve("a", profilelookup.Options{RegistryPath: path})
	var skew *profilelookup.SkewError
	if !errors.As(err, &skew) {
		t.Fatalf("err = %v, want *SkewError", err)
	}
	if skew.RegistryProtocol != 0 || !strings.Contains(skew.Error(), "predates version stamping") {
		t.Fatalf("skew = %#v / %v", skew, skew)
	}
}

func TestSkewOtherProtocolNamesBothSides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	body := "[registry]\nclown_version = \"7.7.7\"\nprotocol = \"99\"\n\n[[profile]]\nname = \"a\"\nprovider = \"claude\"\nbackend = \"anthropic\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := profilelookup.Resolve("a", profilelookup.Options{RegistryPath: path})
	var skew *profilelookup.SkewError
	if !errors.As(err, &skew) {
		t.Fatalf("err = %v, want *SkewError", err)
	}
	for _, want := range []string{"7.7.7", "protocol 99", "speaks profile protocol 2", "reads protocols 1 to 2"} {
		if !strings.Contains(skew.Error(), want) {
			t.Errorf("message %q lacks %q", skew.Error(), want)
		}
	}
}

func TestSkewUnknownFieldsUnderCurrentStamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	body := "[registry]\nclown_version = \"8.0.0\"\nprotocol = \"1\"\n\n[[profile]]\nname = \"a\"\nprovider = \"claude\"\nbackend = \"anthropic\"\nfrom_the_future = true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := profilelookup.Resolve("a", profilelookup.Options{RegistryPath: path})
	var skew *profilelookup.SkewError
	if !errors.As(err, &skew) || len(skew.UnknownFields) != 1 {
		t.Fatalf("err = %v, want *SkewError with one unknown field", err)
	}
}

// The canonical bytes are a wire contract: spinclass pins digests of them.
// Changing this literal means bumping ProtocolVersion.
func TestCanonicalGolden(t *testing.T) {
	p := trimmed()
	p.Token = "secret"
	p.URL = "https://x.example/a?b=1&c=<2>"
	p.Env = map[string]string{"ZED": "z", "ALPHA": "${HOME}"}
	got, err := profilelookup.Canonicalize(p)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"protocol":2,"name":"trimmed","display":"Trimmed","provider":"claude","backend":"anthropic","model":"opus","url":"https://x.example/a?b=1&c=<2>","token_set":true,"env":{"ALPHA":"` + sha256Hex("${HOME}") + `","ZED":"` + sha256Hex("z") + `"},"context_servers":["clown-builtin-jobs/ringmaster","clown-builtin-jobs/troupe","moxy/moxy"],"context_excluded":{"moxy/moxy":["folio.read","folio.write"]},"base":""}`
	if string(got) != want {
		t.Fatalf("canonical =\n%s\nwant\n%s", got, want)
	}
}

func TestCanonicalNilSelectionAndDefaults(t *testing.T) {
	got, err := profilelookup.Canonicalize(profile.Profile{Name: "p", Provider: "claude", Backend: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"protocol":2,"name":"p","display":"","provider":"claude","backend":"anthropic","model":"","url":"","token_set":false,"env":{},"context_servers":null,"context_excluded":{},"base":""}`
	if string(got) != want {
		t.Fatalf("canonical =\n%s\nwant\n%s", got, want)
	}
}

func TestDigestStableAcrossOrderAndTokenRotation(t *testing.T) {
	a := trimmed()
	a.Token = "one"
	b := trimmed()
	b.Token = "two"
	b.ContextServers = []string{"moxy/moxy", "clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe"}
	b.ContextExcluded = map[string][]string{"moxy/moxy": {"folio.read", "folio.write"}}

	pa := writeRegistry(t, "1", a)
	pb := writeRegistry(t, "2", b)
	ra, err := profilelookup.Resolve("trimmed", profilelookup.Options{RegistryPath: pa})
	if err != nil {
		t.Fatal(err)
	}
	rb, err := profilelookup.Resolve("trimmed", profilelookup.Options{RegistryPath: pb})
	if err != nil {
		t.Fatal(err)
	}
	if ra.DigestSHA256 != rb.DigestSHA256 {
		t.Fatalf("digest differs across element order, token rotation and writer version:\n%s\n%s", ra.Canonical, rb.Canonical)
	}

	b.Model = "sonnet"
	pc := writeRegistry(t, "2", b)
	rc, err := profilelookup.Resolve("trimmed", profilelookup.Options{RegistryPath: pc})
	if err != nil {
		t.Fatal(err)
	}
	if rc.DigestSHA256 == ra.DigestSHA256 {
		t.Fatal("digest did not change when the model changed")
	}
	if len(ra.DigestSHA256) != 64 {
		t.Fatalf("digest = %q", ra.DigestSHA256)
	}
}

func TestResolveNeverExposesToken(t *testing.T) {
	p := trimmed()
	p.Token = "sk-very-secret"
	path := writeRegistry(t, "1", p)
	got, err := profilelookup.Resolve("trimmed", profilelookup.Options{RegistryPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got.Canonical), "sk-very-secret") {
		t.Fatal("token leaked into canonical form")
	}
}
