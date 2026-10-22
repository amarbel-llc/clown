package main

import (
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/agentscope"
)

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
