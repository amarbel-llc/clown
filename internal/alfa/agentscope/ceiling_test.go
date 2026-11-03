package agentscope

import (
	"slices"
	"strings"
	"testing"
)

// The default ceiling is what the operator ratified on 2026-11-03
// (docs/plans/2026-11-03-structural-moxy-v3-build-plan.md, item 1). The
// list is pinned here so a change to it is a deliberate diff, not a side
// effect; restart in particular is refused by moxy under enforce.
func TestDefaultCeilingIsTheRatifiedList(t *testing.T) {
	want := []string{
		"toolbox", "env", "man",
		"rg.search",
		"folio.read", "folio.ls", "folio.glob", "folio.tree", "folio.file-type", "folio.mktemp",
		"folio.write", "folio.mkdir", "folio.mv", "folio.rm",
	}
	if !slices.Equal(DefaultCeiling, want) {
		t.Fatalf("DefaultCeiling = %v, want the ratified list %v", DefaultCeiling, want)
	}
	for _, gone := range []string{
		"restart", "batch", "async", "status", "grit", "chix", "jq", "folio", "rg",
		"folio.cp", "folio.link", "folio.chmod", "folio.du", "folio.df", "folio.tar",
	} {
		if slices.Contains(DefaultCeiling, gone) {
			t.Errorf("%q is in the default ceiling", gone)
		}
	}
	for _, disabled := range []string{"smith", "chrest", "grit", "chix", "jq", "freud", "stats-me"} {
		if !slices.Contains(DefaultDisabledMoxins, disabled) {
			t.Errorf("%q is not disabled", disabled)
		}
	}
}

// The rendered file is stable bytes under enforce, sorted and deduplicated,
// so its digest identifies the ceiling (the result record will carry it).
func TestCeilingMoxyfileRendersEnforceAndSortedLists(t *testing.T) {
	got := string(CeilingMoxyfile([]string{"man", "folio.read", "env", "env"}, []string{"smith", "chrest"}))
	for _, want := range []string{
		`permission-mode = "enforce"`,
		"allow-tools = [\n  \"env\",\n  \"folio.read\",\n  \"man\"\n]\n",
		"disable-moxins = [\n  \"chrest\",\n  \"smith\"\n]\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("moxyfile lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, `"env"`) != 1 {
		t.Errorf("duplicate entry survived:\n%s", got)
	}
	again := string(CeilingMoxyfile([]string{"env", "man", "folio.read"}, []string{"chrest", "smith"}))
	if again != got {
		t.Errorf("the same ceiling rendered differently:\n%s\nvs\n%s", got, again)
	}
	if string(CeilingMoxyfile(nil, nil)) != strings.SplitAfter(got, "permission-mode = \"enforce\"\n")[0]+"allow-tools = []\ndisable-moxins = []\n" {
		t.Errorf("empty lists render oddly:\n%s", CeilingMoxyfile(nil, nil))
	}
}

// InUnitMoxyfile is the default ceiling rendered: what startFrontendEndpoints
// binds over the unit's global moxyfile.
func TestInUnitMoxyfileIsTheDefaultCeiling(t *testing.T) {
	if InUnitMoxyfile != string(CeilingMoxyfile(DefaultCeiling, DefaultDisabledMoxins)) {
		t.Fatal("InUnitMoxyfile is not the rendered default ceiling")
	}
	for _, want := range []string{`permission-mode = "enforce"`, `"folio.write"`, `"rg.search"`, `"toolbox"`} {
		if !strings.Contains(InUnitMoxyfile, want) {
			t.Errorf("InUnitMoxyfile lacks %s", want)
		}
	}
	if strings.Contains(InUnitMoxyfile, `"restart"`) {
		t.Error("InUnitMoxyfile still lists restart, which enforce refuses")
	}
}

// A profile or spec may only narrow the default: a dotted tool is covered by
// the same entry or by the default holding the whole moxin; a bare moxin only
// by the same bare name; anything else is refused by name.
func TestIntersectAllowNarrowsOnly(t *testing.T) {
	def := []string{"toolbox", "folio.read", "folio.write"}
	got, err := IntersectAllow(def, []string{"folio.write", "toolbox.sha256", "toolbox", "folio.write"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"folio.write", "toolbox", "toolbox.sha256"}) {
		t.Errorf("narrowed = %v", got)
	}
	if got, err := IntersectAllow(def, nil); err != nil || !slices.Equal(got, []string{"folio.read", "folio.write", "toolbox"}) {
		t.Errorf("nil request = %v, %v; want the default", got, err)
	}
	_, err = IntersectAllow(def, []string{"folio", "grit.commit", "folio.read", "rg.search"})
	if err == nil {
		t.Fatal("a widening was accepted")
	}
	for _, name := range []string{"folio", "grit.commit", "rg.search"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("refusal does not name %q: %v", name, err)
		}
	}
	if strings.Contains(err.Error(), "folio.read") {
		t.Errorf("refusal names an allowed entry: %v", err)
	}
}
