package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/sessions"
)

// recordsOf is a fake AgentScopeRecordOf over a fixed id -> spec map, with
// no digests recorded.
func recordsOf(m map[string]string) func(string) (sessions.AgentScopeRecord, bool, error) {
	return func(id string) (sessions.AgentScopeRecord, bool, error) {
		s, ok := m[id]
		return sessions.AgentScopeRecord{Spec: s}, ok, nil
	}
}

func loadsAll(string) (string, error) { return "d0", nil }

// The agent-scope spec any launch of a session runs under (clown#318): a
// session recorded as confined runs confined under its recorded spec, or
// not at all.
func TestAgentScopeForLaunch(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	gone := func(path string) (string, error) {
		return "", errors.New("agent scope spec: open " + path + ": no such file or directory")
	}
	withDigest := func(spec, digest string) func(string) (sessions.AgentScopeRecord, bool, error) {
		return func(string) (sessions.AgentScopeRecord, bool, error) {
			return sessions.AgentScopeRecord{Spec: spec, Digest: digest}, true, nil
		}
	}
	cases := []struct {
		name      string
		ids       []string
		given     string
		recorded  func(string) (sessions.AgentScopeRecord, bool, error)
		load      func(string) (string, error)
		want      string
		wantFound bool
		wantErr   string
	}{
		{name: "never confined, nothing given: unconfined", ids: []string{id}, recorded: recordsOf(nil), load: loadsAll},
		{name: "never confined, spec given: that spec", ids: []string{id}, given: "/s/new.json", recorded: recordsOf(nil), load: loadsAll, want: "/s/new.json"},
		{name: "no ids: the given spec", given: "/s/new.json", recorded: recordsOf(map[string]string{id: "/s/spec.json"}), load: loadsAll, want: "/s/new.json"},
		{name: "recorded: re-applied", ids: []string{id}, recorded: recordsOf(map[string]string{id: "/s/spec.json"}), load: loadsAll, want: "/s/spec.json", wantFound: true},
		{name: "recorded under a second id", ids: []string{"other", id}, recorded: recordsOf(map[string]string{id: "/s/spec.json"}), load: loadsAll, want: "/s/spec.json", wantFound: true},
		{name: "recorded, same spec given", ids: []string{id}, given: "/s/spec.json", recorded: recordsOf(map[string]string{id: "/s/spec.json"}), load: loadsAll, want: "/s/spec.json", wantFound: true},
		{name: "recorded, other spec given: refused", ids: []string{id}, given: "/s/other.json", recorded: recordsOf(map[string]string{id: "/s/spec.json"}), load: loadsAll, wantErr: "/s/spec.json"},
		{name: "two ids recorded under different specs: refused", ids: []string{"a", "b"}, recorded: recordsOf(map[string]string{"a": "/s/1.json", "b": "/s/2.json"}), load: loadsAll, wantErr: "/s/2.json"},
		{name: "recorded, spec gone: refused", ids: []string{id}, recorded: recordsOf(map[string]string{id: "/s/spec.json"}), load: gone, wantErr: "refusing to run it unconfined"},
		{name: "recorded relative path: refused", ids: []string{id}, recorded: recordsOf(map[string]string{id: "spec.json"}), load: loadsAll, wantErr: "not absolute"},
		{name: "record unreadable: refused", ids: []string{id}, recorded: func(string) (sessions.AgentScopeRecord, bool, error) {
			return sessions.AgentScopeRecord{}, false, errors.New("torn record")
		}, load: loadsAll, wantErr: "torn record"},
		{name: "same digest: re-applied", ids: []string{id}, recorded: withDigest("/s/spec.json", "d0"), load: loadsAll, want: "/s/spec.json", wantFound: true},
		{name: "digest changed: refused", ids: []string{id}, recorded: withDigest("/s/spec.json", "d1"), load: loadsAll, wantErr: "changed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found, err := agentScopeForLaunch(tc.ids, tc.given, tc.recorded, tc.load)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one mentioning %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want || found != tc.wantFound {
				t.Fatalf("agentScopeForLaunch = %q, %v, %v; want %q, %v", got, found, err, tc.want, tc.wantFound)
			}
		})
	}
}

// Every id a launch could resume or record under is checked: the lineage,
// and an explicit --resume <id> or --session-id <id>, also under --naked,
// which skips decideClaudeSession.
func TestLaunchScopeIDs(t *testing.T) {
	const a, b = "11111111-2222-3333-4444-555555555555", "66666666-7777-8888-9999-000000000000"
	cases := map[string]struct {
		flags parsedFlags
		want  []string
	}{
		"direct --resume": {
			flags: parsedFlags{provider: "claude", forwarded: []string{"--resume", a}},
			want:  []string{a},
		},
		"direct --session-id": {
			flags: parsedFlags{provider: "claude", forwarded: []string{"--session-id", a}},
			want:  []string{a},
		},
		"provided identity and a hint": {
			flags: parsedFlags{provider: "claude", identity: sessionIdentity{Key: a, Provided: true}, resumeHintID: b, forwarded: []string{"--session-id", b}},
			want:  []string{a, b},
		},
		"naked --resume": {
			flags: parsedFlags{provider: "claude", naked: true, forwarded: []string{"--resume", a}},
			want:  []string{a},
		},
		"-p with --session-id": {
			flags: parsedFlags{provider: "claude", forwarded: []string{"-p", "--session-id", a, "hi"}},
			want:  []string{a},
		},
		"-c, no identity: none": {
			flags: parsedFlags{provider: "claude", identity: sessionIdentity{Key: a}, forwarded: []string{"-c"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := launchScopeIDs(tc.flags); !slices.Equal(got, tc.want) {
				t.Errorf("launchScopeIDs = %q, want %q", got, tc.want)
			}
		})
	}
}

// A confined launch must know the claude conversation id it will record,
// or it fails closed (-c, the bare --resume picker, -p without an id).
func TestConfinedConversationID(t *testing.T) {
	const a = "11111111-2222-3333-4444-555555555555"
	cases := map[string]struct {
		flags parsedFlags
		want  string
	}{
		"fresh launch (hint)":   {flags: parsedFlags{resumeHintID: a}, want: a},
		"-p with --session-id":  {flags: parsedFlags{forwarded: []string{"-p", "--session-id", a}}, want: a},
		"-p with --resume":      {flags: parsedFlags{forwarded: []string{"-p", "--resume", a}}, want: a},
		"-c":                    {flags: parsedFlags{forwarded: []string{"-c"}}},
		"bare --resume picker":  {flags: parsedFlags{forwarded: []string{"--resume"}}},
		"-p with no id":         {flags: parsedFlags{forwarded: []string{"-p", "hi"}}},
		"provided key, -p only": {flags: parsedFlags{identity: sessionIdentity{Key: a, Provided: true}, forwarded: []string{"-p"}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := confinedConversationID(tc.flags); got != tc.want {
				t.Errorf("confinedConversationID = %q, want %q", got, tc.want)
			}
		})
	}
}

// The spec is recorded as an absolute path with symlinks resolved.
func TestCanonicalSpecPath(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	if err := os.WriteFile(real, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	want, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{real, link, "real.json", "./link.json"} {
		got, err := canonicalSpecPath(p)
		if err != nil || got != want {
			t.Errorf("canonicalSpecPath(%q) = %q, %v; want %q", p, got, err, want)
		}
	}
	missing := filepath.Join(dir, "gone.json")
	if got, err := canonicalSpecPath("gone.json"); err != nil || got != missing {
		t.Errorf("canonicalSpecPath(missing) = %q, %v; want the absolute path %q", got, err, missing)
	}
}

// A confined launch refuses when a record cannot be written; an unconfined
// one only warns.
func TestRecordLaunchSessions(t *testing.T) {
	failing := func(sessions.NameRecord) error { return errors.New("disk full") }
	build := func(id string) sessions.NameRecord { return sessions.NameRecord{SessionID: id, Name: "bozo"} }
	var warn bytes.Buffer
	if err := recordLaunchSessions([]string{"a"}, false, build, failing, &warn); err != nil || !strings.Contains(warn.String(), "disk full") {
		t.Errorf("unconfined: err %v, warning %q; want a warning only", err, warn.String())
	}
	if err := recordLaunchSessions([]string{"a"}, true, build, failing, &warn); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("confined: err %v, want a refusal naming the cause", err)
	}
	var got []string
	ok := func(r sessions.NameRecord) error { got = append(got, r.SessionID); return nil }
	if err := recordLaunchSessions([]string{"a", "b"}, true, build, ok, &warn); err != nil || !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("recorded %q, %v; want both ids", got, err)
	}
}

// The resumed flags carry the spec given to `clown resume`, mark the launch
// as a resume (for the no-record notice), and the spec survives the
// [attach] re-exec exactly once.
func TestResumeLaunchFlagsCarryTheAgentScope(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	f := resumeLaunchFlags(id, resumeArgs{}, "", "", "/s/spec.json")
	if f.agentScopeSpec != "/s/spec.json" {
		t.Fatalf("agentScopeSpec %q; want the spec", f.agentScopeSpec)
	}
	argv := f.reexecArgv()
	if n := strings.Count(strings.Join(argv, "\x00"), "--agent-scope"); n != 1 {
		t.Fatalf("reexecArgv() = %q, want --agent-scope exactly once", argv)
	}
	if back, err := parseFlags(argv); err != nil || back.agentScopeSpec != "/s/spec.json" {
		t.Fatalf("parseFlags(reexecArgv()) agentScopeSpec = %q, %v; want it kept", back.agentScopeSpec, err)
	}
}

const (
	recordedID = "11111111-2222-3333-4444-555555555555"
	otherID    = "66666666-7777-8888-9999-000000000000"
)

// confinementFixture records recordedID as confined under a spec that is
// gone, so any launch the gate checks against it is refused rather than run
// unconfined, and returns that spec path.
func confinementFixture(t *testing.T) string {
	t.Helper()
	gone := filepath.Join(t.TempDir(), "gone.json")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := sessions.RecordSession(sessions.NameRecord{SessionID: recordedID, AgentScope: gone}); err != nil {
		t.Fatal(err)
	}
	return gone
}

// validSpec writes a spec agentscope.LoadSpec accepts and returns its path.
func validSpec(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.json")
	body := `{"version":1,"worktree":"` + filepath.Join(dir, "wt") + `","operator_home":"` + filepath.Join(dir, "home") + `"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The gate reads the record whatever the entrypoint and however the ids are
// spelled, and fails closed where it cannot know the conversation
// (clown#318).
func TestConfinementGateRefuses(t *testing.T) {
	gone := confinementFixture(t)
	refused := map[string]parsedFlags{
		"direct --resume":                   {provider: "claude", forwarded: []string{"--resume", recordedID}, resumeHintID: recordedID},
		"direct --session-id":               {provider: "claude", forwarded: []string{"--session-id", recordedID}, resumeHintID: recordedID},
		"provided identity":                 {provider: "claude", identity: sessionIdentity{Key: recordedID, Provided: true}, forwarded: []string{"--session-id", recordedID}, resumeHintID: recordedID},
		"--naked --resume":                  {provider: "claude", naked: true, forwarded: []string{"--resume", recordedID}},
		"recorded id in a second --resume":  {provider: "claude", forwarded: []string{"--resume", otherID, "-r", recordedID}, resumeHintID: otherID},
		"recorded id in --resume=":          {provider: "claude", forwarded: []string{"--resume=" + recordedID}, resumeHintID: recordedID},
		"disagreeing --resume values":       {provider: "claude", forwarded: []string{"--resume", otherID, "--resume", "77777777-7777-7777-7777-777777777777"}, resumeHintID: otherID},
		"user --resume before resume's":     {provider: "claude", forwarded: resumeLaunchFlags(recordedID, resumeArgs{forwarded: []string{"--resume", otherID}}, "", "", "").forwarded, resumeHintID: otherID},
		"disagreeing --session-id values":   {provider: "claude", forwarded: []string{"--session-id", otherID, "--session-id=" + recordedID}, resumeHintID: otherID},
		"search term while any is confined": {provider: "claude", forwarded: []string{"--resume", "my old chat"}, resumeHintID: "my old chat"},
		"fork of a confined session":        {provider: "claude", forwarded: []string{"--resume", recordedID, "--fork-session"}, resumeHintID: recordedID},
		"confined fork":                     {provider: "claude", forwarded: []string{"--resume", otherID, "--fork-session"}, resumeHintID: otherID, agentScopeSpec: gone},
		"confined --from-pr":                {provider: "claude", forwarded: []string{"--from-pr", "12"}, agentScopeSpec: gone},
		"--from-pr on a recorded identity":  {provider: "claude", identity: sessionIdentity{Key: recordedID, Provided: true}, forwarded: []string{"--from-pr=12"}},
		"confined -c":                       {provider: "claude", forwarded: []string{"-c"}, agentScopeSpec: gone},
		"confined -p, no id":                {provider: "claude", forwarded: []string{"-p", "hi"}, agentScopeSpec: gone},
		"confined picker":                   {provider: "claude", forwarded: []string{"--resume"}, agentScopeSpec: gone},
	}
	for name, flags := range refused {
		t.Run(name, func(t *testing.T) {
			if err := runConfinementGate(&flags, io.Discard); err == nil {
				t.Errorf("launch allowed with agentScopeSpec %q; want it refused", flags.agentScopeSpec)
			}
		})
	}
}

func TestConfinementGateAllows(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	allowed := map[string]parsedFlags{
		"unrecorded session":            {provider: "claude", forwarded: []string{"--session-id", otherID}, resumeHintID: otherID},
		"repeated, agreeing --resume":   {provider: "claude", forwarded: []string{"--resume", otherID, "--resume", otherID}, resumeHintID: otherID},
		"search term, nothing confined": {provider: "claude", forwarded: []string{"--resume", "my old chat"}, resumeHintID: "my old chat"},
		"fork, nothing confined":        {provider: "claude", forwarded: []string{"--resume", otherID, "--fork-session"}, resumeHintID: otherID},
	}
	for name, flags := range allowed {
		t.Run(name, func(t *testing.T) {
			if err := runConfinementGate(&flags, io.Discard); err != nil || flags.agentScopeSpec != "" {
				t.Errorf("err %v, spec %q; want an unconfined launch", err, flags.agentScopeSpec)
			}
		})
	}
}

// A direct --resume of a session with no record says it runs UNCONFINED,
// as `clown resume` does.
func TestConfinementGateUnconfinedNotice(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for name, flags := range map[string]parsedFlags{
		"clown resume":    {provider: "claude", forwarded: resumeLaunchFlags(otherID, resumeArgs{}, "", "", "").forwarded, resumeHintID: otherID},
		"direct --resume": {provider: "claude", forwarded: []string{"--resume", otherID}, resumeHintID: otherID},
	} {
		var w bytes.Buffer
		if err := runConfinementGate(&flags, &w); err != nil || !strings.Contains(w.String(), "UNCONFINED") {
			t.Errorf("%s: err %v, notice %q; want the UNCONFINED notice", name, err, w.String())
		}
	}
}

// The gate records a confined launch before it starts, under its
// conversation id, so a later launch finds it even if this one dies early.
func TestConfinementGateRecordsAConfinedLaunch(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	spec := validSpec(t)
	flags := parsedFlags{provider: "claude", forwarded: []string{"--session-id", otherID}, resumeHintID: otherID, agentScopeSpec: spec}
	if err := runConfinementGate(&flags, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, ok, err := sessions.AgentScopeOf(otherID)
	if err != nil || !ok || got != flags.agentScopeSpec {
		t.Fatalf("record %q, %v, %v; want %q", got, ok, err, flags.agentScopeSpec)
	}
	// And a later plain launch of that session is confined again.
	later := parsedFlags{provider: "claude", forwarded: []string{"--resume", otherID}, resumeHintID: otherID}
	if err := runConfinementGate(&later, io.Discard); err != nil || later.agentScopeSpec != flags.agentScopeSpec {
		t.Errorf("later launch: err %v, spec %q; want the recorded spec", err, later.agentScopeSpec)
	}
}

// The record carries the spec's digest, and a later launch whose spec bytes
// differ from the last launch's is refused: a changed spec means a new
// session (clown#318).
func TestConfinementGateRefusesAChangedSpec(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	spec := validSpec(t)
	first := parsedFlags{provider: "claude", forwarded: []string{"--session-id", otherID}, resumeHintID: otherID, agentScopeSpec: spec}
	if err := runConfinementGate(&first, io.Discard); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := sessions.AgentScopeRecordOf(otherID)
	if err != nil || !ok || rec.Digest == "" {
		t.Fatalf("record %+v, %v, %v; want one carrying a digest", rec, ok, err)
	}
	raw, err := os.ReadFile(spec)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(raw), `{"version":1`, `{"version":1,"binds_ro":["/nix/store"]`, 1)
	if err := os.WriteFile(spec, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	later := parsedFlags{provider: "claude", forwarded: []string{"--resume", otherID}, resumeHintID: otherID}
	if err := runConfinementGate(&later, io.Discard); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("later launch with a changed spec: err %v; want a refusal", err)
	}
}

// A spec the unit itself could rewrite (inside its worktree or a binds_rw
// entry) is refused for a session launch.
func TestConfinementGateRefusesAWritableSpec(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	spec := filepath.Join(dir, "wt", "spec.json")
	if err := os.MkdirAll(filepath.Dir(spec), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"worktree":"` + filepath.Join(dir, "wt") + `","operator_home":"` + filepath.Join(dir, "home") + `"}`
	if err := os.WriteFile(spec, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	flags := parsedFlags{provider: "claude", forwarded: []string{"--session-id", otherID}, resumeHintID: otherID, agentScopeSpec: spec}
	if err := runConfinementGate(&flags, io.Discard); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("spec inside its own worktree: err %v; want a refusal", err)
	}
}

// runWithFlags runs the gate: a recorded confined session whose spec is gone
// is refused by the real launch path. Deleting the call makes this launch
// (a --print-launch-plan, so nothing starts) succeed instead.
func TestRunWithFlagsRunsTheConfinementGate(t *testing.T) {
	confinementFixture(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	called := false
	orig := confinementGate
	confinementGate = func(f *parsedFlags, w io.Writer) error {
		called = true
		return orig(f, w)
	}
	t.Cleanup(func() { confinementGate = orig })
	flags := parsedFlags{provider: "claude", providerExplicit: true, printLaunchPlan: true, noAttach: true, forwarded: []string{"--resume", recordedID}}
	if code := runWithFlags(flags); code != 1 || !called {
		t.Fatalf("runWithFlags = %d, gate called %v; want the gate to refuse (1)", code, called)
	}
}

// The notice a resume with no confinement record prints.
func TestUnconfinedResumeNotice(t *testing.T) {
	n := unconfinedResumeNotice("abc")
	if !strings.Contains(n, "abc") || !strings.Contains(n, "UNCONFINED") {
		t.Errorf("notice %q, want the id and UNCONFINED", n)
	}
}

func TestParseResumeArgsAgentScope(t *testing.T) {
	for _, args := range [][]string{{"--agent-scope", "/s/spec.json"}, {"--agent-scope=/s/spec.json"}} {
		got, err := parseResumeArgs(args)
		if err != nil || got.agentScope != "/s/spec.json" {
			t.Errorf("parseResumeArgs(%q) = %+v, %v; want agentScope /s/spec.json", args, got, err)
		}
	}
	if _, err := parseResumeArgs([]string{"--agent-scope"}); err == nil {
		t.Error("--agent-scope without a value: want an error")
	}
}

// A confined launch records its spec under the session id.
func TestSessionRecordBaseRecordsTheAgentScope(t *testing.T) {
	rec := sessionRecordBase("id-1", parsedFlags{clownName: "bozo", agentScopeSpec: "/s/spec.json"})
	if rec.SessionID != "id-1" || rec.Name != "bozo" || rec.AgentScope != "/s/spec.json" {
		t.Errorf("record = %+v, want the agent scope recorded", rec)
	}
	if plain := sessionRecordBase("id-1", parsedFlags{clownName: "bozo"}); plain.AgentScope != "" {
		t.Errorf("unconfined record = %+v, want no agent scope", plain)
	}
}
