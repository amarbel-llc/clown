package profilelookup_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/profile"
	"code.linenisgreat.com/clown/internal/sessions"
	"code.linenisgreat.com/clown/pkg/profilelookup"
)

// clown#298: the redacted form a pin stores, its lookup by digest, and the
// diff over two stored forms.

func resolveOne(t *testing.T, p profile.Profile) *profilelookup.Resolved {
	t.Helper()
	r, err := profilelookup.Resolve(p.Name, profilelookup.Options{RegistryPath: writeRegistry(t, "0.4.5", p)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func redactedForm(t *testing.T, r *profilelookup.Resolved) []byte {
	t.Helper()
	form, err := r.RedactedForm()
	if err != nil {
		t.Fatal(err)
	}
	return form
}

func pinWithForm(t *testing.T, id string, r *profilelookup.Resolved) {
	t.Helper()
	rec := sessions.NameRecord{SessionID: id, Name: "bozo", Profile: r.Name, ProfileDigest: r.DigestSHA256, ProfileForm: redactedForm(t, r)}
	if err := sessions.RecordSession(rec); err != nil {
		t.Fatal(err)
	}
}

// Nothing secret reaches the sidecar: not URL userinfo, query or fragment,
// not the token, not an env value, not an env value's digest.
func TestRedactedFormHoldsNoSecretsInTheSidecar(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	const envValue = "envval-SECRET-41"
	p := profile.Profile{
		Name: "leaky", Provider: "claude", Backend: "anthropic", Model: "m1",
		URL:   "https://alice:hunter2@gw.example/v1?api_key=sekrit-q#frag-x",
		Token: "tok-SECRET-99",
		Env:   map[string]string{"API_KEY": envValue},
	}
	r := resolveOne(t, p)
	pinWithForm(t, "id-1", r)

	b, err := os.ReadFile(filepath.Join(state, "clown", "session-names.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(envValue))
	for _, secret := range []string{"alice", "hunter2", "sekrit-q", "frag-x", "api_key=", "tok-SECRET-99", envValue, hex.EncodeToString(sum[:])} {
		if strings.Contains(string(b), secret) {
			t.Errorf("sidecar carries %q: %s", secret, b)
		}
	}
	for _, kept := range []string{`"API_KEY"`, `"token_set":true`, `"model":"m1"`} {
		if !strings.Contains(string(b), kept) {
			t.Errorf("sidecar lacks %s: %s", kept, b)
		}
	}
}

// A secret in a plain path segment passes urlMayCarryCredential, so the
// stored url keeps only scheme://host[:port]; a path change is not shown.
func TestRedactedFormDropsTheURLPath(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	p := profile.Profile{Name: "pathy", Provider: "claude", Backend: "anthropic", URL: "https://gw.example:8443/v1/sk-PATHSECRET-7/chat"}
	r := resolveOne(t, p)
	pinWithForm(t, "id-1", r)
	b, err := os.ReadFile(filepath.Join(state, "clown", "session-names.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "PATHSECRET") || strings.Contains(string(b), "/v1") {
		t.Errorf("sidecar carries the url path: %s", b)
	}
	if !strings.Contains(string(b), `"url":"https://gw.example:8443"`) {
		t.Errorf("sidecar lacks scheme://host:port: %s", b)
	}

	q := p
	q.URL = "https://gw.example:8443/v2/other"
	lines, _, err := profilelookup.DiffRedacted(redactedForm(t, r), redactedForm(t, resolveOne(t, q)), 6)
	if err != nil || len(lines) != 0 {
		t.Errorf("path-only change: DiffRedacted = %q, %v; want no lines", lines, err)
	}
}

// The form is deterministic and leaves Canonical and the digest untouched.
func TestRedactedFormIsDeterministic(t *testing.T) {
	p := trimmed()
	p.Env = map[string]string{"B": "2", "A": "1"}
	a, b := resolveOne(t, p), resolveOne(t, p)
	canon := slices.Clone(a.Canonical)
	fa, fb := redactedForm(t, a), redactedForm(t, b)
	if string(fa) != string(fb) {
		t.Fatalf("forms differ:\n%s\n%s", fa, fb)
	}
	if string(a.Canonical) != string(canon) {
		t.Errorf("RedactedForm changed Canonical")
	}
	if !json.Valid(fa) || strings.Contains(string(fa), "\n") {
		t.Errorf("form is not one line of JSON: %q", fa)
	}
	if !strings.Contains(string(fa), `"env_keys":["A","B"]`) {
		t.Errorf("form = %s; want sorted env keys", fa)
	}
}

// The re-pin case: the pinned definition no longer resolves (same name,
// edited), so the old side comes from the sidecar by digest.
func TestRedactedFormByDigestDiffsASameNameEdit(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	before := profile.Profile{Name: "e", Provider: "claude", Backend: "anthropic", Model: "opus", Env: map[string]string{"KEEP": "1", "GONE": "x"}}
	old := resolveOne(t, before)
	pinWithForm(t, "id-1", old)

	after := before
	after.Model = "sonnet"
	after.Env = map[string]string{"KEEP": "changed", "NEW": "y"}
	cur := resolveOne(t, after)

	oldForm, ok, err := profilelookup.RedactedFormByDigest(old.DigestSHA256)
	if err != nil || !ok {
		t.Fatalf("RedactedFormByDigest = ok %v, err %v", ok, err)
	}
	lines, more, err := profilelookup.DiffRedacted(oldForm, redactedForm(t, cur), 6)
	if err != nil {
		t.Fatal(err)
	}
	// Env values are not stored, so KEEP's change cannot be seen; added and
	// removed keys can.
	want := []string{`model: "opus" -> "sonnet"`, `env "GONE": removed`, `env "NEW": added`}
	if !slices.Equal(lines, want) || more != 0 {
		t.Errorf("DiffRedacted = %q, %d; want %q", lines, more, want)
	}

	if _, ok, err := profilelookup.RedactedFormByDigest(strings.Repeat("0", 64)); ok || err != nil {
		t.Errorf("unknown digest: ok %v, err %v; want not found", ok, err)
	}
}

// DiffRedacted keeps Diff's cap, and shows a credential-bearing URL only as
// the redaction.
func TestDiffRedactedCapAndURL(t *testing.T) {
	a := profile.Profile{Name: "c", Provider: "claude", Backend: "anthropic", URL: "https://gw.example/v1"}
	b := a
	b.URL = "https://u:p@gw.example/v1"
	b.Env = map[string]string{}
	for i := range 10 {
		b.Env[fmt.Sprintf("K%02d", i)] = "v"
	}
	lines, more, err := profilelookup.DiffRedacted(redactedForm(t, resolveOne(t, a)), redactedForm(t, resolveOne(t, b)), 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 6 || more != 6 || lines[5] != "+6 more changes" {
		t.Errorf("DiffRedacted = %q, %d; want 6 lines ending in +6 more changes", lines, more)
	}
	// The stored url keeps only scheme and host.
	if lines[0] != `url: "https://gw.example" -> `+profilelookup.RedactedURL {
		t.Errorf("url line = %q", lines[0])
	}
	if strings.Contains(strings.Join(lines, "\n"), "u:p") {
		t.Errorf("credential printed: %q", lines)
	}
}

func TestDiffRedactedRejectsAnUnknownForm(t *testing.T) {
	good := redactedForm(t, resolveOne(t, trimmed()))
	for _, bad := range []string{`{"form":99}`, `not json`, ``} {
		if _, _, err := profilelookup.DiffRedacted([]byte(bad), good, 6); err == nil {
			t.Errorf("DiffRedacted(%q, good): want an error", bad)
		}
	}
}
