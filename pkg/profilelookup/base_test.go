package profilelookup_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/profile"
	"code.linenisgreat.com/clown/pkg/profilelookup"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

const baseName = "canary-spinclass"

// child is a user profile declaring it narrows baseName.
func child(base string) profile.Profile {
	return profile.Profile{
		Name: "my-canary", Provider: "claude", Backend: "anthropic", Model: "claude-sonnet-5-5",
		ContextServers: []string{"clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe"},
		Env:            map[string]string{"CLOWN_DISABLE_CLAUDEAI_CONNECTORS": "1"},
		Base:           base,
	}
}

func resolveBuiltin(t *testing.T, name string) *profilelookup.Resolved {
	t.Helper()
	r, err := profilelookup.Resolve(name, profilelookup.Options{RegistryPath: filepath.Join(t.TempDir(), "absent.toml")})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResolveWithoutBaseHasNilBase(t *testing.T) {
	got, err := profilelookup.Resolve("trimmed", profilelookup.Options{RegistryPath: writeRegistry(t, "1.2.3", trimmed())})
	if err != nil {
		t.Fatal(err)
	}
	if got.Base != nil {
		t.Errorf("base = %#v, want nil", got.Base)
	}
}

// firm-mulberry's ask 1: the base is resolved exactly as a top-level Resolve
// of that builtin, so a launcher compares two values of one type.
func TestResolveBaseIsTheBuiltinAsResolvedAlone(t *testing.T) {
	want := resolveBuiltin(t, baseName)
	for _, base := range []string{baseName, baseName + "@sha256:" + want.DigestSHA256} {
		t.Run(base, func(t *testing.T) {
			got, err := profilelookup.Resolve("my-canary", profilelookup.Options{RegistryPath: writeRegistry(t, "1.2.3", child(base))})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Base, want) {
				t.Errorf("base =\n%#v\nwant\n%#v", got.Base, want)
			}
			if got.Base.Source != profilelookup.SourceBuiltin || got.Base.Env == nil || !got.Base.Selection.Selective {
				t.Errorf("base is not fully populated: %#v", got.Base)
			}
		})
	}
}

// firm-mulberry's ask 2: a user profile shadowing the base's name is not the
// base; the base stays the builtin.
func TestResolveBaseIgnoresAShadowingUserProfile(t *testing.T) {
	want := resolveBuiltin(t, baseName)
	shadow := profile.Profile{Name: baseName, Display: "shadow", Provider: "claude", Backend: "anthropic"}
	got, err := profilelookup.Resolve("my-canary", profilelookup.Options{RegistryPath: writeRegistry(t, "1.2.3", shadow, child(baseName))})
	if err != nil {
		t.Fatal(err)
	}
	if got.Base.Source != profilelookup.SourceBuiltin || got.Base.DigestSHA256 != want.DigestSHA256 || got.Base.Display == "shadow" {
		t.Errorf("base = %#v, want the builtin", got.Base)
	}
}

func TestResolveRefusesABaseItCannotVouchFor(t *testing.T) {
	current := resolveBuiltin(t, baseName).DigestSHA256
	stale := strings.Repeat("0", 64)
	for name, c := range map[string]struct {
		reg  []profile.Profile
		want string
	}{
		"unknown base":   {[]profile.Profile{child("no-such-builtin")}, `no builtin profile is named "no-such-builtin"`},
		"user-only base": {[]profile.Profile{{Name: "mine", Provider: "claude", Backend: "anthropic"}, child("mine")}, `no builtin profile is named "mine"`},
		"stale pin":      {[]profile.Profile{child(baseName + "@sha256:" + stale)}, "pins sha256 " + stale + ", but the builtin now has " + current},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := profilelookup.Resolve("my-canary", profilelookup.Options{RegistryPath: writeRegistry(t, "1.2.3", c.reg...)})
			var be *profilelookup.BaseError
			if !errors.As(err, &be) || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want a *BaseError saying %q", err, c.want)
			}
		})
	}
}

// A malformed pin never reaches resolution: Validate refuses it.
func TestResolveRefusesAMalformedPin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	body := "[registry]\nclown_version = \"1\"\nprotocol = \"2\"\n\n[[profile]]\nname = \"my-canary\"\nprovider = \"claude\"\nbackend = \"anthropic\"\nbase = \"canary@sha256:xyz\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := profilelookup.Resolve("my-canary", profilelookup.Options{RegistryPath: path}); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("err = %v, want the malformed pin refused", err)
	}
}

// clown#292: env values never reach a launcher; equal values give equal digests.
func TestResolveNeverExposesEnvValues(t *testing.T) {
	p := child(baseName)
	p.Env = map[string]string{"API_KEY": "sk-env-secret", "CLOWN_DISABLE_CLAUDEAI_CONNECTORS": "1"}
	got, err := profilelookup.Resolve("my-canary", profilelookup.Options{RegistryPath: writeRegistry(t, "1.2.3", p)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got.Canonical), "sk-env-secret") || strings.Contains(strings.Join(mapValues(got.Env), ""), "sk-env-secret") {
		t.Fatal("an env value leaked")
	}
	if got.Env["API_KEY"] != sha256Hex("sk-env-secret") {
		t.Errorf("env digest = %q", got.Env["API_KEY"])
	}
	if got.Env["CLOWN_DISABLE_CLAUDEAI_CONNECTORS"] != got.Base.Env["CLOWN_DISABLE_CLAUDEAI_CONNECTORS"] {
		t.Error("the same value in the profile and its base gave different digests")
	}
}

func mapValues(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// The coordinator's rule: a protocol-2 reader reads protocol-1 registries, and
// the digest does not depend on the registry's stamp.
func TestDigestIsIndependentOfTheRegistryStamp(t *testing.T) {
	v1 := writeRegistry(t, "1.2.3", trimmed())
	v2 := writeRegistry(t, "1.2.3", trimmed(), child(baseName))
	r1, err := profilelookup.Resolve("trimmed", profilelookup.Options{RegistryPath: v1})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := profilelookup.Resolve("trimmed", profilelookup.Options{RegistryPath: v2})
	if err != nil {
		t.Fatal(err)
	}
	if r1.Registry.ProtocolVersion != 1 || r2.Registry.ProtocolVersion != 2 {
		t.Fatalf("stamps = %d, %d; want 1, 2", r1.Registry.ProtocolVersion, r2.Registry.ProtocolVersion)
	}
	if r1.DigestSHA256 != r2.DigestSHA256 {
		t.Error("the same profile hashed differently under protocol-1 and protocol-2 registries")
	}
}

func TestSkewBaseUnderAProtocol1Stamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	body := "[registry]\nclown_version = \"0.4.4\"\nprotocol = \"1\"\n\n[[profile]]\nname = \"my-canary\"\nprovider = \"claude\"\nbackend = \"anthropic\"\nbase = \"canary\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := profilelookup.Resolve("my-canary", profilelookup.Options{RegistryPath: path})
	var skew *profilelookup.SkewError
	if !errors.As(err, &skew) || !reflect.DeepEqual(skew.MisstampedBase, []string{"my-canary"}) || !strings.Contains(err.Error(), "needs protocol 2") {
		t.Errorf("err = %v, want *SkewError naming my-canary", err)
	}
}

// Every profiles.toml field must reach the canonical form, or a change to it
// would not change the digest (audit 2026-10-22 L5). The token is the one
// field rendered as a flag instead (token_set).
func TestEveryProfileFieldIsInTheCanonicalForm(t *testing.T) {
	raw, err := profilelookup.Canonicalize(profile.Profile{})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	ty := reflect.TypeOf(profile.Profile{})
	for i := range ty.NumField() {
		tag, _, _ := strings.Cut(ty.Field(i).Tag.Get("toml"), ",")
		if tag == "token" {
			tag = "token_set"
		}
		if _, ok := keys[tag]; !ok {
			t.Errorf("profile field %s (toml %q) has no canonical key; add it and bump ProtocolVersion", ty.Field(i).Name, tag)
		}
	}
}

func TestSkewNewerProtocolStillRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	body := "[registry]\nclown_version = \"9.9.9\"\nprotocol = \"3\"\n\n[[profile]]\nname = \"a\"\nprovider = \"claude\"\nbackend = \"anthropic\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var skew *profilelookup.SkewError
	if _, err := profilelookup.Resolve("a", profilelookup.Options{RegistryPath: path}); !errors.As(err, &skew) || !strings.Contains(err.Error(), "protocol 3") {
		t.Errorf("err = %v, want *SkewError naming protocol 3", err)
	}
}
