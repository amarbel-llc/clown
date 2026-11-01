package main

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/agentscope"
)

// An explicit empty egress_allow means deny everything; only an unset
// (nil) one means clown's default list.
func TestAgentEgressAllowlistExplicitEmptySpecDeniesEverything(t *testing.T) {
	got, err := agentEgressAllowlist([]string{}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || got.Allows("api.anthropic.com:443") {
		t.Errorf("allowlist %v: want empty, denying even the model API", got)
	}
}

// The base-URL host is clown's own model routing, so an empty list still
// carries it (and nothing else).
func TestAgentEgressAllowlistExplicitEmptySpecKeepsARemoteBaseURL(t *testing.T) {
	got, err := agentEgressAllowlist([]string{}, "https://gateway.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, agentscope.Allowlist{"gateway.example.com:443"}) {
		t.Errorf("allowlist %v, want exactly the gateway", got)
	}
}

// spinclass writes egress_allow through a pointer with omitempty: unset
// omits the key, an explicit list (even empty) writes it. This pins the
// omitted/empty/listed distinction from the decoded-spec side.
func TestAgentEgressAllowlistFromASpinclassShapedSpec(t *testing.T) {
	base := t.TempDir()
	spec := func(egress string) agentscope.Spec {
		t.Helper()
		raw := `{"version":1,"worktree":` + quote(filepath.Join(base, "wt")) +
			`,"operator_home":` + quote(filepath.Join(base, "home")) + egress + `}`
		s, err := agentscope.ParseSpec("spec.json", []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	cases := map[string]struct {
		egress string
		base   string
		want   agentscope.Allowlist
	}{
		"omitted gives the default": {"", "", agentscope.DefaultEgressAllow},
		"empty list denies":         {`,"egress_allow":[]`, "", agentscope.Allowlist{}},
		"a list is exactly that list plus the base-URL host": {
			`,"egress_allow":["api.anthropic.com:443","cache.example.org:443"]`, "https://gateway.example.com",
			agentscope.Allowlist{"api.anthropic.com:443", "cache.example.org:443", "gateway.example.com:443"},
		},
	}
	for name, c := range cases {
		got, err := agentEgressAllowlist(spec(c.egress).EgressAllow, c.base, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: allowlist %v, want %v", name, got, c.want)
		}
	}
}

func quote(s string) string { return `"` + s + `"` }

// The effective list is logged once, with where it came from and nothing
// from the environment.
func TestAgentEgressAllowlistLogsTheEffectiveListOnce(t *testing.T) {
	for name, c := range map[string]struct {
		spec       []string
		wantSource string
	}{
		"default": {nil, "source=default"},
		"spec":    {[]string{"api.anthropic.com:443"}, "source=spec"},
		"empty":   {[]string{}, "source=spec"},
	} {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))
		got, err := agentEgressAllowlist(c.spec, "https://gateway.example.com", logger)
		if err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if n := strings.Count(out, "egress allowlist"); n != 1 {
			t.Errorf("%s: logged %d times, want once:\n%s", name, n, out)
		}
		if !strings.Contains(out, c.wantSource) || !strings.Contains(out, strings.Join(got, ",")) {
			t.Errorf("%s: log %q lacks %s or the list %v", name, out, c.wantSource, got)
		}
	}
}

func TestAgentEgressAllowlistRefusesALoopbackSpecEntry(t *testing.T) {
	for _, entry := range []string{"127.0.0.1:8080", "localhost:11434", "[::1]:9000", "0.0.0.0:80"} {
		_, err := agentEgressAllowlist([]string{"api.anthropic.com:443", entry}, "", nil)
		if err == nil || !strings.Contains(err.Error(), entry) || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("entry %q: want a refusal naming it and the loopback, got %v", entry, err)
		}
	}
}

func TestAgentEgressAllowlistSkipsALoopbackBaseURL(t *testing.T) {
	for _, base := range []string{"http://127.0.0.1:8080", "http://localhost:11434/v1", "https://[::1]"} {
		got, err := agentEgressAllowlist(nil, base, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, agentscope.Allowlist(agentscope.DefaultEgressAllow)) {
			t.Errorf("base %q: allowlist %v, want the default unchanged", base, got)
		}
	}
}

func TestAgentEgressAllowlistAddsARemoteBaseURL(t *testing.T) {
	got, err := agentEgressAllowlist(nil, "https://gateway.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Allows("gateway.example.com:443") || !got.Allows("api.anthropic.com:443") {
		t.Errorf("allowlist %v: want the default plus the gateway", got)
	}
	if len(agentscope.DefaultEgressAllow) != len(got)-1 {
		t.Errorf("the default allowlist was modified in place")
	}
}
