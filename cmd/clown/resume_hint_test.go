package main

import (
	"reflect"
	"regexp"
	"testing"
)

func TestDecideClaudeSession_InjectsForFreshSession(t *testing.T) {
	got, channelKey, id := decideClaudeSession(nil, "")
	if id == "" || !uuidRegexp.MatchString(id) {
		t.Fatalf("id = %q, want a minted UUIDv4", id)
	}
	// Empty baseKey: the minted id is both claude's --session-id and the channel
	// key, unifying resume id and the job-wakeup channel (RFC-0013 §2.1).
	if channelKey != id {
		t.Errorf("channelKey = %q, want the minted id %q", channelKey, id)
	}
	if want := []string{"--session-id", id}; !reflect.DeepEqual(got, want) {
		t.Errorf("forwarded = %v, want %v", got, want)
	}
}

func TestDecideClaudeSession_PreservesUserSessionID(t *testing.T) {
	args := []string{"--session-id", "abc-123", "--debug"}
	got, channelKey, id := decideClaudeSession(args, "")
	if id != "abc-123" || channelKey != "abc-123" {
		t.Errorf("(channelKey,id) = (%q,%q), want both abc-123", channelKey, id)
	}
	if !reflect.DeepEqual(got, args) {
		t.Errorf("forwarded mutated unexpectedly: %v", got)
	}
}

func TestDecideClaudeSession_PreservesUserSessionIDEqualsForm(t *testing.T) {
	args := []string{"--session-id=abc-123"}
	got, channelKey, id := decideClaudeSession(args, "")
	if id != "abc-123" || channelKey != "abc-123" {
		t.Errorf("(channelKey,id) = (%q,%q), want both abc-123", channelKey, id)
	}
	if !reflect.DeepEqual(got, args) {
		t.Errorf("forwarded mutated unexpectedly: %v", got)
	}
}

func TestDecideClaudeSession_PreservesResume(t *testing.T) {
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
			got, channelKey, id := decideClaudeSession(tc.args, "")
			if id != "abc-123" || channelKey != "abc-123" {
				t.Errorf("(channelKey,id) = (%q,%q), want both abc-123", channelKey, id)
			}
			if !reflect.DeepEqual(got, tc.args) {
				t.Errorf("forwarded mutated unexpectedly: %v", got)
			}
		})
	}
}

func TestDecideClaudeSession_ReusesUUIDChannelKey(t *testing.T) {
	// A UUID-shaped baseKey (minted earlier, or from CLAUDE_SESSION_ID) is reused
	// as claude's --session-id with no fresh mint, unifying resume id and channel.
	const uuid = "11111111-2222-4333-8444-555555555555"
	got, channelKey, id := decideClaudeSession(nil, uuid)
	if id != uuid || channelKey != uuid {
		t.Errorf("(channelKey,id) = (%q,%q), want both %q", channelKey, id, uuid)
	}
	if want := []string{"--session-id", uuid}; !reflect.DeepEqual(got, want) {
		t.Errorf("forwarded = %v, want %v", got, want)
	}
}

func TestDecideClaudeSession_RespectsNonUUIDOperatorKey(t *testing.T) {
	// A non-UUID baseKey is a deliberate operator override: it stays the channel
	// key while claude gets its own minted UUID as --session-id and the hint.
	got, channelKey, id := decideClaudeSession(nil, "operator-key")
	if !uuidRegexp.MatchString(id) {
		t.Errorf("claude id = %q, want a minted UUIDv4", id)
	}
	if channelKey != "operator-key" {
		t.Errorf("channelKey = %q, want operator-key (the channel stays the override)", channelKey)
	}
	if want := []string{"--session-id", id}; !reflect.DeepEqual(got, want) {
		t.Errorf("forwarded = %v, want %v", got, want)
	}
}

func TestDecideClaudeSession_SkipsForPrint(t *testing.T) {
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
			got, channelKey, id := decideClaudeSession(tc.args, "base")
			if id != "" {
				t.Errorf("id = %q, want empty (print mode skips hint)", id)
			}
			// No hint, but the channel key still flows through as baseKey.
			if channelKey != "base" {
				t.Errorf("channelKey = %q, want baseKey passthrough in print mode", channelKey)
			}
			if !reflect.DeepEqual(got, tc.args) {
				t.Errorf("forwarded mutated in print mode: %v", got)
			}
		})
	}
}

func TestDecideClaudeSession_SkipsForContinue(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"long form", []string{"--continue"}},
		{"short form", []string{"-c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, id := decideClaudeSession(tc.args, "base")
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
