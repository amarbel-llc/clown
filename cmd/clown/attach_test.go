package main

import (
	"reflect"
	"testing"

	"github.com/amarbel-llc/clown/internal/clownfile"
)

func TestExtractAttachID(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantID  string
		wantArg []string
	}{
		{"absent", []string{"--provider", "claude"}, "", []string{"--provider", "claude"}},
		{"space form", []string{"--clown-attach-id", "k1", "resume"}, "k1", []string{"resume"}},
		{"equals form", []string{"--clown-attach-id=k2", "--", "hi"}, "k2", []string{"--", "hi"}},
		{"interleaved", []string{"--provider", "claude", "--clown-attach-id", "k3", "--", "x"}, "k3", []string{"--provider", "claude", "--", "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, rest := extractAttachID(tc.args)
			if id != tc.wantID {
				t.Errorf("id = %q, want %q", id, tc.wantID)
			}
			if !reflect.DeepEqual(rest, tc.wantArg) {
				t.Errorf("rest = %v, want %v", rest, tc.wantArg)
			}
		})
	}
}

// resolveSessionIdentity pins to attachedID (the [attach] re-exec id) at top
// precedence over the env-derived key (clown#145 / RFC-0013 §1.3).
func TestResolveSessionIdentityHonorsAttachID(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "ambient-key")
	prev := attachedID
	attachedID = "pinned-attach-id"
	t.Cleanup(func() { attachedID = prev })

	if got := resolveSessionIdentity(); got.Key != "pinned-attach-id" {
		t.Fatalf("identity.Key = %q, want the pinned attach id", got.Key)
	}
}

// Loop guard: the inner attached process (attachedID set) MUST NOT re-wrap, even
// with the multiplexer enabled and the TTY check forced (clown#145). Returns nil
// (skip) before any exec.
func TestMaybeReexecSkipsWhenAttached(t *testing.T) {
	prev := attachedID
	attachedID = "already-inside"
	t.Cleanup(func() { attachedID = prev })
	t.Setenv("CLOWN_ATTACH_FORCE", "1")

	cf := clownfile.Clownfile{Attach: clownfile.Attach{
		Multiplexer: "zmx",
		Start:       []string{"zmx", "attach", "{id}", "{entry}"},
	}}
	if err := maybeReexecMultiplexer(cf, parsedFlags{}, clownfile.ModeStart); err != nil {
		t.Fatalf("loop guard: want nil (skip), got %v", err)
	}
}

// Disabled [attach] (absent / "none") runs inline: the gate returns nil without
// execing.
func TestMaybeReexecSkipsWhenDisabled(t *testing.T) {
	prev := attachedID
	attachedID = ""
	t.Cleanup(func() { attachedID = prev })

	if err := maybeReexecMultiplexer(clownfile.Clownfile{}, parsedFlags{}, clownfile.ModeStart); err != nil {
		t.Fatalf("absent [attach]: want nil (skip), got %v", err)
	}
	none := clownfile.Clownfile{Attach: clownfile.Attach{Multiplexer: "none"}}
	if err := maybeReexecMultiplexer(none, parsedFlags{}, clownfile.ModeStart); err != nil {
		t.Fatalf(`multiplexer "none": want nil (skip), got %v`, err)
	}
}
