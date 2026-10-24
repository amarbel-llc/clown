package main

import (
	"slices"
	"strings"
	"testing"
)

// clown#294: troupe's and ringmaster's dynamic fragments each open with their
// own "## Session Identity"; clown composes them under one heading.

const troupeFragment = "## Session Identity\n\nThis session's per-instance key is `k1` — the `sessionKey` it shows up as in `chat_list`.\n\n## troupe messaging (live)\n\nChat tools."

const ringmasterFragment = "## Session Identity\n\nThis session's per-instance key is `k1` — the `target` other sessions use to aim a job or a wake at it on the platform.\n\n## clown job platform (live)\n\nJob tools."

func TestMergeSessionIdentityKeepsOneHeadingAndBothBodies(t *testing.T) {
	got := strings.Join(mergeSessionIdentity([]string{troupeFragment, ringmasterFragment}), "\n\n")
	if n := strings.Count(got, "## Session Identity"); n != 1 {
		t.Errorf("%d Session Identity headings, want 1:\n%s", n, got)
	}
	for _, want := range []string{"`sessionKey` it shows up as in `chat_list`", "the `target` other sessions use", "## troupe messaging (live)", "## clown job platform (live)", "Chat tools.", "Job tools."} {
		if !strings.Contains(got, want) {
			t.Errorf("merged prompt lost %q:\n%s", want, got)
		}
	}
	// The heading comes first, then both bodies, then the platform sections
	// in their original order.
	order := []string{"## Session Identity", "`sessionKey`", "the `target`", "## troupe messaging", "## clown job platform"}
	last := -1
	for _, s := range order {
		i := strings.Index(got, s)
		if i < last {
			t.Errorf("%q is out of order:\n%s", s, got)
		}
		last = i
	}
}

func TestMergeSessionIdentityLeavesOtherFragmentsAlone(t *testing.T) {
	plain := []string{"## moxy\n\nmoxy tools.", "## other\n\nx"}
	if got := mergeSessionIdentity(plain); !slices.Equal(got, plain) {
		t.Errorf("fragments without the heading changed: %q", got)
	}
	single := []string{troupeFragment, "## moxy\n\nmoxy tools."}
	if got := mergeSessionIdentity(single); !slices.Equal(got, single) {
		t.Errorf("a single Session Identity changed: %q", got)
	}
	// Identical bodies collapse to one.
	same := mergeSessionIdentity([]string{troupeFragment, troupeFragment})
	if n := strings.Count(strings.Join(same, "\n\n"), "`sessionKey` it shows up as"); n != 1 {
		t.Errorf("identical identity body kept %d times: %q", n, same)
	}
	// A fragment that was only the identity section disappears.
	onlyID := "## Session Identity\n\nThis session's per-instance key is `k1`."
	if got := mergeSessionIdentity([]string{troupeFragment, onlyID}); len(got) != 1 {
		t.Errorf("identity-only fragment not dropped: %q", got)
	}
}
