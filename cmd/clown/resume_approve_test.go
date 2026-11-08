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
	want := "\nto approve this digest: CLOWN_PROFILE_DIGEST=d CLOWN_PROFILE_SESSION=id clown resume id --profile p"
	if got := approveCommandLine("id", "p", "d"); got != want {
		t.Errorf("got %q, want %q", got, want)
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
	want := "CLOWN_PROFILE_DIGEST=" + r.current + " CLOWN_PROFILE_SESSION=" + id + " clown resume " + id + " --profile e2e-approve"
	if !strings.HasSuffix(err.Error(), want) {
		t.Errorf("refusal does not end with %q:\n%s", want, err)
	}
}
