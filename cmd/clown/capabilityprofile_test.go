package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/alfa/profilerules"
)

// vectorWhere returns the first RFC 0019 vector of layer whose expectations
// satisfy want, so the lint verb is checked against the ruleset's own rows.
func vectorWhere(t *testing.T, layer string, want func([]profilerules.Expect) bool) profilerules.Vector {
	t.Helper()
	vs, err := profilerules.Vectors()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		if v.Layer == layer && want(v.Expect) {
			return v
		}
	}
	t.Fatalf("no %s vector matches", layer)
	return profilerules.Vector{}
}

func hasRefusal(es []profilerules.Expect) bool {
	for _, e := range es {
		if r, ok := profilerules.Rule(e.Rule); ok && r.Severity == profilerules.Refuse {
			return true
		}
	}
	return false
}

// vectorDocument writes v as the file the verb reads: the hyphence head, when
// the vector has one, then the body.
func vectorDocument(t *testing.T, v profilerules.Vector) string {
	t.Helper()
	doc := v.Body
	if v.Head != "" {
		doc = "---\n" + v.Head + "\n---\n\n" + v.Body
	}
	path := filepath.Join(t.TempDir(), v.ID)
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCheck(args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = runCapabilityProfile(append([]string{"check"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestCapabilityProfileCheckRefusesAndNamesTheRule(t *testing.T) {
	v := vectorWhere(t, profilerules.LayerStencil, hasRefusal)
	code, out, errOut := runCheck(vectorDocument(t, v))
	if code != 1 {
		t.Fatalf("vector %s: exit %d, want 1 (a refusal); stdout %q stderr %q", v.ID, code, out, errOut)
	}
	for _, e := range v.Expect {
		if !strings.Contains(out, e.Rule+":") {
			t.Errorf("vector %s: output does not name %s:\n%s", v.ID, e.Rule, out)
		}
	}
}

func TestCapabilityProfileCheckPrintsWarningsWithoutFailing(t *testing.T) {
	v := vectorWhere(t, profilerules.LayerStencil, func(es []profilerules.Expect) bool {
		return len(es) > 0 && !hasRefusal(es)
	})
	code, out, errOut := runCheck(vectorDocument(t, v))
	if code != 0 {
		t.Fatalf("vector %s: exit %d, want 0 (warnings only); stdout %q stderr %q", v.ID, code, out, errOut)
	}
	for _, e := range v.Expect {
		if !strings.Contains(out, e.Rule+":") {
			t.Errorf("vector %s: output does not name %s:\n%s", v.ID, e.Rule, out)
		}
	}
}

func TestCapabilityProfileCheckPassesACleanProfile(t *testing.T) {
	v := vectorWhere(t, profilerules.LayerStencil, func(es []profilerules.Expect) bool { return len(es) == 0 })
	code, out, errOut := runCheck(vectorDocument(t, v))
	if code != 0 || out != "" {
		t.Fatalf("vector %s: exit %d, stdout %q, stderr %q; want exit 0 and no findings", v.ID, code, out, errOut)
	}
}

func TestCapabilityProfileCheckPatchChecksAnActBody(t *testing.T) {
	v := vectorWhere(t, profilerules.LayerPatch, hasRefusal)
	code, out, errOut := runCheck("--patch", vectorDocument(t, v))
	if code != 1 {
		t.Fatalf("vector %s: exit %d, want 1 (a refusal); stdout %q stderr %q", v.ID, code, out, errOut)
	}
	for _, e := range v.Expect {
		if !strings.Contains(out, e.Rule+":") {
			t.Errorf("vector %s: output does not name %s:\n%s", v.ID, e.Rule, out)
		}
	}
}

func TestCapabilityProfileCheckUsageErrors(t *testing.T) {
	for name, args := range map[string][]string{
		"no file":      {},
		"two files":    {"a", "b"},
		"missing file": {filepath.Join(t.TempDir(), "absent")},
	} {
		if code, _, _ := runCheck(args...); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
	var out, errb bytes.Buffer
	if code := runCapabilityProfile(nil, &out, &errb); code != 2 {
		t.Errorf("no subcommand: exit %d, want 2", code)
	}
}
