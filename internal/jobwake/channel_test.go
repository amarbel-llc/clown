package jobwake

import (
	"errors"
	"testing"
	"time"
)

// ValidateChannelID accepts the lowercase-hex channel-id alphabet and rejects
// anything that could compose a traversal path or otherwise escape the alphabet.
func TestValidateChannelID(t *testing.T) {
	good := []string{
		ChannelID("k"),     // a real 32-hex channel id
		"00",               // short hex is allowed (future prefix mode)
		"0123456789abcdef", // 16 hex
	}
	for _, c := range good {
		if err := ValidateChannelID(c); err != nil {
			t.Errorf("ValidateChannelID(%q): want nil, got %v", c, err)
		}
	}
	bad := []string{
		"",          // empty
		"../etc",    // traversal
		"a/b",       // separator
		"ABCDEF",    // uppercase out of alphabet
		"deadbeefg", // non-hex char
		".",         // dot
	}
	for _, c := range bad {
		err := ValidateChannelID(c)
		if err == nil {
			t.Errorf("ValidateChannelID(%q): want error, got nil", c)
			continue
		}
		if !errors.Is(err, ErrInvalidChannelID) {
			t.Errorf("ValidateChannelID(%q): want ErrInvalidChannelID, got %v", c, err)
		}
	}
}

// ChannelForTarget hashes an explicit target key, and falls back to the resolved
// session key when target is empty — the same rule the producer surface applies.
func TestChannelForTarget(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "owner")
	if got := ChannelForTarget("other"); got != ChannelID("other") {
		t.Fatalf("ChannelForTarget(explicit): want %s, got %s", ChannelID("other"), got)
	}
	if got := ChannelForTarget(""); got != ChannelID("owner") {
		t.Fatalf("ChannelForTarget(empty): want current session %s, got %s", ChannelID("owner"), got)
	}
}

// StatusOfChannel reaches a job by its channel id, without needing the session
// key — the cross-session operator path. An invalid channel id is rejected.
func TestStatusOfChannel(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "owner")
	id, err := Start(StartOpts{Source: "moxy"})
	if err != nil {
		t.Fatal(err)
	}
	cid := ChannelID("owner")

	// From a *different* session, the channel id still reaches the job.
	t.Setenv("CLOWN_SESSION_ID", "operator")
	st, err := StatusOfChannel(cid, id, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "running" || st.Source != "moxy" {
		t.Fatalf("StatusOfChannel: got %+v", st)
	}

	if _, err := StatusOfChannel("../etc", id, 0, time.Now().UTC()); !errors.Is(err, ErrInvalidChannelID) {
		t.Fatalf("StatusOfChannel(bad cid): want ErrInvalidChannelID, got %v", err)
	}
}

// ResolveSpoolChannel returns the spool path for an explicit channel and
// validates both ids it composes the path from.
func TestResolveSpoolChannel(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "owner")
	id, _ := Start(StartOpts{Source: "moxy"})
	cid := ChannelID("owner")

	got, err := ResolveSpoolChannel(cid, id)
	if err != nil {
		t.Fatal(err)
	}
	if want := SpoolFile(cid, id); got != want {
		t.Fatalf("ResolveSpoolChannel: want %s, got %s", want, got)
	}
	if _, err := ResolveSpoolChannel("../etc", id); !errors.Is(err, ErrInvalidChannelID) {
		t.Fatalf("bad cid: want ErrInvalidChannelID, got %v", err)
	}
	if _, err := ResolveSpoolChannel(cid, "../passwd"); !errors.Is(err, ErrInvalidJobID) {
		t.Fatalf("bad job id: want ErrInvalidJobID, got %v", err)
	}
}

// DoneChannel writes the terminal record addressed by channel id, recovering the
// originating session key from the job's existing records so the record stays
// well-formed.
func TestDoneChannel(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "owner")
	id, _ := Start(StartOpts{Source: "moxy"})
	cid := ChannelID("owner")

	// Cancel from a different session, addressing only the channel id.
	t.Setenv("CLOWN_SESSION_ID", "operator")
	if err := DoneChannel(cid, id, TypeCancelled, "by operator", ""); err != nil {
		t.Fatal(err)
	}

	recs, err := ReadJob(cid, id)
	if err != nil {
		t.Fatal(err)
	}
	last := recs[len(recs)-1]
	if last.Type != TypeCancelled {
		t.Fatalf("terminal record type: want cancelled, got %q", last.Type)
	}
	if last.Session != "owner" {
		t.Fatalf("terminal record session: want recovered 'owner', got %q", last.Session)
	}

	// A second terminal append is rejected (already terminal).
	if err := DoneChannel(cid, id, TypeCancelled, "again", ""); err == nil {
		t.Fatal("DoneChannel on terminal job: want error, got nil")
	}
	if err := DoneChannel("../etc", id, TypeCancelled, "", ""); !errors.Is(err, ErrInvalidChannelID) {
		t.Fatalf("DoneChannel(bad cid): want ErrInvalidChannelID, got %v", err)
	}
}
