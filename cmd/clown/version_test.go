package main

import "testing"

// eng-versioning(7) "self-as-row": clown folds its identity into the component
// table as the FIRST row, ahead of the components it pins. Guards against a
// regression to the old alphabetical sort, which buried clown in the middle.
func TestBuildVersionRowsSelfFirst(t *testing.T) {
	// Exclude plugin rows for a deterministic set independent of the host's
	// CLOWN_PLUGIN_META.
	t.Setenv("CLOWN_PLUGIN_META", "")

	rows := buildVersionRows()
	if len(rows) == 0 {
		t.Fatal("buildVersionRows returned no rows")
	}
	if rows[0].component != "clown" {
		t.Fatalf("self-as-row: clown must be the first row, got %q (rows=%+v)", rows[0].component, rows)
	}
	// The remaining (non-self) rows are sorted for stable output.
	for i := 2; i < len(rows); i++ {
		if rows[i-1].component > rows[i].component {
			t.Fatalf("non-self rows must be sorted: %q before %q", rows[i-1].component, rows[i].component)
		}
	}
	// A second clown row must not sneak in (self-row only, not also in rest).
	for _, r := range rows[1:] {
		if r.component == "clown" {
			t.Fatalf("clown must appear exactly once (as the self-row), found again in %+v", rows)
		}
	}
}
