package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/alfa/sessions"
	"code.linenisgreat.com/clown/pkg/profilelookup"
)

// clown#340: `clown profile digest <name>` prints the digest this build
// pins the profile at, exactly the launch's pin; an unknown profile, or one
// the library cannot vouch for, says so and exits 1 with nothing on stdout.
func TestProfileDigest(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.toml")
	const builtinName = "canary-spinclass"
	want, err := profilelookup.Resolve(builtinName, profilelookup.Options{RegistryPath: absent})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := profileDigest(builtinName, absent, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != want.DigestSHA256 || strings.Count(stdout.String(), "\n") != 1 {
		t.Errorf("stdout = %q, want one line %s", stdout.String(), want.DigestSHA256)
	}
	builtin, _, _, err := loadProfileSets(absent)
	if err != nil {
		t.Fatal(err)
	}
	for i := range builtin {
		if builtin[i].Name == builtinName {
			if pin, _, _ := launchProfilePin(&builtin[i], absent); pin != (sessions.ProfilePin{Name: builtinName, Digest: want.DigestSHA256}) {
				t.Errorf("the launch pins %+v, not what the verb prints", pin)
			}
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := profileDigest("no-such-profile", absent, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "no-such-profile") || !strings.Contains(stderr.String(), "cannot be pinned") {
		t.Errorf("unknown profile: code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

// describePinProvenance's four verdicts, from the pin's writer and protocol
// against this build's.
func TestDescribePinProvenance(t *testing.T) {
	pin := sessions.ProfilePin{Name: "p", Digest: "d"}
	for name, c := range map[string]struct {
		rec  sessions.PinRecord
		want []string
	}{
		"same build":     {sessions.PinRecord{ProfilePin: pin, Writer: "0.4.5+abcdef0", Protocol: 3}, []string{"this same clown build (0.4.5+abcdef0, protocol 3)", "the definition changed"}},
		"other protocol": {sessions.PinRecord{ProfilePin: pin, Writer: "0.4.4+0000000", Protocol: 2}, []string{"clown 0.4.4+0000000 under profile protocol 2", "this clown 0.4.5+abcdef0 speaks protocol 3", "every digest", "may be unchanged", "CLOWN_PROFILE_DIGEST"}},
		"other build":    {sessions.PinRecord{ProfilePin: pin, Writer: "0.4.4+0000000", Protocol: 3}, []string{"clown 0.4.4+0000000", "same profile protocol 3", "changed with the build", "clown profile digest"}},
		"no provenance":  {sessions.PinRecord{ProfilePin: pin}, []string{"predates clown#340", "unknown"}},
	} {
		got := describePinProvenance(c.rec, "0.4.5+abcdef0", 3)
		for _, s := range c.want {
			if !strings.Contains(got, s) {
				t.Errorf("%s: %q lacks %q", name, got, s)
			}
		}
		if strings.Contains(got, "\n") {
			t.Errorf("%s: more than one line: %q", name, got)
		}
	}
}
