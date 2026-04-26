package main

import (
	"reflect"
	"regexp"
	"testing"
)

func TestPrepareClaudeSessionID_InjectsForFreshSession(t *testing.T) {
	got, id := prepareClaudeSessionID(nil)
	if id == "" {
		t.Fatal("expected a generated id, got empty")
	}
	if !uuidRegexp.MatchString(id) {
		t.Errorf("id %q is not a UUIDv4", id)
	}
	want := []string{"--session-id", id}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("forwarded = %v, want %v", got, want)
	}
}

func TestPrepareClaudeSessionID_PreservesUserSessionID(t *testing.T) {
	args := []string{"--session-id", "abc-123", "--debug"}
	got, id := prepareClaudeSessionID(args)
	if id != "abc-123" {
		t.Errorf("id = %q, want abc-123", id)
	}
	if !reflect.DeepEqual(got, args) {
		t.Errorf("forwarded mutated unexpectedly: %v", got)
	}
}

func TestPrepareClaudeSessionID_PreservesUserSessionIDEqualsForm(t *testing.T) {
	args := []string{"--session-id=abc-123"}
	got, id := prepareClaudeSessionID(args)
	if id != "abc-123" {
		t.Errorf("id = %q, want abc-123", id)
	}
	if !reflect.DeepEqual(got, args) {
		t.Errorf("forwarded mutated unexpectedly: %v", got)
	}
}

func TestPrepareClaudeSessionID_PreservesResume(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"long form", []string{"--resume", "abc-123"}},
		{"short form", []string{"-r", "abc-123"}},
		{"equals form", []string{"--resume=abc-123"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, id := prepareClaudeSessionID(tc.args)
			if id != "abc-123" {
				t.Errorf("id = %q, want abc-123", id)
			}
			if !reflect.DeepEqual(got, tc.args) {
				t.Errorf("forwarded mutated unexpectedly: %v", got)
			}
		})
	}
}

func TestPrepareClaudeSessionID_SkipsForPrint(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"long form", []string{"--print", "hello"}},
		{"short form", []string{"-p", "hello"}},
		{"equals form", []string{"--print=true"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, id := prepareClaudeSessionID(tc.args)
			if id != "" {
				t.Errorf("id = %q, want empty (print mode skips hint)", id)
			}
			if !reflect.DeepEqual(got, tc.args) {
				t.Errorf("forwarded mutated in print mode: %v", got)
			}
		})
	}
}

func TestPrepareClaudeSessionID_SkipsForContinue(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"long form", []string{"--continue"}},
		{"short form", []string{"-c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, id := prepareClaudeSessionID(tc.args)
			if id != "" {
				t.Errorf("id = %q, want empty (continue mode skips hint)", id)
			}
		})
	}
}

func TestNewUUIDv4_Format(t *testing.T) {
	for i := 0; i < 16; i++ {
		got := newUUIDv4()
		if !uuidRegexp.MatchString(got) {
			t.Errorf("uuid #%d = %q does not match expected v4 format", i, got)
		}
	}
}

func TestNewUUIDv4_Unique(t *testing.T) {
	const n = 64
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		seen[newUUIDv4()] = struct{}{}
	}
	if len(seen) != n {
		t.Errorf("got %d unique UUIDs out of %d generations", len(seen), n)
	}
}

// uuidRegexp matches a canonical UUIDv4 string. The version nibble must
// be 4 and the variant nibble must be 8/9/a/b (RFC 4122).
var uuidRegexp = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
