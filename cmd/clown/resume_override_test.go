package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/alfa/sessions"
	"code.linenisgreat.com/clown/pkg/profilelookup"
)

// clown#334 (FDR 0022 decision 2 as amended, operator 2026-11-05): `clown
// resume` may offer a confirm that re-pins a session whose pin no longer
// matches, at a terminal only, as a labelled speed bump.

const overrideTestID = "11111111-2222-3333-4444-555555555555"

// The two pin refusals are typed and name the digest this build pins the
// profile at now; a profile that cannot be resolved at all is a plain error.
func TestResumeProfileRefusalNamesTheDigestItWouldPinNow(t *testing.T) {
	const builtinName = "canary-spinclass"
	absent := filepath.Join(t.TempDir(), "absent.toml")
	current, err := profilelookup.Resolve(builtinName, profilelookup.Options{RegistryPath: absent})
	if err != nil {
		t.Fatal(err)
	}
	skewed := skewedProfilesRegistry
	stale := strings.Repeat("0", 64)
	cases := []struct {
		name     string
		record   sessions.ProfilePin
		registry func(*testing.T) string
		typed    bool
		current  string
	}{
		{"changed digest", sessions.ProfilePin{Name: builtinName, Digest: stale}, func(*testing.T) string { return absent }, true, current.DigestSHA256},
		{"unpinnable, registry now vouches", sessions.ProfilePin{Name: builtinName, Digest: sessions.UnpinnableDigest}, func(*testing.T) string { return absent }, true, current.DigestSHA256},
		{"unpinnable, registry still skewed", sessions.ProfilePin{Name: "mine", Digest: sessions.UnpinnableDigest}, skewed, true, ""},
		{"profile gone", sessions.ProfilePin{Name: "gone", Digest: stale}, func(*testing.T) string { return absent }, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			if err := sessions.RecordSession(sessions.NameRecord{SessionID: overrideTestID, Profile: tc.record.Name, ProfileDigest: tc.record.Digest}); err != nil {
				t.Fatal(err)
			}
			_, err := resumeProfile(overrideTestID, false, profilelookup.Options{RegistryPath: tc.registry(t)})
			if err == nil {
				t.Fatal("want a refusal")
			}
			var r *profileRefusal
			if typed := errors.As(err, &r); typed != tc.typed {
				t.Fatalf("typed = %v, want %v (%v)", typed, tc.typed, err)
			}
			if tc.typed && (r.current != tc.current || r.pin != tc.record || r.Error() != err.Error()) {
				t.Errorf("refusal = pin %+v current %q; want %+v %q", r.pin, r.current, tc.record, tc.current)
			}
		})
	}
}

// The confirm is offered only at a terminal, attended, without -y, without a
// launcher's digest, for a UUID session, and only when there is a digest to
// pin to.
func TestPinOverrideOffered(t *testing.T) {
	changed := &profileRefusal{pin: sessions.ProfilePin{Name: "p", Digest: "old"}, current: "new", text: "changed"}
	unpinnable := &profileRefusal{pin: sessions.ProfilePin{Name: "p", Digest: sessions.UnpinnableDigest}, text: "still unpinnable"}
	cases := map[string]struct {
		err                  error
		id                   string
		yes                  bool
		unattended, launcher string
		tty, want            bool
	}{
		"changed digest at a terminal":    {changed, overrideTestID, false, "", "", true, true},
		"-y":                              {changed, overrideTestID, true, "", "", true, false},
		"unattended":                      {changed, overrideTestID, false, "1", "", true, false},
		"misspelt marker counts as set":   {changed, overrideTestID, false, "yes", "", true, false},
		"launcher digest present":         {changed, overrideTestID, false, "", "abc", true, false},
		"not a terminal":                  {changed, overrideTestID, false, "", "", false, false},
		"session id not a UUID":           {changed, "legacy-session", false, "", "", true, false},
		"nothing to pin to":               {unpinnable, overrideTestID, false, "", "", true, false},
		"another error (unreadable file)": {errors.New("session-names.jsonl"), overrideTestID, false, "", "", true, false},
	}
	for name, c := range cases {
		r, ok := pinOverrideOffered(c.err, c.id, c.yes, c.unattended, c.launcher, c.tty)
		if ok != c.want || (ok && r != changed) {
			t.Errorf("%s: offered %v (%+v), want %v", name, ok, r, c.want)
		}
	}
}

// A confirmed re-pin goes through the unchanged gate as an approval of
// exactly that digest, bound to the resumed session, with the user-confirm
// source; any other digest is refused as a launcher's mismatch would be.
func TestUserConfirmedResumeIsAnApprovalBoundToTheSession(t *testing.T) {
	recorded := func(string) (sessions.ProfilePin, bool, error) {
		return sessions.ProfilePin{Name: "p", Digest: "d1"}, true, nil
	}
	flags := resumeLaunchFlags(overrideTestID, resumeArgs{}, "", "p", "")
	env := launcherProfileEnv{digest: "d2", session: overrideTestID, source: sessions.PinSourceUserConfirm}
	launch := &sessions.ProfilePin{Name: "p", Digest: "d2"}
	g := newProfileGate(flags, "", env, launch, false, recorded)
	d, err := checkLaunchProfile(g, recorded)
	if err != nil || !d.approved || d.source != sessions.PinSourceUserConfirm || d.approvedSession != overrideTestID || d.pinEarly == nil || *d.pinEarly != *launch {
		t.Fatalf("decision = %+v, err %v; want an approval of d2 bound to %s with source user-confirm", d, err, overrideTestID)
	}
	if !d.approvesRecordFor(overrideTestID) || d.approvesRecordFor("another-id") {
		t.Error("a user-confirmed approval must cover the resumed session only")
	}
	other := newProfileGate(flags, "", env, &sessions.ProfilePin{Name: "p", Digest: "d3"}, false, recorded)
	if _, err := checkLaunchProfile(other, recorded); err == nil || !strings.Contains(err.Error(), "d3") {
		t.Errorf("a profile resolving to another digest than the confirmed one: err = %v, want a refusal naming it", err)
	}
}

// The launcher's environment variables are the launcher source; without a
// digest there is no source.
func TestConsumeLauncherProfileEnvNamesTheLauncher(t *testing.T) {
	t.Setenv("CLOWN_PROFILE_DIGEST", "abc")
	t.Setenv("CLOWN_PROFILE_SESSION", overrideTestID)
	if env := consumeLauncherProfileEnv(); env.digest != "abc" || env.source != sessions.PinSourceLauncher {
		t.Errorf("env = %+v, want digest abc from the launcher", env)
	}
	t.Setenv("CLOWN_PROFILE_DIGEST", "")
	if env := consumeLauncherProfileEnv(); env.source != "" {
		t.Errorf("env without a digest has source %q", env.source)
	}
}

// A later refusal says who approved the pin it compares against.
func TestDescribePinProvenanceNamesTheApprover(t *testing.T) {
	base := sessions.PinRecord{ProfilePin: sessions.ProfilePin{Name: "p", Digest: "d"}, Writer: "0.4.5+abcdef0", Protocol: profilelookup.ProtocolVersion}
	for src, want := range map[string]string{
		"":                            "",
		sessions.PinSourceLauncher:    "profile_pin_source launcher",
		sessions.PinSourceUserConfirm: "profile_pin_source user-confirm",
	} {
		rec := base
		rec.Source = src
		got := describePinProvenance(rec, "0.4.5+abcdef0", profilelookup.ProtocolVersion)
		if !strings.Contains(got, "written by this same clown build") {
			t.Errorf("source %q: build line missing: %q", src, got)
		}
		if (want == "" && strings.Contains(got, "profile_pin_source")) || (want != "" && !strings.Contains(got, want)) {
			t.Errorf("source %q: %q, want it to mention %q", src, got, want)
		}
	}
}
