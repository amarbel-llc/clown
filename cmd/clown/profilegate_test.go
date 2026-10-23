package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/sessions"
)

// FDR 0022 slice 2 (clown#295, decisions 2, 3, 8): checkLaunchProfile is
// the launch-time gate. clown only enforces equality with the digest the
// launcher pinned (CLOWN_PROFILE_DIGEST); it never verifies a grant.
func TestCheckLaunchProfile(t *testing.T) {
	const (
		id   = "11111111-2222-3333-4444-555555555555"
		dOld = "aaaa"
		dNew = "bbbb"
	)
	pin := func(name, digest string) *sessions.ProfilePin {
		return &sessions.ProfilePin{Name: name, Digest: digest}
	}
	recordedP1 := map[string]sessions.ProfilePin{id: {Name: "p1", Digest: dOld}}
	recordedUnpinnable := map[string]sessions.ProfilePin{id: {Name: "p1", Digest: sessions.UnpinnableDigest}}

	cases := []struct {
		name     string
		gate     profileGate
		recorded map[string]sessions.ProfilePin
		readErr  error
		approved bool
		// refuse lists substrings the refusal must carry; nil means allowed.
		refuse []string
	}{
		// Fresh launches: today's behavior unless a digest was pinned.
		{name: "fresh, no profile", gate: profileGate{}},
		{name: "fresh, profile, no digest", gate: profileGate{requested: "p1", profileEnvSet: true, launch: pin("p1", dNew)}},
		{
			name:     "fresh, digest matches",
			gate:     profileGate{requested: "p1", profileEnvSet: true, approvedDigest: dNew, launch: pin("p1", dNew)},
			approved: true,
		},
		{
			name:   "fresh, digest differs",
			gate:   profileGate{requested: "p1", profileEnvSet: true, approvedDigest: dOld, launch: pin("p1", dNew)},
			refuse: []string{`"p1"`, dOld, dNew, "CLOWN_PROFILE_DIGEST"},
		},
		{
			name:   "fresh, digest set but no profile applied",
			gate:   profileGate{approvedDigest: dOld},
			refuse: []string{"CLOWN_PROFILE_DIGEST", dOld, "no profile"},
		},
		{
			name:   "fresh, digest set but profile unpinnable",
			gate:   profileGate{approvedDigest: dOld, launch: pin("p1", sessions.UnpinnableDigest)},
			refuse: []string{`"p1"`, dOld, "cannot be pinned"},
		},

		// Resumes.
		{
			name:     "resume restoring the recorded pin",
			gate:     profileGate{resumeID: id, launch: pin("p1", dOld)},
			recorded: recordedP1,
		},
		{
			name:     "resume --profile equal to the pin is a no-op",
			gate:     profileGate{resumeID: id, requested: "p1", launch: pin("p1", dOld)},
			recorded: recordedP1,
		},
		{
			name:     "resume --profile to another profile needs approval",
			gate:     profileGate{resumeID: id, requested: "p2", launch: pin("p2", dNew)},
			recorded: recordedP1,
			refuse:   []string{id, `"p1"`, dOld, `"p2"`, dNew, "approval"},
		},
		{
			name:     "resume under a changed digest of the same name needs approval",
			gate:     profileGate{resumeID: id, launch: pin("p1", dNew)},
			recorded: recordedP1,
			refuse:   []string{`"p1"`, dOld, dNew},
		},
		{
			name:     "resume without the recorded profile refuses",
			gate:     profileGate{resumeID: id},
			recorded: recordedP1,
			refuse:   []string{`"p1"`, dOld, "without"},
		},
		{
			name:     "resume of an unpinnable record never matches without approval",
			gate:     profileGate{resumeID: id, launch: pin("p1", sessions.UnpinnableDigest)},
			recorded: recordedUnpinnable,
			refuse:   []string{`"p1"`},
		},
		{
			name:     "approved transition to another profile",
			gate:     profileGate{resumeID: id, requested: "p2", profileEnvSet: true, approvedDigest: dNew, launch: pin("p2", dNew)},
			recorded: recordedP1,
			approved: true,
		},
		{
			name:     "approved re-pin of a changed profile",
			gate:     profileGate{resumeID: id, approvedDigest: dNew, launch: pin("p1", dNew)},
			recorded: recordedP1,
			approved: true,
		},
		{
			name:     "approval must match the resolved digest",
			gate:     profileGate{resumeID: id, requested: "p1", profileEnvSet: true, approvedDigest: dOld, launch: pin("p1", dNew)},
			recorded: recordedP1,
			refuse:   []string{dOld, dNew},
		},
		{
			name:     "CLOWN_PROFILE without CLOWN_PROFILE_DIGEST on a resume that changes the pin refuses",
			gate:     profileGate{resumeID: id, requested: "p1", profileEnvSet: true, launch: pin("p1", dNew)},
			recorded: recordedP1,
			refuse:   []string{"CLOWN_PROFILE", "CLOWN_PROFILE_DIGEST", id},
		},
		{
			name:   "CLOWN_PROFILE without CLOWN_PROFILE_DIGEST on a resume of an unpinned session refuses",
			gate:   profileGate{resumeID: id, requested: "p1", profileEnvSet: true, launch: pin("p1", dNew)},
			refuse: []string{"CLOWN_PROFILE", "CLOWN_PROFILE_DIGEST", id},
		},
		{
			// The inner clown of an [attach] re-exec: the outer consumed (and
			// scrubbed) CLOWN_PROFILE_DIGEST after recording the approved pin,
			// while CLOWN_PROFILE is still inherited. Naming the pin is a no-op.
			name:     "CLOWN_PROFILE without CLOWN_PROFILE_DIGEST naming the pin is a no-op",
			gate:     profileGate{resumeID: id, requested: "p1", profileEnvSet: true, launch: pin("p1", dOld)},
			recorded: recordedP1,
		},
		{
			name:   "resume --profile for a session that recorded none needs approval",
			gate:   profileGate{resumeID: id, requested: "p2", launch: pin("p2", dNew)},
			refuse: []string{id, `"p2"`, "approval"},
		},
		{
			// A clownfile pin or build default (not an explicit request) on a
			// session that predates pinning keeps today's behavior.
			name: "resume of an unpinned session with a defaulted profile",
			gate: profileGate{resumeID: id, launch: pin("p2", dNew)},
		},
		{
			name:    "unreadable record refuses a resume",
			gate:    profileGate{resumeID: id, launch: pin("p1", dOld)},
			readErr: errors.New("sidecar is a directory"),
			refuse:  []string{id, "sidecar is a directory"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(sid string) (sessions.ProfilePin, bool, error) {
				if tc.readErr != nil {
					return sessions.ProfilePin{}, false, tc.readErr
				}
				p, ok := tc.recorded[sid]
				return p, ok, nil
			}
			approved, err := checkLaunchProfile(tc.gate, lookup)
			if tc.refuse == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if approved != tc.approved {
					t.Errorf("approved = %v, want %v", approved, tc.approved)
				}
				return
			}
			if err == nil {
				t.Fatal("allowed; want a refusal")
			}
			if approved {
				t.Error("a refusal must not report approval")
			}
			for _, s := range tc.refuse {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("refusal %q does not mention %q", err, s)
				}
			}
		})
	}
}

func fakeProfileLookup(recorded map[string]sessions.ProfilePin, readErr error) func(string) (sessions.ProfilePin, bool, error) {
	return func(id string) (sessions.ProfilePin, bool, error) {
		if readErr != nil {
			return sessions.ProfilePin{}, false, readErr
		}
		p, ok := recorded[id]
		return p, ok, nil
	}
}

// gatedSessionID picks the existing session a launch continues: a claude
// --resume/-r id, or a --session-id (user-passed, or injected by
// decideClaudeSession from CLOWN_SESSION_ID/CLAUDE_SESSION_ID) that already
// carries a pin, so `--session-id <victim>` cannot dodge the gate.
func TestGatedSessionID(t *testing.T) {
	const victim, fresh = "victim-id", "fresh-id"
	recorded := map[string]sessions.ProfilePin{victim: {Name: "p1", Digest: "aaaa"}}
	cases := []struct {
		name    string
		args    []string
		readErr error
		want    string
	}{
		{name: "no session flags", args: []string{"--model", "m"}},
		{name: "--resume", args: []string{"--resume", "r1"}, want: "r1"},
		{name: "-r", args: []string{"-r", "r1"}, want: "r1"},
		{name: "--resume=", args: []string{"--resume=r1"}, want: "r1"},
		{name: "--session-id of an unpinned id is a fresh launch", args: []string{"--session-id", fresh}},
		{name: "--session-id of a pinned session", args: []string{"--session-id", victim}, want: victim},
		{name: "--session-id with an unreadable record fails closed", args: []string{"--session-id", fresh}, readErr: errors.New("boom"), want: fresh},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gatedSessionID(tc.args, fakeProfileLookup(recorded, tc.readErr)); got != tc.want {
				t.Errorf("gatedSessionID(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// The `--session-id <victim>` attack end to end through the wiring: the
// gate refuses, and even the record a launch would append cannot carry a
// different pin for a pinned session without approval.
func TestSessionIDCannotOverwriteAPin(t *testing.T) {
	const victim = "victim-id"
	lookup := fakeProfileLookup(map[string]sessions.ProfilePin{victim: {Name: "strong", Digest: "aaaa"}}, nil)
	weak := &sessions.ProfilePin{Name: "weak", Digest: "bbbb"}

	g := profileGate{requested: "weak", launch: weak, resumeID: gatedSessionID([]string{"--session-id", victim}, lookup)}
	if _, err := checkLaunchProfile(g, lookup); err == nil {
		t.Error("checkLaunchProfile allowed --session-id <pinned session> under a different profile")
	}
	rec := launchRecordFor(sessions.NameRecord{SessionID: victim, Name: "bozo"}, weak, false, lookup)
	if rec.Profile != "" || rec.ProfileDigest != "" {
		t.Errorf("launchRecordFor wrote pin %s@%s over the victim's", rec.Profile, rec.ProfileDigest)
	}
}

// launchRecordFor is the record a launch appends: the launch pin only when
// it does not replace a different existing pin, unless approved.
func TestLaunchRecordFor(t *testing.T) {
	const id = "id-1"
	pinned := map[string]sessions.ProfilePin{id: {Name: "p1", Digest: "aaaa"}}
	same := &sessions.ProfilePin{Name: "p1", Digest: "aaaa"}
	other := &sessions.ProfilePin{Name: "p2", Digest: "bbbb"}
	cases := []struct {
		name     string
		launch   *sessions.ProfilePin
		approved bool
		recorded map[string]sessions.ProfilePin
		readErr  error
		want     *sessions.ProfilePin
	}{
		{name: "no profile applied", recorded: pinned},
		{name: "unpinned session gets the launch pin", launch: other, want: other},
		{name: "same pin is re-recorded", launch: same, recorded: pinned, want: same},
		{name: "different pin is dropped", launch: other, recorded: pinned},
		{name: "approved different pin is recorded", launch: other, approved: true, recorded: pinned, want: other},
		{name: "unreadable record drops the pin", launch: other, readErr: errors.New("boom")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := sessions.NameRecord{SessionID: id, Name: "bozo", Group: "r/w"}
			got := launchRecordFor(base, tc.launch, tc.approved, fakeProfileLookup(tc.recorded, tc.readErr))
			if got.SessionID != id || got.Name != "bozo" || got.Group != "r/w" {
				t.Errorf("base fields changed: %+v", got)
			}
			var want sessions.ProfilePin
			if tc.want != nil {
				want = *tc.want
			}
			if got.Profile != want.Name || got.ProfileDigest != want.Digest {
				t.Errorf("record pin = %s@%s, want %s@%s", got.Profile, got.ProfileDigest, want.Name, want.Digest)
			}
		})
	}
}

// After the gate, CLOWN_PROFILE_DIGEST is consumed: the provider, an
// [attach] re-exec child and nested clowns must not inherit an approval.
// CLOWN_PROFILE stays.
func TestConsumeApprovedDigest(t *testing.T) {
	t.Setenv("CLOWN_PROFILE_DIGEST", "aaaa")
	t.Setenv("CLOWN_PROFILE", "p1")
	if got := consumeApprovedDigest(); got != "aaaa" {
		t.Errorf("consumeApprovedDigest = %q, want aaaa", got)
	}
	if v, ok := os.LookupEnv("CLOWN_PROFILE_DIGEST"); ok {
		t.Errorf("CLOWN_PROFILE_DIGEST still set (%q) after consumption", v)
	}
	if got := os.Getenv("CLOWN_PROFILE"); got != "p1" {
		t.Errorf("CLOWN_PROFILE = %q, want p1 (must not be scrubbed)", got)
	}
}

func TestParseResumeArgsProfile(t *testing.T) {
	for _, args := range [][]string{
		{"--profile", "p1"},
		{"--profile=p1"},
		{"--profile", "p1", "clown://claude/11111111-2222-3333-4444-555555555555"},
		{"repo/wt", "--profile", "p1"},
	} {
		got, err := parseResumeArgs(args)
		if err != nil {
			t.Fatalf("parseResumeArgs(%q): %v", args, err)
		}
		if got.profile != "p1" {
			t.Errorf("parseResumeArgs(%q).profile = %q, want p1", args, got.profile)
		}
	}
	if _, err := parseResumeArgs([]string{"--profile"}); err == nil {
		t.Error("--profile without a value must be an error")
	}
}
