package profilerules

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// vectorDocument is the vector as the validator reads it: a hyphence
// document when it has a head (hyphence(7) DOCUMENT STRUCTURE, BODY
// SEPARATOR), the bare body when it has none.
func vectorDocument(v Vector) string {
	if v.Head == "" {
		return v.Body
	}
	return "---\n" + v.Head + "\n---\n\n" + v.Body
}

func validateVector(v Vector) []Finding {
	if v.Layer == LayerPatch {
		return ValidatePatch(v.Body)
	}
	return Validate(vectorDocument(v))
}

// Every vector's findings, counted by rule, are exactly its expectation.
func TestValidateRunsEveryVector(t *testing.T) {
	for _, v := range mustVectors(t) {
		t.Run(v.ID, func(t *testing.T) {
			findings := validateVector(v)
			got := map[string]int{}
			for _, f := range findings {
				got[f.Rule]++
			}
			want := map[string]int{}
			for _, e := range v.Expect {
				want[e.Rule] = e.Count
			}
			if !maps.Equal(got, want) {
				var msgs []string
				for _, f := range findings {
					msgs = append(msgs, f.Message)
				}
				t.Errorf("findings %v, want %v:\n%s", got, want, strings.Join(msgs, "\n"))
			}
		})
	}
}

// A finding carries its rule's severity and a message in that rule's voice.
func TestFindingsCarryTheirRulesSeverityAndPrefix(t *testing.T) {
	for _, v := range mustVectors(t) {
		for _, f := range validateVector(v) {
			spec, ok := Rule(f.Rule)
			if !ok {
				t.Fatalf("vector %s: finding for undeclared rule %s", v.ID, f.Rule)
			}
			if f.Severity != spec.Severity {
				t.Errorf("vector %s: %s severity %q, want %q", v.ID, f.Rule, f.Severity, spec.Severity)
			}
			if !strings.HasPrefix(f.Message, f.Rule+": ") {
				t.Errorf("vector %s: message %q lacks the %q prefix", v.ID, f.Message, f.Rule+": ")
			}
		}
	}
}

// The head rules do not run on a head-less body.
func TestHeadlessBodySkipsHeadAndModeRules(t *testing.T) {
	findings := Validate("[moxy !clown-plugin-v1 [-> man !mcp-server-v1 [-> toc !mcp-tool-v1]]]")
	if i := slices.IndexFunc(findings, func(f Finding) bool {
		return strings.HasPrefix(f.Rule, "H-") || strings.HasPrefix(f.Rule, "M-")
	}); i >= 0 {
		t.Errorf("head-less body got %s: %s", findings[i].Rule, findings[i].Message)
	}
}
