package main

import (
	"slices"
	"testing"
)

// Every --resume/-r and --session-id occurrence, in every spelling, so a
// repeated flag cannot hide an id from the confinement check (clown#318).
func TestClaudeConversationRefs(t *testing.T) {
	cases := map[string]struct {
		args        []string
		wantResume  []string
		wantSession []string
	}{
		"none":                      {args: []string{"-p", "hi"}},
		"--resume twice":            {args: []string{"--resume", "a", "--resume", "b"}, wantResume: []string{"a", "b"}},
		"-r and --resume=":          {args: []string{"-r", "a", "--resume=b", "-r=c"}, wantResume: []string{"a", "b", "c"}},
		"bare --resume is no value": {args: []string{"--resume", "--verbose"}},
		"--session-id forms":        {args: []string{"--session-id", "a", "--session-id=b"}, wantSession: []string{"a", "b"}},
		"both":                      {args: []string{"--resume", "a", "--fork-session", "--session-id", "b"}, wantResume: []string{"a"}, wantSession: []string{"b"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resume, sids := claudeConversationRefs(tc.args)
			if !slices.Equal(resume, tc.wantResume) || !slices.Equal(sids, tc.wantSession) {
				t.Errorf("claudeConversationRefs = %q, %q; want %q, %q", resume, sids, tc.wantResume, tc.wantSession)
			}
		})
	}
}
