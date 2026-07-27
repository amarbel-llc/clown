package main

import (
	"errors"
	"slices"
	"testing"

	"code.linenisgreat.com/clown/internal/pluginhost"
)

// claudeBinding is an extraction, not a rewrite: on the fallback paths it must
// produce byte-for-byte what the pre-seam code produced, which was literally
// prependPluginDirs(baseArgs, pluginDirs, nil). Comparing against that call
// keeps the two from drifting even if prependPluginDirs itself changes.
func TestClaudeBinding_NilServersMatchesLegacyFallback(t *testing.T) {
	baseArgs := []string{"--resume", "abc"}
	dirs := []string{"/plugins/a", "/plugins/b"}

	b := &claudeBinding{baseArgs: baseArgs, pluginDirs: dirs}
	got, err := b.Bind(nil, nil)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	want := prependPluginDirs(baseArgs, dirs, nil)
	if !slices.Equal(got.Args, want) {
		t.Errorf("fallback argv drifted:\n got %v\nwant %v", got.Args, want)
	}
	if got.Env != nil {
		t.Errorf("claude binding must contribute no env, got %v", got.Env)
	}
}

// An empty (non-nil) discovered slice is the same fallback case as nil.
func TestClaudeBinding_EmptyDiscoveredIsFallback(t *testing.T) {
	b := &claudeBinding{baseArgs: []string{"x"}, pluginDirs: []string{"/p"}}
	got, err := b.Bind(nil, []pluginhost.DiscoveredServer{})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	want := prependPluginDirs([]string{"x"}, []string{"/p"}, nil)
	if !slices.Equal(got.Args, want) {
		t.Errorf("got %v, want %v", got.Args, want)
	}
}

func TestConfigFileBinding_PassesEntriesAndReturnsEnv(t *testing.T) {
	var seen map[string]pluginhost.MCPServerEntry
	called := 0
	b := &configFileBinding{
		baseArgs: []string{"--flag"},
		writeConfig: func(mcp map[string]pluginhost.MCPServerEntry) ([]string, error) {
			seen = mcp
			called++
			return []string{"CFG=/tmp/x.json"}, nil
		},
	}

	got, err := b.Bind(nil, nil)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	// The config must still be written with no servers: the provider needs its
	// model/token config even when clown has no MCP servers to add.
	if called != 1 {
		t.Errorf("writeConfig called %d times on the empty path, want 1", called)
	}
	if len(seen) != 0 {
		t.Errorf("expected no entries, got %v", seen)
	}
	if !slices.Equal(got.Args, []string{"--flag"}) {
		t.Errorf("Args = %v; a config-file provider takes no extra argv", got.Args)
	}
	if !slices.Equal(got.Env, []string{"CFG=/tmp/x.json"}) {
		t.Errorf("Env = %v", got.Env)
	}
}

// A write failure must abort rather than launch the provider against a
// missing or half-written config.
func TestConfigFileBinding_WriteErrorPropagates(t *testing.T) {
	sentinel := errors.New("disk full")
	b := &configFileBinding{
		writeConfig: func(map[string]pluginhost.MCPServerEntry) ([]string, error) {
			return nil, sentinel
		},
	}
	if _, err := b.Bind(nil, nil); !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want %v", err, sentinel)
	}
}
