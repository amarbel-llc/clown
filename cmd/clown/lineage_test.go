package main

import (
	"slices"
	"strings"
	"testing"
)

// clown#219: the session-names journal records only ids a later launch can
// look up again, not a key freshly minted for one launch.

func TestResolveSessionIdentityReportsAProvidedKey(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "")
	t.Setenv("CLAUDE_SESSION_ID", "")
	if id := resolveSessionIdentity(); id.Provided {
		t.Errorf("a minted key (%q) is reported as provided", id.Key)
	}
	t.Setenv("CLOWN_SESSION_ID", "spinclass-key")
	if id := resolveSessionIdentity(); !id.Provided {
		t.Error("CLOWN_SESSION_ID is not reported as provided")
	}
	t.Setenv("CLOWN_SESSION_ID", "")
	t.Setenv("CLAUDE_SESSION_ID", "11111111-2222-4333-8444-555555555555")
	if id := resolveSessionIdentity(); !id.Provided {
		t.Error("CLAUDE_SESSION_ID is not reported as provided")
	}
}

func TestLineageRecordIDs(t *testing.T) {
	const minted, claudeID = "aaaaaaaa-0000-4000-8000-000000000001", "bbbbbbbb-0000-4000-8000-000000000002"
	cases := []struct {
		name     string
		provider string
		identity sessionIdentity
		hint     string
		want     []string
	}{
		// The growth #219 is about: a bare non-claude launch mints a key per
		// launch that nothing looks up.
		{"bare codex, minted key", "codex", sessionIdentity{Key: minted}, "", nil},
		// Non-claude lineage under spinclass does not regress.
		{"codex with CLOWN_SESSION_ID", "codex", sessionIdentity{Key: "spin-key", Provided: true}, "", []string{"spin-key"}},
		{"juggler with CLOWN_SESSION_ID", "juggler", sessionIdentity{Key: "spin-key", Provided: true}, "", []string{"spin-key"}},
		// claude persists the conversation under the id clown passes it.
		{"claude fresh launch, minted key injected as --session-id", "claude", sessionIdentity{Key: minted}, minted, []string{minted}},
		{"claude resume adopts the resumed id", "claude", sessionIdentity{Key: claudeID}, claudeID, []string{claudeID}},
		// clown resume ... -- -p: no claude id is known, the key is minted.
		{"claude -p, minted key", "claude", sessionIdentity{Key: minted}, "", nil},
		// The operator-key edge: the channel key is the operator's, claude
		// gets its own id; both are recorded.
		{"claude, operator key plus minted claude id", "claude", sessionIdentity{Key: "operator-key", Provided: true}, claudeID, []string{"operator-key", claudeID}},
		// A session UUID is written lower-cased, as the confinement gate
		// looks it up (clown#318); other keys keep their case.
		{"claude, uppercase resumed id", "claude", sessionIdentity{Key: "Operator-Key", Provided: true}, strings.ToUpper(claudeID), []string{"Operator-Key", claudeID}},
		{"claude, uppercase key adopted", "claude", sessionIdentity{Key: strings.ToUpper(claudeID)}, strings.ToUpper(claudeID), []string{claudeID}},
	}
	for _, c := range cases {
		if got := lineageRecordIDs(c.provider, c.identity, c.hint); !slices.Equal(got, c.want) {
			t.Errorf("%s: ids = %q, want %q", c.name, got, c.want)
		}
	}
}
