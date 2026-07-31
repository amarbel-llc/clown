package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"code.linenisgreat.com/clown/internal/pluginhost"
	"code.linenisgreat.com/clown/internal/staging"
)

// recordingExecutor captures whatever FormatArgs was handed. The compile-time
// Executor assertion below is half the point: it fails to build unless the
// interface actually carries the env alongside the argv.
type recordingExecutor struct {
	gotArgs []string
	gotEnv  []string
}

var _ Executor = (*recordingExecutor)(nil)

func (e *recordingExecutor) Binary() (string, error) { return "/bin/true", nil }

func (e *recordingExecutor) FormatArgs(cmd Command) (Command, error) {
	e.gotArgs = cmd.Args
	e.gotEnv = cmd.Env
	return cmd, nil
}

// The #205 regression test. An executor that rewrites argv MUST be handed the
// env too — that is the whole point of the type. Pre-Command, argv went through
// FormatArgs (so tentExecutor rewrote it) while env went straight to
// runProvider, so a containerizing executor could not even see what it was
// failing to translate.
func TestExecutor_FormatArgsReceivesEnv(t *testing.T) {
	e := &recordingExecutor{}
	_, err := e.FormatArgs(Command{
		Args: []string{"--version"},
		Env:  []string{"OPENCODE_CONFIG=/stage/opencode.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.gotEnv, []string{"OPENCODE_CONFIG=/stage/opencode.json"}) {
		t.Errorf("executor did not receive env: %v", e.gotEnv)
	}
	if !slices.Equal(e.gotArgs, []string{"--version"}) {
		t.Errorf("executor did not receive args: %v", e.gotArgs)
	}
}

// claudeBinding is an extraction, not a rewrite: on the fallback paths it must
// produce byte-for-byte what the pre-seam code produced, which was literally
// prependPluginDirs(baseArgs, pluginDirs, nil). Comparing against that call
// keeps the two from drifting even if prependPluginDirs itself changes.
func TestClaudeBinding_NilServersMatchesLegacyFallback(t *testing.T) {
	baseArgs := []string{"--resume", "abc"}
	dirs := []string{"/plugins/a", "/plugins/b"}

	b := &claudeBinding{baseArgs: baseArgs, pluginDirs: dirs}
	got, err := b.Bind(nil, nil, nil)
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
	got, err := b.Bind(nil, nil, []pluginhost.DiscoveredServer{})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	want := prependPluginDirs([]string{"x"}, []string{"/p"}, nil)
	if !slices.Equal(got.Args, want) {
		t.Errorf("got %v, want %v", got.Args, want)
	}
}

// discoveredForTest builds a DiscoveredServer with a matching started
// ManagedServer name, so collapseUpstreamsFor / the exclusion logic can pair
// them the way runManaged does.
func discoveredForTest(pluginName, serverName, pluginDir string) pluginhost.DiscoveredServer {
	return pluginhost.DiscoveredServer{
		PluginDir:  pluginDir,
		PluginName: pluginName,
		ServerName: serverName,
	}
}

// --mcp-collapse: collapseBinding.Bind must hand claude EXACTLY the synthesized
// aggregator plugin dir plus any non-upstream dirs — never the upstream dirs
// (those are fronted by the aggregator, one-in-N-out) and never more than one
// aggregator entry. The compiled aggregator plugin.json must carry a single
// mcpServers entry pointing at the running aggregator's URL.
func TestCollapseBinding_SingleAggregatorCommand(t *testing.T) {
	root, err := staging.New(t.TempDir())
	if err != nil {
		t.Fatalf("staging.New: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	host := &pluginhost.Host{}
	agg := pluginhost.NewStartedServerForTest("clown-mcp-collapse", "127.0.0.1:6001", "streamable-http")

	// Two upstream dirs (must be dropped) and one non-server dir like the job
	// monitor (must be kept).
	upstreamDirA := "/plugins/moxy"
	upstreamDirB := "/plugins/dodder"
	monitorDir := "/plugins/clown-builtin-jobs"
	baseArgs := []string{"--resume", "abc"}
	pluginDirs := []string{upstreamDirA, upstreamDirB, monitorDir}

	allDiscovered := []pluginhost.DiscoveredServer{
		discoveredForTest("moxy", "moxy", upstreamDirA),
		discoveredForTest("dodder", "dodder", upstreamDirB),
	}

	b := newCollapseBinding(baseArgs, pluginDirs, agg, root, nil)
	got, err := b.Bind(host, allDiscovered, allDiscovered)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if got.Env != nil {
		t.Errorf("collapse binding must contribute no env, got %v", got.Env)
	}

	// Collect the --plugin-dir values from the argv.
	var dirs []string
	for i := 0; i < len(got.Args); i++ {
		if got.Args[i] == "--plugin-dir" && i+1 < len(got.Args) {
			dirs = append(dirs, got.Args[i+1])
			i++
		}
	}

	// The two upstream dirs must be gone.
	for _, d := range dirs {
		if d == upstreamDirA || d == upstreamDirB {
			t.Errorf("upstream dir %q leaked into --plugin-dir; it should be fronted by the aggregator", d)
		}
	}
	// The monitor dir must survive.
	if !slices.Contains(dirs, monitorDir) {
		t.Errorf("non-upstream dir %q was dropped; only upstream dirs should be excluded", monitorDir)
	}
	// Exactly one aggregator dir (the one NOT in the original pluginDirs).
	var aggDirs []string
	for _, d := range dirs {
		if d != monitorDir {
			aggDirs = append(aggDirs, d)
		}
	}
	if len(aggDirs) != 1 {
		t.Fatalf("expected exactly one synthesized aggregator --plugin-dir, got %v (all dirs: %v)", aggDirs, dirs)
	}
	aggDir := aggDirs[0]

	// The base args must be preserved after the --plugin-dir flags.
	if !slices.Equal(got.Args[len(got.Args)-len(baseArgs):], baseArgs) {
		t.Errorf("base args not preserved at tail: %v", got.Args)
	}

	// The compiled aggregator plugin.json must carry exactly one mcpServers
	// entry, pointing at the running aggregator's URL.
	manifestPath := filepath.Join(aggDir, ".claude-plugin", "plugin.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading compiled aggregator plugin.json: %v", err)
	}
	var doc struct {
		Name       string                               `json:"name"`
		McpServers map[string]pluginhost.MCPServerEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing compiled plugin.json: %v", err)
	}
	if doc.Name != "clown-mcp-collapse" {
		t.Errorf("aggregator plugin name = %q, want clown-mcp-collapse", doc.Name)
	}
	if len(doc.McpServers) != 1 {
		t.Fatalf("aggregator manifest has %d mcpServers entries, want exactly 1: %v", len(doc.McpServers), doc.McpServers)
	}
	for _, entry := range doc.McpServers {
		wantURL := agg.Handshake().URL()
		if entry.URL != wantURL {
			t.Errorf("aggregator mcpServers URL = %q, want %q", entry.URL, wantURL)
		}
		if entry.Type != "http" {
			t.Errorf("aggregator mcpServers type = %q, want http", entry.Type)
		}
	}
}

// The off-path safety proof: a collapseBinding with a nil aggregator (the
// fallback contract — nil host / no selected servers) must produce EXACTLY what
// the plain claude fallback produces. This guards the invariant that
// --mcp-collapse degrades to unchanged behavior when there is nothing to
// collapse, and that the collapse machinery never rewrites the default argv.
func TestCollapseBinding_NilAggregatorMatchesLegacyFallback(t *testing.T) {
	baseArgs := []string{"--resume", "abc"}
	dirs := []string{"/plugins/a", "/plugins/b"}

	b := newCollapseBinding(baseArgs, dirs, nil, nil, nil)
	got, err := b.Bind(nil, nil, nil)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	want := prependPluginDirs(baseArgs, dirs, nil)
	if !slices.Equal(got.Args, want) {
		t.Errorf("fallback argv drifted:\n got %v\nwant %v", got.Args, want)
	}
	if got.Env != nil {
		t.Errorf("collapse binding must contribute no env, got %v", got.Env)
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

	got, err := b.Bind(nil, nil, nil)
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
	if _, err := b.Bind(nil, nil, nil); !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want %v", err, sentinel)
	}
}
