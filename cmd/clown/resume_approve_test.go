package main

import (
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/0/profile"
	"code.linenisgreat.com/clown/internal/alfa/sessions"
	"code.linenisgreat.com/clown/pkg/profilelookup"
)

func TestApproveCommandLine(t *testing.T) {
	if got := approveCommandLine("id", "p", ""); got != "" {
		t.Errorf("no digest: %q, want nothing", got)
	}
	want := "\nto approve this digest: CLOWN_PROFILE_DIGEST=d CLOWN_PROFILE_SESSION=id clown resume clown://claude/id --profile p"
	if got := approveCommandLine("id", "p", "d"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := approveCommandLine("id", "my profile; rm 'x'", "d"); !strings.HasSuffix(got, `--profile 'my profile; rm '\''x'\'''`) {
		t.Errorf("odd name not quoted: %q", got)
	}
}

// The printed line parses: its resume arguments go through parseResumeArgs and
// ParseURI and come out as the session and profile the line names.
func TestApproveCommandLineParses(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555556"
	line := strings.TrimPrefix(approveCommandLine(id, "e2e-approve", "d"), "\nto approve this digest: ")
	_, rest, ok := strings.Cut(line, " clown resume ")
	if !ok {
		t.Fatalf("no resume command in %q", line)
	}
	a, err := parseResumeArgs(strings.Fields(rest))
	if err != nil {
		t.Fatalf("printed line does not parse: %v", err)
	}
	provider, gotID, err := sessions.ParseURI(a.uri)
	if err != nil || provider != "claude" || gotID != id || a.profile != "e2e-approve" {
		t.Errorf("parsed %+v (%s, %s, %v)", a, provider, gotID, err)
	}
}

// A changed-profile refusal ends with the literal approve command naming the
// new digest, the session and the profile.
func TestChangedProfileRefusalEndsWithTheApproveCommand(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const id = "11111111-2222-3333-4444-555555555556"
	registry := filepath.Join(t.TempDir(), "profiles.toml")
	p := profile.Profile{Name: "e2e-approve", Provider: "claude", Backend: "anthropic", Model: "model-a"}
	if err := profile.Save(registry, []profile.Profile{p}, "0.4.5"); err != nil {
		t.Fatal(err)
	}
	pin, form, ok := launchProfilePin(&p, registry)
	if !ok {
		t.Fatal("profile not pinnable")
	}
	if err := sessions.RecordSession(sessions.NameRecord{SessionID: id, Profile: pin.Name, ProfileDigest: pin.Digest, ProfileForm: form}); err != nil {
		t.Fatal(err)
	}
	p.Model = "model-b"
	if err := profile.Save(registry, []profile.Profile{p}, "0.4.5"); err != nil {
		t.Fatal(err)
	}
	_, err := resumeProfile(id, false, profilelookup.Options{RegistryPath: registry})
	if err == nil {
		t.Fatal("want a refusal")
	}
	r, ok := err.(*profileRefusal)
	if !ok {
		t.Fatalf("err = %T, want *profileRefusal", err)
	}
	want := "CLOWN_PROFILE_DIGEST=" + r.current + " CLOWN_PROFILE_SESSION=" + id + " clown resume clown://claude/" + id + " --profile e2e-approve"
	if !strings.HasSuffix(err.Error(), want) {
		t.Errorf("refusal does not end with %q:\n%s", want, err)
	}
}
