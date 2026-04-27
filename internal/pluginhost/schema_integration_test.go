package pluginhost_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/clown/internal/pluginhost"
)

// schemaErrorMarker is the substring claude-code prints when an
// mcpServers entry fails JSON-schema validation. Captured from
// claude-code 2.1.111; see #14 for the original repro.
const schemaErrorMarker = "Does not adhere to MCP server configuration schema"

// TestMCPConfigSchema_AgainstClaude pipes generated .mcp.json files
// shaped like pluginhost.MCPServerEntry into a real claude
// invocation and asserts that claude does not reject them with a
// schema-validation error. Regression guard for #14: clown previously
// shipped schema-invalid entries (missing the `type` discriminator)
// and the gap was found only when a user ran clown interactively.
//
// Invocation form: `claude --mcp-config <file> <bogus>`. The variadic
// --mcp-config validates each file; a non-existent extra file forces
// claude into its multi-file error-collection path, which is the only
// form that reliably surfaces schema errors. `mcp list` and friends
// silently drop invalid entries instead of erroring — see the
// `explore-claude-mcp-config-parsing` justfile recipe for the
// transcript that motivated this choice.
//
// Skipped when claude is not on PATH (sandboxed nix builds, CI without
// claude installed).
func TestMCPConfigSchema_AgainstClaude(t *testing.T) {
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude not on PATH; skipping schema integration test")
	}

	cases := []struct {
		name    string
		servers map[string]pluginhost.MCPServerEntry
	}{
		{
			name: "typed-http",
			servers: map[string]pluginhost.MCPServerEntry{
				"test/server": {Type: "http", URL: "http://127.0.0.1:42323/mcp"},
			},
		},
		{
			name: "typed-sse",
			servers: map[string]pluginhost.MCPServerEntry{
				"test/server": {Type: "sse", URL: "http://127.0.0.1:42323/sse"},
			},
		},
		{
			name: "typed-http-with-timeout",
			servers: map[string]pluginhost.MCPServerEntry{
				"test/server": {
					Type:    "http",
					URL:     "http://127.0.0.1:42323/mcp",
					Timeout: 30000,
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := writeMCPConfig(t, tc.servers)
			output := runClaudeMCPCheck(t, claudeBin, cfgPath)
			if strings.Contains(output, schemaErrorMarker) {
				t.Errorf("schema rejection for %s entry; output:\n%s",
					tc.name, output)
			}
		})
	}
}

// TestMCPConfigSchema_BareEntry_PositiveControl verifies that a "bare"
// entry — no `type` discriminator — IS rejected by claude with the
// schema marker. This pairs with TestMCPConfigSchema_AgainstClaude:
// if claude ever loosens the schema and accepts bare entries, this
// test fires and tells us the regression guard above no longer
// exercises real schema validation.
func TestMCPConfigSchema_BareEntry_PositiveControl(t *testing.T) {
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude not on PATH; skipping schema integration test")
	}

	bare := []byte(`{"mcpServers":{"test/server":{"url":"http://127.0.0.1:42323/mcp"}}}`)
	cfgPath := filepath.Join(t.TempDir(), "mcp-bare.json")
	if err := os.WriteFile(cfgPath, bare, 0o600); err != nil {
		t.Fatal(err)
	}
	output := runClaudeMCPCheck(t, claudeBin, cfgPath)
	if !strings.Contains(output, schemaErrorMarker) {
		t.Errorf("expected schema marker for bare entry; got:\n%s", output)
	}
}

func writeMCPConfig(t *testing.T, servers map[string]pluginhost.MCPServerEntry) string {
	t.Helper()
	cfg := struct {
		MCPServers map[string]pluginhost.MCPServerEntry `json:"mcpServers"`
	}{MCPServers: servers}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal mcp config: %v", err)
	}
	p := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatalf("write mcp config: %v", err)
	}
	return p
}

func runClaudeMCPCheck(t *testing.T, claudeBin, cfgPath string) string {
	t.Helper()
	bogus := filepath.Join(t.TempDir(), "__nonexistent.json")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, claudeBin,
		"--mcp-config", cfgPath, bogus)
	// claude prints "Error: Invalid MCP configuration:" and exits
	// non-zero by design — the bogus second file always triggers it.
	out, _ := cmd.CombinedOutput()
	return string(out)
}
