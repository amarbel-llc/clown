package main

import (
	"slices"
	"testing"
)

// Every --resume/-r and --session-id occurrence, in every spelling claude's
// parser (commander, with _combineFlagAndOptionalValue: verified in the
// claude binary's strings) accepts, so no spelling hides an id from the
// confinement check (clown#318). A short-option cluster with r inside it is
// not parsed (whether the r takes the next argument depends on its
// neighbours) and is reported as unknown. UUIDs come back lower-cased.
func TestClaudeConversationRefs(t *testing.T) {
	const upper = "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"
	const lower = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	cases := map[string]struct {
		args        []string
		wantResume  []string
		wantSession []string
		wantUnknown bool
	}{
		"none":                           {args: []string{"-p", "hi"}},
		"--resume twice":                 {args: []string{"--resume", "a", "--resume", "b"}, wantResume: []string{"a", "b"}},
		"-r and --resume=":               {args: []string{"-r", "a", "--resume=b"}, wantResume: []string{"a", "b"}},
		"-r with an attached value":      {args: []string{"-r" + lower}, wantResume: []string{lower}},
		"-rc is -r with the value c":     {args: []string{"-rc"}, wantResume: []string{"c"}},
		"-r= keeps the = (commander)":    {args: []string{"-r=x"}, wantResume: []string{"=x"}},
		"cluster with r: unknown":        {args: []string{"-cr"}, wantUnknown: true},
		"cluster with r, value: unknown": {args: []string{"-pr", lower}, wantUnknown: true},
		"cluster without r":              {args: []string{"-cp"}},
		"bare --resume is no value":      {args: []string{"--resume", "--verbose"}},
		"--session-id forms":             {args: []string{"--session-id", "a", "--session-id=b"}, wantSession: []string{"a", "b"}},
		"both":                           {args: []string{"--resume", "a", "--fork-session", "--session-id", "b"}, wantResume: []string{"a"}, wantSession: []string{"b"}},
		"uppercase UUIDs lower-cased":    {args: []string{"--resume", upper, "--session-id=" + upper}, wantResume: []string{lower}, wantSession: []string{lower}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resume, sids, unknown := claudeConversationRefs(tc.args)
			if !slices.Equal(resume, tc.wantResume) || !slices.Equal(sids, tc.wantSession) || unknown != tc.wantUnknown {
				t.Errorf("claudeConversationRefs = %q, %q, %v; want %q, %q, %v", resume, sids, unknown, tc.wantResume, tc.wantSession, tc.wantUnknown)
			}
		})
	}
}

// resumeArgRaw (the profile check, decideClaudeSession) reads the same
// spellings: an attached -r value is a value, and a cluster with r reads as
// the bare picker (no id: the profile check refuses it, decideClaudeSession
// prints no hint).
func TestResumeArgRawShortForms(t *testing.T) {
	const id = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	cases := map[string]struct {
		args      []string
		wantFlag  string
		wantValue string
	}{
		"attached": {args: []string{"-r" + id}, wantFlag: "-r", wantValue: id},
		"-r=":      {args: []string{"-r=" + id}, wantFlag: "-r", wantValue: "=" + id},
		"cluster":  {args: []string{"-cr", id}, wantFlag: "-r"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			flag, v := resumeArgRaw(tc.args)
			if flag != tc.wantFlag || v != tc.wantValue {
				t.Errorf("resumeArgRaw = %q, %q; want %q, %q", flag, v, tc.wantFlag, tc.wantValue)
			}
		})
	}
}
