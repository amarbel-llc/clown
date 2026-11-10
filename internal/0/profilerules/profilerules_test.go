package profilerules

import (
	"slices"
	"strings"
	"testing"
)

func mustVectors(t *testing.T) []Vector {
	t.Helper()
	vs, err := Vectors()
	if err != nil {
		t.Fatalf("Vectors: %v", err)
	}
	return vs
}

func TestRuleIDsAreUniqueAndWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Rules {
		if seen[r.ID] {
			t.Errorf("rule %s declared twice", r.ID)
		}
		seen[r.ID] = true
		if r.Severity != Refuse && r.Severity != Warn {
			t.Errorf("rule %s: severity %q", r.ID, r.Severity)
		}
		if !strings.HasPrefix(r.Message, r.ID+": ") {
			t.Errorf("rule %s: message must start with %q: %q", r.ID, r.ID+": ", r.Message)
		}
		if r.Rule == "" {
			t.Errorf("rule %s: no rule text", r.ID)
		}
	}
}

func TestEveryRuleHasAVector(t *testing.T) {
	vs := mustVectors(t)
	for _, r := range Rules {
		found := slices.ContainsFunc(vs, func(v Vector) bool {
			return slices.ContainsFunc(v.Expect, func(e Expect) bool { return e.Rule == r.ID })
		})
		if !found {
			t.Errorf("rule %s has no vector expecting it", r.ID)
		}
	}
}

func TestEveryVectorNamesDeclaredRules(t *testing.T) {
	vs := mustVectors(t)
	ids := map[string]bool{}
	for _, v := range vs {
		if ids[v.ID] {
			t.Errorf("vector %s declared twice", v.ID)
		}
		ids[v.ID] = true
		if v.Layer != LayerStencil && v.Layer != LayerPatch {
			t.Errorf("vector %s: layer %q", v.ID, v.Layer)
		}
		if strings.TrimSpace(v.Body) == "" {
			t.Errorf("vector %s: empty body", v.ID)
		}
		for _, e := range v.Expect {
			if _, ok := Rule(e.Rule); !ok {
				t.Errorf("vector %s expects undeclared rule %s", v.ID, e.Rule)
			}
			if e.Count < 1 {
				t.Errorf("vector %s: %s count %d", v.ID, e.Rule, e.Count)
			}
		}
	}
}

// A vector that elides with "…" is a table abbreviation, not a document.
func TestVectorsAreWholeDocuments(t *testing.T) {
	for _, v := range mustVectors(t) {
		if strings.Contains(v.Body, "…") || strings.Contains(v.Head, "…") {
			t.Errorf("vector %s is elided", v.ID)
		}
	}
}
