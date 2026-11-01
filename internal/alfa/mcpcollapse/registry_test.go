package mcpcollapse

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestLookupResolvesDottedID verifies the primary happy path: a single
// server's tool renders to a {server}.{tool} id and Lookup returns the
// canonical entry verbatim — the URL mcp_call dispatches to, the real
// upstream tool name, and the schema/description handed back untouched.
func TestLookupResolvesDottedID(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`)
	var b Builder
	b.AddServer("grit", "http://127.0.0.1:9001/mcp", []ToolSpec{
		{Name: "commit", Description: "record staged changes", Schema: schema},
	})
	reg, err := b.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	entry, ok := reg.Lookup("grit.commit")
	if !ok {
		t.Fatalf("Lookup(grit.commit): not found")
	}
	if entry.Server != "grit" {
		t.Errorf("Server = %q, want grit", entry.Server)
	}
	if entry.Tool != "commit" {
		t.Errorf("Tool = %q, want commit", entry.Tool)
	}
	if entry.URL != "http://127.0.0.1:9001/mcp" {
		t.Errorf("URL = %q, want http://127.0.0.1:9001/mcp", entry.URL)
	}
	if entry.Description != "record staged changes" {
		t.Errorf("Description = %q, want record staged changes", entry.Description)
	}
	if string(entry.Schema) != string(schema) {
		t.Errorf("Schema = %s, want %s (must be handed back verbatim)", entry.Schema, schema)
	}
}

// TestLookupMissingID verifies an unknown id resolves to (zero, false) rather
// than a zero Entry that looks real — mcp_call/mcp_describe rely on the bool to
// reject ids the agent invented.
func TestLookupMissingID(t *testing.T) {
	var b Builder
	b.AddServer("grit", "http://127.0.0.1:9001/mcp", []ToolSpec{{Name: "commit"}})
	reg, err := b.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := reg.Lookup("grit.push"); ok {
		t.Fatal("Lookup(grit.push): want not found, got found")
	}
}

// TestBuildRejectsDuplicateServerName verifies the primary collision defense:
// two DIFFERENT upstreams registered under the same server name make Build fail
// with an error naming BOTH (so a silently-dropped server's tools aren't a
// "why is this tool missing?" mystery). The error must mention both URLs so the
// caller can identify the conflict.
func TestBuildRejectsDuplicateServerName(t *testing.T) {
	var b Builder
	b.AddServer("grit", "http://127.0.0.1:9001/mcp", []ToolSpec{{Name: "commit"}})
	b.AddServer("grit", "http://127.0.0.1:9002/mcp", []ToolSpec{{Name: "status"}})

	_, err := b.Build()
	if err == nil {
		t.Fatal("Build: want error for duplicate server name, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"grit", "http://127.0.0.1:9001/mcp", "http://127.0.0.1:9002/mcp"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not name %q", msg, want)
		}
	}
}

// TestEntriesStableOrder verifies Entries() returns a deterministic id-sorted
// slice regardless of the order servers/tools were added — mcp_list output must
// not shuffle between builds.
func TestEntriesStableOrder(t *testing.T) {
	build := func(reverse bool) []Entry {
		var b Builder
		if reverse {
			b.AddServer("zulu", "http://127.0.0.1:9002/mcp", []ToolSpec{{Name: "tango"}, {Name: "alpha"}})
			b.AddServer("alpha", "http://127.0.0.1:9001/mcp", []ToolSpec{{Name: "zulu"}, {Name: "bravo"}})
		} else {
			b.AddServer("alpha", "http://127.0.0.1:9001/mcp", []ToolSpec{{Name: "bravo"}, {Name: "zulu"}})
			b.AddServer("zulu", "http://127.0.0.1:9002/mcp", []ToolSpec{{Name: "alpha"}, {Name: "tango"}})
		}
		reg, err := b.Build()
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		return reg.Entries()
	}

	wantIDs := []string{"alpha.bravo", "alpha.zulu", "zulu.alpha", "zulu.tango"}
	for _, reverse := range []bool{false, true} {
		entries := build(reverse)
		if len(entries) != len(wantIDs) {
			t.Fatalf("reverse=%t: got %d entries, want %d", reverse, len(entries), len(wantIDs))
		}
		for i, e := range entries {
			if got := e.Server + "." + e.Tool; got != wantIDs[i] {
				t.Errorf("reverse=%t: entries[%d] id = %q, want %q", reverse, i, got, wantIDs[i])
			}
		}
	}
}

// TestFirstWinsTiebreakerOnRenderedIDCollision verifies the last-resort
// defense: when — despite distinct server NAMES — two tools still render to the
// same dotted id, the first is kept and a warning is recorded for the caller to
// surface. This is only reachable when a server name itself contains a dot:
// server "grit.sub" tool "commit" and server "grit" tool "sub.commit" both
// render "grit.sub.commit". Distinct names alone can't collide, so this is the
// closest reachable case rather than a manufactured impossible one.
func TestFirstWinsTiebreakerOnRenderedIDCollision(t *testing.T) {
	var b Builder
	b.AddServer("grit.sub", "http://127.0.0.1:9001/mcp", []ToolSpec{
		{Name: "commit", Description: "kept: first wins"},
	})
	b.AddServer("grit", "http://127.0.0.1:9002/mcp", []ToolSpec{
		{Name: "sub.commit", Description: "dropped: rendered-id collision"},
	})
	reg, err := b.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	entry, ok := reg.Lookup("grit.sub.commit")
	if !ok {
		t.Fatal("Lookup(grit.sub.commit): not found")
	}
	if entry.Description != "kept: first wins" {
		t.Errorf("Description = %q, want the first-added entry (first wins)", entry.Description)
	}

	warnings := reg.Warnings()
	if len(warnings) != 1 {
		t.Fatalf("Warnings() = %v, want exactly one collision warning", warnings)
	}
	if !strings.Contains(warnings[0], "grit.sub.commit") {
		t.Errorf("warning %q does not name the colliding id grit.sub.commit", warnings[0])
	}
}

// TestWarningsReturnsDefensiveCopy verifies Warnings() hands back a fresh slice
// per call rather than aliasing the Registry's internal one: the aggregator's
// verbs read the Registry concurrently, so a caller mutating the returned slice
// (overwrite, append) must not corrupt what every other reader sees.
func TestWarningsReturnsDefensiveCopy(t *testing.T) {
	var b Builder
	b.AddServer("grit.sub", "http://127.0.0.1:9001/mcp", []ToolSpec{{Name: "commit"}})
	b.AddServer("grit", "http://127.0.0.1:9002/mcp", []ToolSpec{{Name: "sub.commit"}})
	reg, err := b.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	w1 := reg.Warnings()
	if len(w1) != 1 {
		t.Fatalf("Warnings() = %v, want exactly one warning", w1)
	}
	original := w1[0]

	// Corrupt the returned slice in every way an aliasing bug would leak:
	// overwrite an element, then append past its length.
	w1[0] = "corrupted"
	w1 = append(w1, "extra")

	w2 := reg.Warnings()
	if len(w2) != 1 {
		t.Fatalf("second Warnings() = %v, want length 1 unaffected by first caller's append", w2)
	}
	if w2[0] != original {
		t.Errorf("second Warnings()[0] = %q, want %q — first caller's mutation leaked", w2[0], original)
	}
}

// TestEntriesReturnsDefensiveCopy mirrors the Warnings non-aliasing guarantee
// for Entries — cheap to assert and it pins the same concurrent-reader
// invariant for the mcp_list path.
func TestEntriesReturnsDefensiveCopy(t *testing.T) {
	var b Builder
	b.AddServer("grit", "http://127.0.0.1:9001/mcp", []ToolSpec{{Name: "commit"}})
	reg, err := b.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	e1 := reg.Entries()
	if len(e1) != 1 {
		t.Fatalf("Entries() = %v, want one entry", e1)
	}
	e1[0].Tool = "corrupted"

	e2 := reg.Entries()
	if e2[0].Tool != "commit" {
		t.Errorf("second Entries()[0].Tool = %q, want commit — first caller's mutation leaked", e2[0].Tool)
	}
}

// TestBuildNoCollisionsHasNoWarnings verifies the common case records nothing —
// Warnings() is empty when every id is unique, so a non-empty slice is a real
// signal rather than routine noise.
func TestBuildNoCollisionsHasNoWarnings(t *testing.T) {
	var b Builder
	b.AddServer("grit", "http://127.0.0.1:9001/mcp", []ToolSpec{{Name: "commit"}, {Name: "status"}})
	reg, err := b.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if w := reg.Warnings(); len(w) != 0 {
		t.Errorf("Warnings() = %v, want empty", w)
	}
}
