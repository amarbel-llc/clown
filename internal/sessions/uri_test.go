package sessions

import "testing"

func TestParseURI_Valid(t *testing.T) {
	provider, id, err := ParseURI("clown://claude/abc-123")
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	if provider != "claude" {
		t.Errorf("provider = %q, want claude", provider)
	}
	if id != "abc-123" {
		t.Errorf("id = %q, want abc-123", id)
	}
}

func TestParseURI_Rejects(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"missing scheme", "claude/abc-123"},
		{"wrong scheme", "http://claude/abc-123"},
		{"no provider", "clown:///abc-123"},
		{"no id", "clown://claude/"},
		{"no slash", "clown://claude"},
		{"id with slash", "clown://claude/abc/def"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ParseURI(tc.in); err == nil {
				t.Errorf("ParseURI(%q): expected error, got nil", tc.in)
			}
		})
	}
}

func TestParseURI_RoundTripWithSessionURI(t *testing.T) {
	s := Session{Provider: "claude", ID: "abc-123"}
	provider, id, err := ParseURI(s.URI())
	if err != nil {
		t.Fatalf("ParseURI(%q): %v", s.URI(), err)
	}
	if provider != s.Provider || id != s.ID {
		t.Errorf("round-trip: got (%q, %q), want (%q, %q)", provider, id, s.Provider, s.ID)
	}
}

func TestFindByID_Hit(t *testing.T) {
	ss := []Session{
		{ID: "a"},
		{ID: "b"},
		{ID: "c"},
	}
	got := FindByID(ss, "b")
	if got == nil {
		t.Fatal("FindByID returned nil for present id")
	}
	if got.ID != "b" {
		t.Errorf("got.ID = %q, want b", got.ID)
	}
}

func TestFindByID_Miss(t *testing.T) {
	ss := []Session{{ID: "a"}}
	if got := FindByID(ss, "nope"); got != nil {
		t.Errorf("FindByID returned %+v for absent id, want nil", got)
	}
}
