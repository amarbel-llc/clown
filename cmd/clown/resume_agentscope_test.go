package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/sessions"
)

// recordsOf is a fake AgentScopeOf over a fixed id -> spec map.
func recordsOf(m map[string]string) func(string) (string, bool, error) {
	return func(id string) (string, bool, error) {
		s, ok := m[id]
		return s, ok, nil
	}
}

func loadsAll(string) error { return nil }

// The agent-scope spec any launch of a session runs under (clown#318): a
// session recorded as confined runs confined under its recorded spec, or
// not at all.
func TestAgentScopeForLaunch(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	gone := func(path string) error {
		return errors.New("agent scope spec: open " + path + ": no such file or directory")
	}
	cases := []struct {
		name      string
		ids       []string
		given     string
		recorded  func(string) (string, bool, error)
		load      func(string) error
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
		{name: "record unreadable: refused", ids: []string{id}, recorded: func(string) (string, bool, error) { return "", false, errors.New("torn record") }, load: loadsAll, wantErr: "torn record"},
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
	if f.agentScopeSpec != "/s/spec.json" || !f.resumeCommand {
		t.Fatalf("agentScopeSpec %q, resumeCommand %v; want the spec and a resume", f.agentScopeSpec, f.resumeCommand)
	}
	argv := f.reexecArgv()
	if n := strings.Count(strings.Join(argv, "\x00"), "--agent-scope"); n != 1 {
		t.Fatalf("reexecArgv() = %q, want --agent-scope exactly once", argv)
	}
	if back, err := parseFlags(argv); err != nil || back.agentScopeSpec != "/s/spec.json" {
		t.Fatalf("parseFlags(reexecArgv()) agentScopeSpec = %q, %v; want it kept", back.agentScopeSpec, err)
	}
}

// The shared check reads the record whatever the entrypoint: a direct
// --resume <id>, --session-id <id> or --naked launch of a confined session
// is checked (here the recorded spec is gone, so each is refused rather
// than run unconfined), and a confined launch that cannot know its claude
// session is refused (clown#318).
func TestApplyAgentScopeRecord(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	gone := filepath.Join(t.TempDir(), "gone.json")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := sessions.RecordSession(sessions.NameRecord{SessionID: id, AgentScope: gone}); err != nil {
		t.Fatal(err)
	}
	refused := map[string]parsedFlags{
		"direct --resume":     {provider: "claude", forwarded: []string{"--resume", id}, resumeHintID: id},
		"direct --session-id": {provider: "claude", forwarded: []string{"--session-id", id}, resumeHintID: id},
		"provided identity":   {provider: "claude", identity: sessionIdentity{Key: id, Provided: true}, forwarded: []string{"--session-id", id}, resumeHintID: id},
		"--naked --resume":    {provider: "claude", naked: true, forwarded: []string{"--resume", id}},
		"confined -c":         {provider: "claude", forwarded: []string{"-c"}, agentScopeSpec: gone},
		"confined -p, no id":  {provider: "claude", forwarded: []string{"-p", "hi"}, agentScopeSpec: gone},
		"confined picker":     {provider: "claude", forwarded: []string{"--resume"}, agentScopeSpec: gone},
	}
	for name, flags := range refused {
		t.Run(name, func(t *testing.T) {
			if code := applyAgentScopeRecord(&flags); code == 0 {
				t.Errorf("launch allowed with agentScopeSpec %q; want it refused", flags.agentScopeSpec)
			}
		})
	}
	other := parsedFlags{provider: "claude", forwarded: []string{"--session-id", "66666666-7777-8888-9999-000000000000"}, resumeHintID: "66666666-7777-8888-9999-000000000000"}
	if code := applyAgentScopeRecord(&other); code != 0 || other.agentScopeSpec != "" {
		t.Errorf("unrecorded session: code %d spec %q; want it to run unconfined", code, other.agentScopeSpec)
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
