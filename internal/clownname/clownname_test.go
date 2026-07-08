package clownname

import "testing"

func TestAllocatePicksFirstFreeBaseName(t *testing.T) {
	live := map[string]bool{Pool[0]: true}
	got := Allocate(live)
	if got != Pool[1] {
		t.Fatalf("Allocate() = %q, want the second pool entry %q (first is taken)", got, Pool[1])
	}
}

func TestAllocateEmptyLiveSetPicksFirstPoolEntry(t *testing.T) {
	if got := Allocate(map[string]bool{}); got != Pool[0] {
		t.Fatalf("Allocate() = %q, want the first pool entry %q", got, Pool[0])
	}
}

func TestAllocateWholePoolTakenUsesGenerationSuffix(t *testing.T) {
	live := map[string]bool{}
	for _, name := range Pool {
		live[name] = true
	}
	got := Allocate(live)
	want := Pool[0] + "-2"
	if got != want {
		t.Fatalf("Allocate() with full pool = %q, want the first generation suffix %q", got, want)
	}
}

func TestAllocatePicksLowestFreeGeneration(t *testing.T) {
	live := map[string]bool{}
	for _, name := range Pool {
		live[name] = true
	}
	live[Pool[0]+"-2"] = true // generation 2 taken; 3 should be picked next
	got := Allocate(live)
	want := Pool[0] + "-3"
	if got != want {
		t.Fatalf("Allocate() = %q, want the lowest free generation %q", got, want)
	}
}

// Disjunct identity (clown#169): a reused base name's new generation is a
// wholly distinct string from the original — nothing else needs to track
// "generation" as a separate concept.
func TestAllocateGenerationNamesAreDisjunctFromBase(t *testing.T) {
	live := map[string]bool{}
	for _, name := range Pool {
		live[name] = true
	}
	got := Allocate(live)
	if got == Pool[0] {
		t.Fatalf("Allocate() with full pool must not return the (taken) base name %q", Pool[0])
	}
}

func TestPoolIsNonEmpty(t *testing.T) {
	if len(Pool) == 0 {
		t.Fatal("Pool must not be empty — Allocate has no fallback for an empty pool beyond a degenerate clown-1")
	}
}
