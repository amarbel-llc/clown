package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/profile"
	"code.linenisgreat.com/clown/internal/sessions"
	"code.linenisgreat.com/clown/pkg/profilelookup"
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
			// Decision 8 as amended: a user's own CLOWN_PROFILE export naming
			// the session's unchanged pin is a no-op.
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
	g := newProfileGate(outer, "", launcherProfileEnv{}, launch, false, fakeProfileLookup(recorded, nil))
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
	if _, err := checkLaunchProfile(newProfileGate(inner, inner.profile, launcherProfileEnv{}, launch, false, fakeProfileLookup(recorded, nil)), fakeProfileLookup(recorded, nil)); err == nil {
		t.Error("a non-inner launch requesting p1 for an unpinned session should need approval")
	}
	if _, err := checkLaunchProfile(newProfileGate(inner, inner.profile, launcherProfileEnv{}, launch, true, fakeProfileLookup(recorded, nil)), fakeProfileLookup(recorded, nil)); err != nil {
		t.Errorf("inner refused even without the early pin: %v", err)
	}

	// With the early pin recorded (the outer's write), the inner passes too.
	recorded[id] = *d.pinEarly
	if _, err := checkLaunchProfile(newProfileGate(inner, inner.profile, launcherProfileEnv{}, launch, true, fakeProfileLookup(recorded, nil)), fakeProfileLookup(recorded, nil)); err != nil {
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
		{name: "--resume", args: []string{"--resume", uuidR}, want: uuidR},
		{name: "-r", args: []string{"-r", uuidR}, want: uuidR},
		{name: "--resume=", args: []string{"--resume=" + uuidR}, want: uuidR},
		{name: "--resume with a search term is not a session", args: []string{"--resume", "fix tests"}},
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

// After parseFlags and launchResume have read them, CLOWN_PROFILE,
// CLOWN_PROFILE_DIGEST and CLOWN_PROFILE_SESSION are consumed: the
// provider, an [attach] re-exec child (which gets --profile in argv) and
// nested clowns must not inherit a profile request or an approval.
func TestConsumeLauncherProfileEnv(t *testing.T) {
	t.Setenv("CLOWN_PROFILE_DIGEST", "aaaa")
	t.Setenv("CLOWN_PROFILE_SESSION", "id-1")
	t.Setenv("CLOWN_PROFILE", "p1")
	got := consumeLauncherProfileEnv()
	if got != (launcherProfileEnv{profile: "p1", digest: "aaaa", session: "id-1"}) {
		t.Errorf("consumeLauncherProfileEnv = %+v", got)
	}
	for _, k := range []string{"CLOWN_PROFILE", "CLOWN_PROFILE_DIGEST", "CLOWN_PROFILE_SESSION"} {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s still set (%q) after consumption", k, v)
		}
	}
}

// How spinclass approves (it sets no CLOWN_PROFILE): `--profile X` as a
// flag plus CLOWN_PROFILE_DIGEST (bare lowercase hex) and
// CLOWN_PROFILE_SESSION. Through `clown resume <id> --profile X` and a
// direct `clown --profile X -- --resume <id>`, that is an approved
// transition (decision 3): the pin moves to X, for that session only.
func TestLauncherApprovesTransitionByFlag(t *testing.T) {
	t.Setenv("CLOWN_PROFILE", "")
	const id = "11111111-2222-3333-4444-555555555555"
	dNew := strings.Repeat("b", 64)
	recorded := fakeProfileLookup(map[string]sessions.ProfilePin{id: {Name: "p1", Digest: strings.Repeat("a", 64)}}, nil)
	launch := &sessions.ProfilePin{Name: "p2", Digest: dNew}
	env := launcherProfileEnv{digest: dNew, session: id}

	direct, err := parseFlags([]string{"--profile", "p2", "--", "--resume", id})
	if err != nil {
		t.Fatal(err)
	}
	direct.provider = "claude" // set by the profile's selection in runWithFlags
	for name, flags := range map[string]parsedFlags{
		// launchResume with args.profile = "p2"
		"clown resume <id> --profile p2":      {provider: "claude", providerExplicit: true, profile: "p2", forwarded: []string{"--resume", id}},
		"clown --profile p2 -- --resume <id>": direct,
	} {
		t.Run(name, func(t *testing.T) {
			g := newProfileGate(flags, flags.profile, env, launch, false, recorded)
			if g.requested != "p2" || g.profileEnvSet || g.resumeID != id {
				t.Fatalf("gate = %+v; want requested p2, no CLOWN_PROFILE, resumeID %s", g, id)
			}
			d, err := checkLaunchProfile(g, recorded)
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !d.approved || d.pinEarly == nil || *d.pinEarly != *launch {
				t.Errorf("decision = %+v; want approved with pinEarly %+v", d, launch)
			}
			if !d.approvesRecordFor(id) || d.approvesRecordFor("operator-key") {
				t.Errorf("approval scope wrong: covers %s=%v, other=%v", id, d.approvesRecordFor(id), d.approvesRecordFor("operator-key"))
			}
		})
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

// Slice 3a (operator answer to open question 1): resume forms whose session
// id clown cannot see are refused when a profile is applied.
const uuidR = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

// Two readings of a claude --resume/-r share one parser (resumeArgRaw).
// The strict one, used by the profile check, names a session only for a
// UUID value (claude's session ids are); anything else (no value, an empty
// value, a search term, a following flag) is an unseen form, refused when
// a profile applies. The lenient one (claudeResumeValue, isBareResume)
// keeps the pre-check behavior for unprofiled runs: any non-empty value,
// but never a following flag.
func TestResumeArgReaders(t *testing.T) {
	cases := []struct {
		args   []string
		id     string // claudeResumeID (strict)
		unseen string // unseenResumeForm (strict)
		value  string // claudeResumeValue (lenient)
		bare   bool   // isBareResume (lenient: no value)
	}{
		{args: nil},
		{args: []string{"--session-id", uuidR}},
		{args: []string{"-p", "hi"}},
		{args: []string{"--resume", uuidR}, id: uuidR, value: uuidR},
		{args: []string{"-r", uuidR}, id: uuidR, value: uuidR},
		{args: []string{"--resume=" + uuidR}, id: uuidR, value: uuidR},
		{args: []string{"-c"}, unseen: "-c"},
		{args: []string{"--continue"}, unseen: "--continue"},
		{args: []string{"--from-pr", "12"}, unseen: "--from-pr"},
		{args: []string{"--from-pr"}, unseen: "--from-pr"},
		{args: []string{"--resume"}, unseen: "--resume", bare: true},
		{args: []string{"-r"}, unseen: "-r", bare: true},
		{args: []string{"--resume="}, unseen: "--resume", bare: true},
		{args: []string{"--resume", ""}, unseen: "--resume", bare: true},
		{args: []string{"--resume", "fix tests"}, unseen: "--resume", value: "fix tests"},
		{args: []string{"-r", "foo"}, unseen: "-r", value: "foo"},
		{args: []string{"--resume=foo"}, unseen: "--resume", value: "foo"},
		{args: []string{"--resume", "--model", "m"}, unseen: "--resume", bare: true},
		{args: []string{"-r", "-p", "hi"}, unseen: "-r", bare: true},
		{args: []string{"--resume", "--session-id", uuidR}, unseen: "--resume", bare: true},
	}
	for _, tc := range cases {
		if got := claudeResumeID(tc.args); got != tc.id {
			t.Errorf("claudeResumeID(%q) = %q, want %q", tc.args, got, tc.id)
		}
		if got := unseenResumeForm(tc.args); got != tc.unseen {
			t.Errorf("unseenResumeForm(%q) = %q, want %q", tc.args, got, tc.unseen)
		}
		if got := claudeResumeValue(tc.args); got != tc.value {
			t.Errorf("claudeResumeValue(%q) = %q, want %q", tc.args, got, tc.value)
		}
		if got := isBareResume(tc.args); got != tc.bare {
			t.Errorf("isBareResume(%q) = %v, want %v", tc.args, got, tc.bare)
		}
	}
}

// claudeSessionRef is the session a claude launch names for the [attach]
// mode and the group-id command: an explicit --session-id, else the
// --resume value (lenient, as before the profile check: the bats fixtures'
// `--resume my-fixed-id` still selects the resume template). A flag after a
// bare --resume/--session-id is never taken as one.
func TestClaudeSessionRef(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{args: nil},
		{args: []string{"--resume", uuidR}, want: uuidR},
		{args: []string{"--resume", "my-fixed-id"}, want: "my-fixed-id"},
		{args: []string{"-r", "foo"}, want: "foo"},
		{args: []string{"--session-id", uuidR}, want: uuidR},
		{args: []string{"--resume", "--session-id", uuidR}, want: uuidR},
		{args: []string{"--resume", "--foo"}},
		{args: []string{"--resume", ""}},
		{args: []string{"--session-id", "--foo"}},
		{args: []string{"-c"}},
	}
	for _, tc := range cases {
		if got := claudeSessionRef(tc.args); got != tc.want {
			t.Errorf("claudeSessionRef(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// decideClaudeSession keeps its unprofiled behavior for a non-UUID
// --resume value (adopts it), skips a --resume with no value like
// --continue, and still adopts an explicit --session-id next to a bare
// --resume.
func TestDecideClaudeSessionResumeValues(t *testing.T) {
	for _, args := range [][]string{
		{"--resume", "my-fixed-id"},
		{"-r", "foo"},
		{"--resume=foo"},
	} {
		want := claudeResumeValue(args)
		got, channelKey, hint := decideClaudeSession(args, "base")
		if !slices.Equal(got, args) || channelKey != want || hint != want {
			t.Errorf("decideClaudeSession(%q) = %q, %q, %q; want args unchanged and %q adopted", args, got, channelKey, hint, want)
		}
	}
	for _, args := range [][]string{
		{"--resume", ""},
		{"--resume="},
		{"--resume"},
	} {
		got, channelKey, hint := decideClaudeSession(args, "base")
		if !slices.Equal(got, args) || channelKey != "base" || hint != "" {
			t.Errorf("decideClaudeSession(%q) = %q, %q, %q; want args unchanged, base, no hint", args, got, channelKey, hint)
		}
	}
	args := []string{"--resume", "--session-id", uuidR}
	if _, channelKey, hint := decideClaudeSession(args, "base"); channelKey != uuidR || hint != uuidR {
		t.Errorf("decideClaudeSession(%q) = %q, %q; want the explicit --session-id adopted", args, channelKey, hint)
	}
}

// The fail-open the review found: a non-UUID --resume value must not pass
// the gate as a resume of a session named by that string, nor be
// trust-on-first-use pinned.
func TestNonUUIDResumeIsRefusedWithAProfile(t *testing.T) {
	t.Setenv("CLOWN_PROFILE", "")
	p1 := &sessions.ProfilePin{Name: "p1", Digest: "aaaa"}
	for _, fwd := range [][]string{{"-r", "foo"}, {"--resume", "fix tests"}, {"--resume", ""}, {"--resume="}, {"--resume=foo"}} {
		g := newProfileGate(parsedFlags{provider: "claude", forwarded: fwd}, "", launcherProfileEnv{}, p1, false, fakeProfileLookup(nil, nil))
		d, err := checkLaunchProfile(g, fakeProfileLookup(nil, nil))
		if err == nil || g.resumeID != "" || d.pinEarly != nil {
			t.Errorf("%q: resumeID %q, pinEarly %+v, err %v; want a refusal and no session", fwd, g.resumeID, d.pinEarly, err)
		}
	}
}

func TestCheckLaunchProfileRefusesUnseenResume(t *testing.T) {
	p1 := &sessions.ProfilePin{Name: "p1", Digest: "aaaa"}
	for _, form := range []string{"-c", "--continue", "--resume", "-r", "--from-pr"} {
		// Defaulted profile (clownfile pin / build default): the message also
		// says --provider claude turns defaulting off.
		_, err := checkLaunchProfile(profileGate{unseenResume: form, launch: p1}, fakeProfileLookup(nil, nil))
		if err == nil || !strings.Contains(err.Error(), form) || !strings.Contains(err.Error(), "clown resume <id>") || !strings.Contains(err.Error(), "--provider claude") {
			t.Errorf("%s with a defaulted profile: err = %v; want a refusal naming %s, clown resume <id> and --provider claude", form, err, form)
		}
		// Explicitly requested profile: --provider claude would not help.
		_, err = checkLaunchProfile(profileGate{unseenResume: form, requested: "p1", launch: p1}, fakeProfileLookup(nil, nil))
		if err == nil || strings.Contains(err.Error(), "--provider claude") {
			t.Errorf("%s with --profile: err = %v; want a refusal without the --provider hint", form, err)
		}
	}
	if _, err := checkLaunchProfile(profileGate{unseenResume: "-c"}, fakeProfileLookup(nil, nil)); err != nil {
		t.Errorf("-c without a profile refused: %v", err)
	}
}

// newProfileGate picks the unseen form up from the forwarded claude args.
func TestNewProfileGateUnseenResume(t *testing.T) {
	t.Setenv("CLOWN_PROFILE", "")
	g := newProfileGate(parsedFlags{provider: "claude", forwarded: []string{"--resume", "--model", "m"}}, "", launcherProfileEnv{}, nil, false, fakeProfileLookup(nil, nil))
	if g.unseenResume != "--resume" || g.resumeID != "" {
		t.Errorf("gate = unseen %q, resumeID %q; want --resume and no id (not --model)", g.unseenResume, g.resumeID)
	}
}

// decideClaudeSession must not adopt the flag after a bare --resume as a
// session id; it treats a bare --resume like --continue.
func TestDecideClaudeSessionBareResume(t *testing.T) {
	args := []string{"--resume", "--model", "m"}
	got, channelKey, hint := decideClaudeSession(args, "base")
	if !slices.Equal(got, args) || channelKey != "base" || hint != "" {
		t.Errorf("decideClaudeSession(%q) = %q, %q, %q; want args unchanged, base, no hint", args, got, channelKey, hint)
	}
}

// Slice 3b: a pin change refusal shows the field diff when the old
// definition is recoverable (the pinned name still resolves to the pinned
// digest), and says so when it is not, never inventing one.
func TestDescribeProfileChange(t *testing.T) {
	mk := func(name, model, display string) *profilelookup.Resolved {
		return resolvedForTest(t, profile.Profile{Name: name, Display: display, Provider: "claude", Backend: "anthropic", Model: model})
	}
	p1 := mk("p1", "m1", "")
	p1Edited := mk("p1", "m-edited", "")
	p2 := mk("p2", "m2", "")
	p1Display := mk("p1", "m1", "renamed")
	registry := func(rs ...*profilelookup.Resolved) func(string) (*profilelookup.Resolved, error) {
		return func(name string) (*profilelookup.Resolved, error) {
			for _, r := range rs {
				if r.Name == name {
					return r, nil
				}
			}
			return nil, profilelookup.ErrNotFound
		}
	}
	pin := func(r *profilelookup.Resolved) sessions.ProfilePin {
		return sessions.ProfilePin{Name: r.Name, Digest: r.DigestSHA256}
	}

	got := describeProfileChange(pin(p1), pin(p2), registry(p1, p2))
	if !strings.Contains(got, `name: "p1" -> "p2"`) || !strings.Contains(got, `model: "m1" -> "m2"`) {
		t.Errorf("recoverable old: %q, want the field diff", got)
	}
	got = describeProfileChange(pin(p1), pin(p1Edited), registry(p1Edited))
	if !strings.Contains(got, "not recorded") || strings.Contains(got, "model:") {
		t.Errorf("unrecoverable old: %q, want the not-recorded note and no diff", got)
	}
	got = describeProfileChange(pin(p1), pin(p2), registry(p1))
	if !strings.Contains(got, "could not be re-read") {
		t.Errorf("unreadable new: %q", got)
	}
	got = describeProfileChange(pin(p1), pin(p1Display), registry(p1Display, p1))
	if got == "" {
		t.Errorf("display-only change produced no text")
	}
	// At most profileDiffLines diff lines.
	many := profile.Profile{Name: "many", Provider: "claude", Backend: "anthropic", Env: map[string]string{}}
	for i := range 20 {
		many.Env[fmt.Sprintf("K%02d", i)] = "v"
	}
	rm := resolvedForTest(t, many)
	got = describeProfileChange(pin(p1), pin(rm), registry(p1, rm))
	if n := strings.Count(got, "\n  "); n != profileDiffLines {
		t.Errorf("%d diff lines, want %d: %q", n, profileDiffLines, got)
	}
}

// resolvedForTest resolves p through a throwaway registry holding only it.
func resolvedForTest(t *testing.T, p profile.Profile) *profilelookup.Resolved {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.toml")
	if err := profile.Save(path, []profile.Profile{p}, "0.4.5"); err != nil {
		t.Fatal(err)
	}
	r, err := profilelookup.Resolve(p.Name, profilelookup.Options{RegistryPath: path})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The gate appends the description to a pin-change refusal.
func TestCheckLaunchProfileDescribesChange(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	recorded := fakeProfileLookup(map[string]sessions.ProfilePin{id: {Name: "p1", Digest: "aaaa"}}, nil)
	g := profileGate{
		resumeID: id,
		launch:   &sessions.ProfilePin{Name: "p2", Digest: "bbbb"},
		describeChange: func(old, new sessions.ProfilePin) string {
			return fmt.Sprintf("DESCRIBED %s->%s", old.Name, new.Name)
		},
	}
	_, err := checkLaunchProfile(g, recorded)
	if err == nil || !strings.Contains(err.Error(), "DESCRIBED p1->p2") {
		t.Errorf("err = %v, want the change description", err)
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
