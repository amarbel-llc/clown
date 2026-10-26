package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
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
		// approved: the launcher supplied CLOWN_PROFILE_DIGEST.
		approved bool
		want     string
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
			// The old definition is not recoverable (same name, new digest):
			// the refusal says so instead of inventing a diff.
			refuse: []string{builtinName, stale, current.DigestSHA256, "previous definition is not recorded"},
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
		// With launcher approval (CLOWN_PROFILE_DIGEST set) resume restores
		// the recorded name without comparing digests here: runWithFlags'
		// gate checks the resolved digest against the approved one instead.
		{
			name:     "approved launch restores a changed profile by name",
			record:   &sessions.ProfilePin{Name: builtinName, Digest: stale},
			registry: func(*testing.T) string { return absent },
			approved: true,
			want:     builtinName,
		},
		{
			name:     "approved launch restores an unpinnable profile by name",
			record:   &sessions.ProfilePin{Name: builtinName, Digest: sessions.UnpinnableDigest},
			registry: func(*testing.T) string { return absent },
			approved: true,
			want:     builtinName,
		},
		{
			name:              "approved launch still refuses an unreadable sidecar",
			registry:          func(*testing.T) string { return absent },
			unreadableSidecar: true,
			approved:          true,
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
			got, err := resumeProfile(id, tc.approved, profilelookup.Options{RegistryPath: tc.registry(t)})
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

// Decision 13: `clown resume` of a session with no pin applies the
// clownfile/build-default profile like a direct launch (and the gate then
// TOFU-pins it); a pinned or explicitly requested resume is unchanged.
func TestResumeLaunchFlags(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	cases := []struct {
		name               string
		args               resumeArgs
		requested          string
		recorded           string
		wantProfile        string
		wantFromRecord     bool
		wantDefaultProfile bool
	}{
		{name: "no pin, nothing requested: defaulting on", wantDefaultProfile: true},
		{name: "pinned: the recorded profile, no defaulting", recorded: "p1", wantProfile: "p1", wantFromRecord: true},
		{name: "explicit request: no defaulting", requested: "p2", wantProfile: "p2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := resumeLaunchFlags(id, tc.args, tc.requested, tc.recorded, "")
			if f.provider != "claude" || !f.providerExplicit {
				t.Errorf("provider %q explicit %v; want claude, explicit (no picker, no clownfile provider)", f.provider, f.providerExplicit)
			}
			if f.profile != tc.wantProfile || f.profileFromRecord != tc.wantFromRecord || f.resumeDefaultProfile != tc.wantDefaultProfile {
				t.Errorf("flags = profile %q fromRecord %v defaultProfile %v; want %q %v %v", f.profile, f.profileFromRecord, f.resumeDefaultProfile, tc.wantProfile, tc.wantFromRecord, tc.wantDefaultProfile)
			}
			if !slices.Equal(f.forwarded[len(f.forwarded)-2:], []string{"--resume", id}) {
				t.Errorf("forwarded = %q, want it to end in --resume %s", f.forwarded, id)
			}
		})
	}
}

// Through the gate: the defaulted profile of an unpinned resume is
// trust-on-first-use pinned under the resumed id; an explicit --profile on
// an unpinned session still needs approval.
func TestUnpinnedResumeThroughGate(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	none := fakeProfileLookup(nil, nil)

	f := resumeLaunchFlags(id, resumeArgs{}, "", "", "")
	flagProfile := f.profile // what runWithFlags captures before defaulting
	f.profile = launchProfileName(f, "cf-profile", "")
	launch := &sessions.ProfilePin{Name: "cf-profile", Digest: "aaaa"}
	g := newProfileGate(f, flagProfile, launcherProfileEnv{}, launch, false, none)
	d, err := checkLaunchProfile(g, none)
	if err != nil || g.resumeID != id || d.pinEarly == nil || *d.pinEarly != *launch {
		t.Errorf("defaulted unpinned resume: resumeID %q pinEarly %+v err %v; want a TOFU pin under %s", g.resumeID, d.pinEarly, err, id)
	}

	f2 := resumeLaunchFlags(id, resumeArgs{profile: "p2"}, "p2", "", "")
	g2 := newProfileGate(f2, f2.profile, launcherProfileEnv{}, &sessions.ProfilePin{Name: "p2", Digest: "bbbb"}, false, none)
	if _, err := checkLaunchProfile(g2, none); err == nil || !strings.Contains(err.Error(), "needs approval") {
		t.Errorf("explicit --profile on an unpinned session: err = %v, want approval needed", err)
	}
}

// The profile name a launch resolves: resumeDefaultProfile lets the
// clownfile pin and build default apply despite the explicit provider; it
// does not leak into other launches.
func TestLaunchProfileName(t *testing.T) {
	cases := []struct {
		name  string
		flags parsedFlags
		pin   string
		def   string
		want  string
	}{
		{name: "unpinned resume, clownfile pin", flags: parsedFlags{providerExplicit: true, resumeDefaultProfile: true}, pin: "cf", def: "bd", want: "cf"},
		{name: "unpinned resume, build default", flags: parsedFlags{providerExplicit: true, resumeDefaultProfile: true}, def: "bd", want: "bd"},
		{name: "unpinned resume, no default", flags: parsedFlags{providerExplicit: true, resumeDefaultProfile: true}},
		{name: "explicit --provider still suppresses defaulting", flags: parsedFlags{providerExplicit: true}, pin: "cf", def: "bd"},
		{name: "explicit profile wins", flags: parsedFlags{providerExplicit: true, resumeDefaultProfile: true, profile: "p"}, pin: "cf", want: "p"},
	}
	for _, tc := range cases {
		if got := launchProfileName(tc.flags, tc.pin, tc.def); got != tc.want {
			t.Errorf("%s: launchProfileName = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestUnpinnedResumeNotice(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	p := &profile.Profile{Name: "p1"}
	cases := []struct {
		name     string
		selected *profile.Profile
		pinned   bool
		skipped  string
		want     string
	}{
		{name: "default applied and pinned", selected: p, pinned: true, want: "session " + id + " has no recorded profile pin; resuming with profile p1 (now pinned)"},
		{name: "no default", want: "session " + id + " has no recorded profile pin; resuming without a profile"},
		{name: "default applied but not pinnable", selected: p, want: "session " + id + " has no recorded profile pin; resuming with profile p1 (not pinned)"},
		{name: "default not applied", skipped: "because", want: "session " + id + " has no recorded profile pin; resuming without a profile (because)"},
	}
	for _, tc := range cases {
		if got := unpinnedResumeNotice(id, tc.selected, tc.pinned, tc.skipped); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// Decision 13 applies a default profile only when it can be pinned to a
// claude session: not when the default's provider is not claude (it would
// switch the resumed claude session to another harness and skip the check),
// and not when the session id is not a UUID (claude reads it as a search
// term, and the check cannot gate it).
func TestResumeDefaultSkipReason(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	claude := &profile.Profile{Name: "c", Provider: "claude"}
	opencode := &profile.Profile{Name: "o", Provider: "opencode"}
	if r := resumeDefaultSkipReason(id, claude); r != "" {
		t.Errorf("claude default on a UUID session skipped: %q", r)
	}
	if r := resumeDefaultSkipReason(id, opencode); !strings.Contains(r, `default profile o uses provider opencode`) || !strings.Contains(r, "cannot resume a claude session") {
		t.Errorf("opencode default: reason %q", r)
	}
	if r := resumeDefaultSkipReason("legacy-session", claude); !strings.Contains(r, "not a UUID") {
		t.Errorf("non-UUID session: reason %q", r)
	}
}

// A truncated final sidecar line is reported as a warning naming the file,
// never as a failure; a whole sidecar says nothing.
func TestWarnTruncatedSidecar(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	path := filepath.Join(state, "clown", "session-names.jsonl")

	var quiet bytes.Buffer
	warnTruncatedSidecar(&quiet)
	if quiet.Len() != 0 {
		t.Errorf("missing sidecar warned: %q", quiet.String())
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"session_id":"id-1","na`), 0o644); err != nil {
		t.Fatal(err)
	}
	var w bytes.Buffer
	warnTruncatedSidecar(&w)
	if got := w.String(); !strings.Contains(got, "warning") || !strings.Contains(got, path) || strings.Count(got, "\n") != 1 {
		t.Errorf("warning = %q; want one line naming %s", got, path)
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

	if pin, form, ok := launchProfilePin(nil, absent); ok || form != nil {
		t.Errorf("no profile: got pin %+v, form %s", pin, form)
	}
	pin, form, ok := launchProfilePin(canary, absent)
	if !ok || pin != (sessions.ProfilePin{Name: canary.Name, Digest: current.DigestSHA256}) {
		t.Errorf("launchProfilePin = %+v, %v; want %s@%s", pin, ok, canary.Name, current.DigestSHA256)
	}
	// clown#298: the pin carries the redacted form of what it pins.
	if want, err := current.RedactedForm(); err != nil || string(form) != string(want) {
		t.Errorf("launchProfilePin form = %s (err %v); want %s", form, err, want)
	}

	unpinnable := func(p profile.Profile, registry string) {
		t.Helper()
		pin, form, ok := launchProfilePin(&p, registry)
		if want := (sessions.ProfilePin{Name: p.Name, Digest: sessions.UnpinnableDigest}); !ok || pin != want || form != nil {
			t.Errorf("launchProfilePin(%s) = %+v, %s, %v; want %+v and no form", p.Name, pin, form, ok, want)
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

// clown#298: a same-name edit no longer resolves to the pinned digest, but
// the pin stored the redacted form, so the refusal shows the field diff.
func TestResumeProfileShowsTheStoredFormDiff(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const id = "11111111-2222-3333-4444-555555555555"
	registry := filepath.Join(t.TempDir(), "profiles.toml")
	p := profile.Profile{Name: "e2e-pin", Provider: "claude", Backend: "anthropic", Model: "model-a"}
	if err := profile.Save(registry, []profile.Profile{p}, "0.4.5"); err != nil {
		t.Fatal(err)
	}
	pin, form, ok := launchProfilePin(&p, registry)
	if !ok || form == nil {
		t.Fatalf("launchProfilePin = %+v, %s, %v", pin, form, ok)
	}
	if err := sessions.RecordSession(sessions.NameRecord{SessionID: id, Profile: pin.Name, ProfileDigest: pin.Digest, ProfileForm: form}); err != nil {
		t.Fatal(err)
	}
	p.Model = "model-b"
	if err := profile.Save(registry, []profile.Profile{p}, "0.4.5"); err != nil {
		t.Fatal(err)
	}
	_, err := resumeProfile(id, false, profilelookup.Options{RegistryPath: registry})
	if err == nil || !strings.Contains(err.Error(), `model: "model-a" -> "model-b"`) || strings.Contains(err.Error(), "not recorded") {
		t.Fatalf("refusal = %v; want the stored-form field diff", err)
	}
}
