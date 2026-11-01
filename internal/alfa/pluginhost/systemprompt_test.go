package pluginhost

import (
	"context"
	"testing"
)

// TestFetchPromptFragments drives Host.FetchPromptFragments against real
// started servers: one opted in (path serves a 200 body), one opted in at a
// missing path (404 → skipped), and one not opted in at all (skipped). Only
// the first contributes a fragment, proving the opt-in gate and the
// degrade-on-non-200 behavior over real HTTP.
func TestFetchPromptFragments(t *testing.T) {
	optedIn := newTestServer(t, "sleep")
	optedIn.Name = "plugin/opted-in"
	optedIn.Def.SystemPromptPath = "/clown/system-prompt"

	missingPath := newTestServer(t, "sleep")
	missingPath.Name = "plugin/missing-path"
	missingPath.Def.SystemPromptPath = "/no-such-endpoint"

	notOptedIn := newTestServer(t, "sleep")
	notOptedIn.Name = "plugin/static-only"

	ctx := context.Background()
	for _, s := range []*ManagedServer{optedIn, missingPath, notOptedIn} {
		if err := s.Start(ctx); err != nil {
			t.Fatalf("start %s: %v", s.Name, err)
		}
		defer s.Stop()
	}

	h := &Host{Servers: []*ManagedServer{optedIn, missingPath, notOptedIn}}
	frags := h.FetchPromptFragments(ctx)
	if len(frags) != 1 {
		t.Fatalf("want exactly 1 fragment (only the opted-in 200 server), got %d: %v", len(frags), frags)
	}
	if frags[0] != "FAKE-FRAGMENT" {
		t.Errorf("fragment = %q, want %q", frags[0], "FAKE-FRAGMENT")
	}
}

// TestFetchPromptFragmentsNoneOptedIn confirms a host whose servers all run
// static-only returns no fragments (no fetch attempted).
func TestFetchPromptFragmentsNoneOptedIn(t *testing.T) {
	s := newTestServer(t, "sleep")
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()
	h := &Host{Servers: []*ManagedServer{s}}
	if frags := h.FetchPromptFragments(context.Background()); len(frags) != 0 {
		t.Errorf("want no fragments, got %v", frags)
	}
}
