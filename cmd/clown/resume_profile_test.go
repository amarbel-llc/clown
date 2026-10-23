package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/profile"
	"code.linenisgreat.com/clown/internal/sessions"
	"code.linenisgreat.com/clown/pkg/profilelookup"
)

// FDR 0022 slice 1 (clown#295): `clown resume` restores the profile a
// session was launched with, and refuses rather than resume under a profile
// that changed since.
func TestResumeProfile(t *testing.T) {
	const builtinName = "canary-spinclass"
	absent := filepath.Join(t.TempDir(), "absent.toml")
	current, err := profilelookup.Resolve(builtinName, profilelookup.Options{RegistryPath: absent})
	if err != nil {
		t.Fatal(err)
	}
	stale := strings.Repeat("0", 64)

	userRegistry := func(t *testing.T, p profile.Profile) string {
		path := filepath.Join(t.TempDir(), "profiles.toml")
		if err := profile.Save(path, []profile.Profile{p}, "0.4.5"); err != nil {
			t.Fatal(err)
		}
		return path
	}
	skewedRegistry := func(t *testing.T) string {
		path := userRegistry(t, profile.Profile{Name: "mine", Provider: "claude", Backend: "anthropic"})
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append([]byte("from_a_newer_clown = true\n"), b...), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	cases := []struct {
		name     string
		record   *sessions.ProfilePin
		registry func(t *testing.T) string
		// unreadableSidecar puts a directory where the sidecar file belongs.
		unreadableSidecar bool
		want              string
		// refuse lists substrings the refusal must carry; nil means no error.
		refuse []string
	}{
		{
			name:     "no recorded profile launches without one",
			registry: func(*testing.T) string { return absent },
		},
		{
			name:     "unchanged digest restores the profile",
			record:   &sessions.ProfilePin{Name: builtinName, Digest: current.DigestSHA256},
			registry: func(*testing.T) string { return absent },
			want:     builtinName,
		},
		{
			name:     "changed digest refuses",
			record:   &sessions.ProfilePin{Name: builtinName, Digest: stale},
			registry: func(*testing.T) string { return absent },
			refuse:   []string{builtinName, stale, current.DigestSHA256},
		},
		{
			name:     "missing digest refuses",
			record:   &sessions.ProfilePin{Name: builtinName},
			registry: func(*testing.T) string { return absent },
			refuse:   []string{builtinName, current.DigestSHA256},
		},
		{
			name:     "profile no longer exists refuses",
			record:   &sessions.ProfilePin{Name: "gone", Digest: stale},
			registry: func(*testing.T) string { return absent },
			refuse:   []string{`"gone"`, stale, "profile not found"},
		},
		{
			name:     "skewed registry refuses",
			record:   &sessions.ProfilePin{Name: "mine", Digest: stale},
			registry: skewedRegistry,
			refuse:   []string{`"mine"`, stale, "from_a_newer_clown"},
		},
		{
			name:   "stale base pin refuses",
			record: &sessions.ProfilePin{Name: "mine", Digest: stale},
			registry: func(t *testing.T) string {
				return userRegistry(t, profile.Profile{Name: "mine", Provider: "claude", Backend: "anthropic", Base: builtinName + "@sha256:" + stale})
			},
			refuse: []string{`"mine"`, stale, "but the builtin now has"},
		},
		{
			name:     "profile that could not be pinned at launch refuses",
			record:   &sessions.ProfilePin{Name: builtinName, Digest: sessions.UnpinnableDigest},
			registry: func(*testing.T) string { return absent },
			refuse:   []string{builtinName, "could not be pinned", "clown profile edit"},
		},
		{
			name:              "unreadable sidecar refuses",
			registry:          func(*testing.T) string { return absent },
			unreadableSidecar: true,
			refuse:            []string{"session-names.jsonl"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			if tc.unreadableSidecar {
				if err := os.MkdirAll(filepath.Join(state, "clown", "session-names.jsonl"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			const id = "11111111-2222-3333-4444-555555555555"
			if tc.record != nil {
				if err := sessions.RecordSession(sessions.NameRecord{SessionID: id, Name: "bozo", Profile: tc.record.Name, ProfileDigest: tc.record.Digest}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := resumeProfile(id, profilelookup.Options{RegistryPath: tc.registry(t)})
			if tc.refuse == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if got != tc.want {
					t.Errorf("resumeProfile = %q, want %q", got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("resumeProfile = %q, nil; want a refusal (never a fallback to no profile)", got)
			}
			if got != "" {
				t.Errorf("refusal returned profile %q alongside the error", got)
			}
			for _, s := range tc.refuse {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("refusal %q does not mention %q", err, s)
				}
			}
		})
	}
}

// The pin a launch records is the profilelookup digest of the applied
// profile. A launch with no profile records none; one the library cannot
// vouch for still records its name, with sessions.UnpinnableDigest, so a
// later resume refuses instead of silently dropping the profile.
func TestLaunchProfilePin(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.toml")
	builtins, err := profile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	var canary *profile.Profile
	for i := range builtins {
		if builtins[i].Name == "canary-spinclass" {
			canary = &builtins[i]
		}
	}
	if canary == nil {
		t.Fatal("builtin canary-spinclass missing")
	}
	current, err := profilelookup.Resolve(canary.Name, profilelookup.Options{RegistryPath: absent})
	if err != nil {
		t.Fatal(err)
	}

	if pin, ok := launchProfilePin(nil, absent); ok {
		t.Errorf("no profile: got pin %+v", pin)
	}
	pin, ok := launchProfilePin(canary, absent)
	if !ok || pin != (sessions.ProfilePin{Name: canary.Name, Digest: current.DigestSHA256}) {
		t.Errorf("launchProfilePin = %+v, %v; want %s@%s", pin, ok, canary.Name, current.DigestSHA256)
	}

	unpinnable := func(p profile.Profile, registry string) {
		t.Helper()
		pin, ok := launchProfilePin(&p, registry)
		if want := (sessions.ProfilePin{Name: p.Name, Digest: sessions.UnpinnableDigest}); !ok || pin != want {
			t.Errorf("launchProfilePin(%s) = %+v, %v; want %+v", p.Name, pin, ok, want)
		}
	}
	unpinnable(profile.Profile{Name: "not-in-any-registry", Provider: "claude", Backend: "anthropic"}, absent)

	// A registry profilelookup refuses (*SkewError: an unknown key).
	mine := profile.Profile{Name: "mine", Provider: "claude", Backend: "anthropic"}
	skewed := filepath.Join(t.TempDir(), "profiles.toml")
	if err := profile.Save(skewed, []profile.Profile{mine}, "0.4.5"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(skewed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skewed, append([]byte("from_a_newer_clown = true\n"), b...), 0o644); err != nil {
		t.Fatal(err)
	}
	unpinnable(mine, skewed)
}
