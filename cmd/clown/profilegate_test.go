package main

import (
	"errors"
	"os"
	"slices"
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
	// CLOWN_PROFILE_SESSION (operator answer to open question 2) binds the
	// launcher's approval to one session.
	cases = append(cases, []struct {
		name     string
		gate     profileGate
		recorded map[string]sessions.ProfilePin
		readErr  error
		approved bool
		refuse   []string
	}{
		{
			name:     "approval bound to the resumed session",
			gate:     profileGate{resumeID: id, approvedDigest: dNew, approvedSession: id, launch: pin("p1", dNew)},
			recorded: recordedP1,
			approved: true,
		},
		{
			name:     "approval bound to another session refuses",
			gate:     profileGate{resumeID: id, approvedDigest: dNew, approvedSession: "other-id", launch: pin("p1", dNew)},
			recorded: recordedP1,
			refuse:   []string{"CLOWN_PROFILE_SESSION", "other-id", id},
		},
		{
			name:   "approval bound to a session on a fresh launch refuses",
			gate:   profileGate{approvedDigest: dNew, approvedSession: id, launch: pin("p1", dNew)},
			refuse: []string{"CLOWN_PROFILE_SESSION", id},
		},
		{
			name:     "session binding without a digest still has to match",
			gate:     profileGate{resumeID: id, approvedSession: "other-id", launch: pin("p1", dOld)},
			recorded: recordedP1,
			refuse:   []string{"CLOWN_PROFILE_SESSION", "other-id"},
		},
		{
			name:     "session binding without a digest, matching, unchanged pin",
			gate:     profileGate{resumeID: id, approvedSession: id, launch: pin("p1", dOld)},
			recorded: recordedP1,
		},
	}...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(sid string) (sessions.ProfilePin, bool, error) {
				if tc.readErr != nil {
					return sessions.ProfilePin{}, false, tc.readErr
				}
				p, ok := tc.recorded[sid]
				return p, ok, nil
			}
			d, err := checkLaunchProfile(tc.gate, lookup)
			if tc.refuse == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if d.approved != tc.approved {
					t.Errorf("approved = %v, want %v", d.approved, tc.approved)
				}
				return
			}
			if err == nil {
				t.Fatal("allowed; want a refusal")
			}
			if d.approved || d.pinEarly != nil {
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

// pinEarly is the pin the outer clown records for the resumed session
// before any [attach] re-exec: an approved pin, or a trust-on-first-use pin
// for a session with none, so the inner clown finds the session pinned to
// the profile it is about to apply.
func TestCheckLaunchProfilePinEarly(t *testing.T) {
	const id = "id-1"
	p1 := &sessions.ProfilePin{Name: "p1", Digest: "aaaa"}
	unpinnable := &sessions.ProfilePin{Name: "p1", Digest: sessions.UnpinnableDigest}
	pinned := map[string]sessions.ProfilePin{id: *p1}
	cases := []struct {
		name     string
		gate     profileGate
		recorded map[string]sessions.ProfilePin
		want     *sessions.ProfilePin
	}{
		{name: "fresh launch", gate: profileGate{launch: p1}},
		{name: "approved fresh launch has no session to pin", gate: profileGate{approvedDigest: "aaaa", launch: p1}},
		{name: "approved resume", gate: profileGate{resumeID: id, approvedDigest: "aaaa", launch: p1}, recorded: pinned, want: p1},
		{name: "trust on first use", gate: profileGate{resumeID: id, launch: p1}, want: p1},
		{name: "trust on first use of an unpinnable profile is not pinned early", gate: profileGate{resumeID: id, launch: unpinnable}},
		{name: "already pinned", gate: profileGate{resumeID: id, launch: p1}, recorded: pinned},
		{name: "no profile, no pin", gate: profileGate{resumeID: id}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := checkLaunchProfile(tc.gate, fakeProfileLookup(tc.recorded, nil))
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if (d.pinEarly == nil) != (tc.want == nil) || (d.pinEarly != nil && *d.pinEarly != *tc.want) {
				t.Errorf("pinEarly = %+v, want %+v", d.pinEarly, tc.want)
			}
		})
	}
}

// clown#295 review: an [attach]-wrapped resume of a session that predates
// pinning, with a defaulted profile (clownfile pin or build default). The
// outer passes the gate as trust on first use; its re-exec argv carries the
// resolved name as --profile. The inner must not refuse that as a request
// for an unpinned session.
func TestAttachReexecOfPrePinSession(t *testing.T) {
	t.Setenv("CLOWN_PROFILE", "")
	const id = "11111111-2222-3333-4444-555555555555"
	launch := &sessions.ProfilePin{Name: "p1", Digest: "aaaa"}
	recorded := map[string]sessions.ProfilePin{}

	// Outer: `clown resume <id>` of an unpinned session; p1 came from the
	// clownfile pin, so nothing was requested (flagProfile "").
	outer := parsedFlags{provider: "claude", providerExplicit: true, profile: "p1", forwarded: []string{"--resume", id}}
	g := newProfileGate(outer, "", "", "", launch, false, fakeProfileLookup(recorded, nil))
	d, err := checkLaunchProfile(g, fakeProfileLookup(recorded, nil))
	if err != nil {
		t.Fatalf("outer refused: %v", err)
	}
	if d.pinEarly == nil || *d.pinEarly != *launch {
		t.Fatalf("outer pinEarly = %+v, want %+v (recorded before the re-exec)", d.pinEarly, launch)
	}

	argv := outer.reexecArgv()
	if !slices.Contains(argv, "--profile") {
		t.Fatalf("reexecArgv %q carries no --profile; the inner would lose the profile", argv)
	}
	inner, err := parseFlags(argv)
	if err != nil {
		t.Fatalf("parseFlags(reexecArgv): %v", err)
	}

	// Without the early pin, an inner that read --profile as a request would
	// refuse (the regression); as the inner of a re-exec it does not.
	if _, err := checkLaunchProfile(newProfileGate(inner, inner.profile, "", "", launch, false, fakeProfileLookup(recorded, nil)), fakeProfileLookup(recorded, nil)); err == nil {
		t.Error("a non-inner launch requesting p1 for an unpinned session should need approval")
	}
	if _, err := checkLaunchProfile(newProfileGate(inner, inner.profile, "", "", launch, true, fakeProfileLookup(recorded, nil)), fakeProfileLookup(recorded, nil)); err != nil {
		t.Errorf("inner refused even without the early pin: %v", err)
	}

	// With the early pin recorded (the outer's write), the inner passes too.
	recorded[id] = *d.pinEarly
	if _, err := checkLaunchProfile(newProfileGate(inner, inner.profile, "", "", launch, true, fakeProfileLookup(recorded, nil)), fakeProfileLookup(recorded, nil)); err != nil {
		t.Errorf("inner refused after the early pin: %v", err)
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

// After the gate, CLOWN_PROFILE_DIGEST and CLOWN_PROFILE_SESSION are
// consumed: the provider, an [attach] re-exec child and nested clowns must
// not inherit an approval. CLOWN_PROFILE stays.
func TestConsumeApprovalEnv(t *testing.T) {
	t.Setenv("CLOWN_PROFILE_DIGEST", "aaaa")
	t.Setenv("CLOWN_PROFILE_SESSION", "id-1")
	t.Setenv("CLOWN_PROFILE", "p1")
	digest, session := consumeApprovalEnv()
	if digest != "aaaa" || session != "id-1" {
		t.Errorf("consumeApprovalEnv = %q, %q; want aaaa, id-1", digest, session)
	}
	for _, k := range []string{"CLOWN_PROFILE_DIGEST", "CLOWN_PROFILE_SESSION"} {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s still set (%q) after consumption", k, v)
		}
	}
	if got := os.Getenv("CLOWN_PROFILE"); got != "p1" {
		t.Errorf("CLOWN_PROFILE = %q, want p1 (must not be scrubbed)", got)
	}
}

// A session-bound approval covers only that session's records: the other
// id a launch writes (identity.Key when it differs from the claude id) goes
// through the ordinary pin comparison.
func TestProfileDecisionApprovesRecordFor(t *testing.T) {
	cases := []struct {
		name string
		d    profileDecision
		id   string
		want bool
	}{
		{name: "not approved", d: profileDecision{}, id: "a"},
		{name: "unbound approval covers any id", d: profileDecision{approved: true}, id: "a", want: true},
		{name: "bound approval covers its session", d: profileDecision{approved: true, approvedSession: "a"}, id: "a", want: true},
		{name: "bound approval does not cover another id", d: profileDecision{approved: true, approvedSession: "a"}, id: "b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.d.approvesRecordFor(tc.id); got != tc.want {
				t.Errorf("approvesRecordFor(%q) = %v, want %v", tc.id, got, tc.want)
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
