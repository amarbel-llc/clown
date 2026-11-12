package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// Off by default: with the switch unset (or anything but "1") no token is
// minted and nothing is said.
func TestBridgeTokenIsOffByDefault(t *testing.T) {
	warned := false
	warn := func(string) { warned = true }
	for _, env := range []map[string]string{{}, {bridgeAuthEnv: "0"}, {bridgeAuthEnv: "true"}} {
		if got := bridgeTokenFor(parsedFlags{provider: "claude"}, envOf(env), bytes.NewReader(make([]byte, 64)), warn); got != "" {
			t.Errorf("env %v minted a token", env)
		}
	}
	if warned {
		t.Error("the default launch warned")
	}
}

// Opted in on a plain claude launch: a 64-hex-character token from the
// entropy source, no warning.
func TestBridgeTokenIsMintedWhenOptedIn(t *testing.T) {
	var warnings []string
	src := bytes.NewReader(bytes.Repeat([]byte{0xab}, 32))
	got := bridgeTokenFor(parsedFlags{provider: "claude"}, envOf(map[string]string{bridgeAuthEnv: "1"}), src, func(m string) { warnings = append(warnings, m) })
	if got != strings.Repeat("ab", 32) {
		t.Errorf("token = %q, want 32 bytes as hex", got)
	}
	if len(warnings) != 0 {
		t.Errorf("warned: %v", warnings)
	}
}

// A confined launch (--agent-scope) gets a token like any other claude launch.
func TestBridgeTokenIsMintedForAConfinedLaunch(t *testing.T) {
	src := bytes.NewReader(bytes.Repeat([]byte{0xcd}, 32))
	got := bridgeTokenFor(parsedFlags{provider: "claude", agentScopeSpec: "/spec"}, envOf(map[string]string{bridgeAuthEnv: "1"}), src, func(m string) { t.Errorf("warned: %s", m) })
	if got != strings.Repeat("cd", 32) {
		t.Errorf("confined launch token = %q, want 32 bytes as hex", got)
	}
}

// Launches whose bridges or clients would not send the token do not get one,
// and the warning names why but never the token.
func TestBridgeTokenRefusedWhereItCannotBeSent(t *testing.T) {
	cases := map[string]parsedFlags{
		"provider":     {provider: "opencode"},
		"mcp-collapse": {provider: "claude", mcpCollapse: true},
	}
	for name, flags := range cases {
		var warnings []string
		got := bridgeTokenFor(flags, envOf(map[string]string{bridgeAuthEnv: "1"}), bytes.NewReader(bytes.Repeat([]byte{1}, 32)), func(m string) { warnings = append(warnings, m) })
		if got != "" {
			t.Errorf("%s: minted a token", name)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], bridgeAuthEnv) || strings.Contains(warnings[0], "0101") {
			t.Errorf("%s: warnings = %v", name, warnings)
		}
	}
}

func TestBridgeTokenNeedsRandomness(t *testing.T) {
	var warnings []string
	got := bridgeTokenFor(parsedFlags{provider: "claude"}, envOf(map[string]string{bridgeAuthEnv: "1"}), errReader{}, func(m string) { warnings = append(warnings, m) })
	if got != "" || len(warnings) != 1 {
		t.Errorf("token %q, warnings %v; want none and one warning", got, warnings)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }
