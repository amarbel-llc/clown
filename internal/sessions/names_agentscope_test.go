package sessions

import (
	"path/filepath"
	"testing"
)

// A confined launch's record carries its agent-scope spec, and the session
// stays recorded as confined: a later record without one does not clear it
// (clown#318).
func TestAgentScopeOf(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, ok, err := AgentScopeOf("id-1"); ok || err != nil {
		t.Fatalf("empty sidecar: ok %v err %v, want not recorded", ok, err)
	}
	// A record with only the spec (no name, no profile) is still written.
	if err := RecordSession(NameRecord{SessionID: "id-1", AgentScope: "/state/spec-a.json"}); err != nil {
		t.Fatal(err)
	}
	if err := RecordSession(NameRecord{SessionID: "id-2", Name: "other"}); err != nil {
		t.Fatal(err)
	}
	if err := RecordSession(NameRecord{SessionID: "id-1", Name: "bozo"}); err != nil {
		t.Fatal(err)
	}
	spec, ok, err := AgentScopeOf("id-1")
	if err != nil || !ok || spec != "/state/spec-a.json" {
		t.Fatalf("AgentScopeOf(id-1) = %q, %v, %v; want the recorded spec, kept across a later record without one", spec, ok, err)
	}
	if _, ok, err := AgentScopeOf("id-2"); ok || err != nil {
		t.Errorf("AgentScopeOf(id-2): ok %v err %v, want never confined", ok, err)
	}
	if err := RecordSession(NameRecord{SessionID: "id-1", Name: "bozo", AgentScope: "/state/spec-b.json"}); err != nil {
		t.Fatal(err)
	}
	if spec, _, _ := AgentScopeOf("id-1"); spec != "/state/spec-b.json" {
		t.Errorf("AgentScopeOf(id-1) = %q, want the last recorded spec", spec)
	}
}

// A later record of the same spec without a digest keeps the recorded
// digest; a record of another spec replaces both.
func TestAgentScopeRecordOfKeepsTheDigest(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, r := range []NameRecord{
		{SessionID: "id-1", AgentScope: "/s/a.json", AgentScopeDigest: "d1"},
		{SessionID: "id-1", Name: "bozo", AgentScope: "/s/a.json"},
	} {
		if err := RecordSession(r); err != nil {
			t.Fatal(err)
		}
	}
	if rec, ok, err := AgentScopeRecordOf("id-1"); err != nil || !ok || rec != (AgentScopeRecord{Spec: "/s/a.json", Digest: "d1"}) {
		t.Fatalf("record %+v, %v, %v; want the digest kept", rec, ok, err)
	}
	if err := RecordSession(NameRecord{SessionID: "id-1", AgentScope: "/s/b.json"}); err != nil {
		t.Fatal(err)
	}
	if rec, _, _ := AgentScopeRecordOf("id-1"); rec != (AgentScopeRecord{Spec: "/s/b.json"}) {
		t.Fatalf("record %+v; want the new spec with no digest", rec)
	}
}

// AnyAgentScope reports whether any session at all is recorded as confined:
// a resume by search term cannot be checked against one id.
func TestAnyAgentScope(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if got, err := AnyAgentScope(); got || err != nil {
		t.Fatalf("empty sidecar: %v, %v; want false", got, err)
	}
	if err := RecordSession(NameRecord{SessionID: "id-1", Name: "bozo"}); err != nil {
		t.Fatal(err)
	}
	if got, err := AnyAgentScope(); got || err != nil {
		t.Fatalf("no confined record: %v, %v; want false", got, err)
	}
	if err := RecordSession(NameRecord{SessionID: "id-2", AgentScope: "/s/spec.json"}); err != nil {
		t.Fatal(err)
	}
	if got, err := AnyAgentScope(); !got || err != nil {
		t.Fatalf("confined record: %v, %v; want true", got, err)
	}
}

// A torn line that may carry a spec counts as confined: fail closed.
func TestAnyAgentScopeFailsClosed(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	writeSidecar(t, filepath.Join(state, "clown", "session-names.jsonl"), `{"session_id":"id-1","agent_scope":"/s/sp`)
	if got, err := AnyAgentScope(); !got && err == nil {
		t.Fatal("a torn spec line read as nothing confined; want true or an error")
	}
}

// AgentScopeOf gates confinement on resume, so it fails closed: a torn
// line for the session that carries the spec key is an error, not "never
// confined".
func TestAgentScopeOfFailsClosed(t *testing.T) {
	good := `{"session_id":"id-1","name":"bozo","agent_scope":"/state/spec.json","ts":"2026-01-01T00:00:00Z"}`
	cases := map[string]struct {
		lines   []string
		wantErr bool
	}{
		"torn spec line for the session": {lines: []string{good, `{"session_id":"id-1","agent_scope":"/state/sp`}, wantErr: true},
		"only a torn spec line":          {lines: []string{`{"session_id":"id-1","agent_scope":"/state/sp`}, wantErr: true},
		"torn line for another session":  {lines: []string{good, `{"session_id":"id-2","agent_scope":"/state/sp`}},
		"torn line without the spec key": {lines: []string{good, `{"session_id":"id-1","name":"bo`}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			writeSidecar(t, filepath.Join(state, "clown", "session-names.jsonl"), tc.lines...)
			_, ok, err := AgentScopeOf("id-1")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && !ok {
				t.Error("the good record was not found")
			}
		})
	}
}
