package main

import (
	"errors"
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
			name:     "CLOWN_PROFILE without CLOWN_PROFILE_DIGEST on a resume refuses",
			gate:     profileGate{resumeID: id, requested: "p1", profileEnvSet: true, launch: pin("p1", dOld)},
			recorded: recordedP1,
			refuse:   []string{"CLOWN_PROFILE", "CLOWN_PROFILE_DIGEST", id},
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
