package pluginhost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type ClownConfig struct {
	Version int `json:"version"`
	// HTTPServers declares MCP servers that already speak the
	// streamable-HTTP (or sse) transport. clown-plugin-host launches
	// each server, reads its handshake, and exposes it to Claude Code
	// via the compiled plugin.json's mcpServers block.
	HTTPServers map[string]ServerDef `json:"httpServers"`
	// StdioServers declares MCP servers that speak JSON-RPC over their
	// own stdin/stdout. After parsing, Desugar transforms each entry
	// into a synthesized HTTPServers entry whose command is the
	// clown-stdio-bridge binary, so the rest of clown-plugin-host
	// (discovery, lifecycle, manifest compilation) treats them
	// uniformly with native HTTP servers. See FDR 0002.
	StdioServers map[string]StdioServerDef `json:"stdioServers,omitempty"`
}

type ServerDef struct {
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	Transport   string            `json:"transport"`
	Healthcheck HealthcheckDef    `json:"healthcheck"`
	// Timeout, when non-zero, is forwarded verbatim into the compiled
	// plugin.json's mcpServers.<name>.timeout key. Claude Code reads
	// this to override its default 60 s per-tool MCP request timeout.
	// Units are milliseconds, matching Claude Code's wire format.
	Timeout int `json:"timeout,omitempty"`
}

// StdioServerDef declares a stdio MCP server that clown will bridge to
// HTTP. Schema parallels ServerDef but omits transport (always stdio)
// and healthcheck (the bridge synthesizes startup readiness).
type StdioServerDef struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// Timeout has the same semantics as ServerDef.Timeout: when
	// non-zero, propagates to the compiled plugin.json entry.
	Timeout int `json:"timeout,omitempty"`
}

type HealthcheckDef struct {
	Path     string       `json:"path"`
	Interval JSONDuration `json:"interval"`
	Timeout  JSONDuration `json:"timeout"`
}

type JSONDuration struct {
	time.Duration
}

func (d *JSONDuration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = dur
	return nil
}

func (d JSONDuration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Duration.String())
}

func LoadClownConfig(pluginDir string) (*ClownConfig, error) {
	path := filepath.Join(pluginDir, "clown.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg ClownConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.Version != 1 {
		return nil, fmt.Errorf("%s: unsupported version %d (expected 1)", path, cfg.Version)
	}
	applyDefaults(&cfg)
	return &cfg, nil
}

func applyDefaults(cfg *ClownConfig) {
	for name, srv := range cfg.HTTPServers {
		if srv.Transport == "" {
			srv.Transport = "streamable-http"
		}
		if srv.Healthcheck.Path == "" {
			srv.Healthcheck.Path = "/healthz"
		}
		if srv.Healthcheck.Interval.Duration == 0 {
			srv.Healthcheck.Interval.Duration = 1 * time.Second
		}
		if srv.Healthcheck.Timeout.Duration == 0 {
			srv.Healthcheck.Timeout.Duration = 30 * time.Second
		}
		if srv.Args == nil {
			srv.Args = []string{}
		}
		if srv.Env == nil {
			srv.Env = map[string]string{}
		}
		cfg.HTTPServers[name] = srv
	}
}

func PluginName(pluginDir string) (string, error) {
	path := filepath.Join(pluginDir, ".claude-plugin", "plugin.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading plugin manifest: %w", err)
	}
	var manifest struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", fmt.Errorf("parsing %s: %w", path, err)
	}
	if manifest.Name == "" {
		return "", fmt.Errorf("%s: missing or empty name field", path)
	}
	return manifest.Name, nil
}

// Desugar transforms each StdioServers entry in cfg into a synthesized
// HTTPServers entry whose command is bridgePath, so the rest of
// clown-plugin-host can treat stdio-declared servers uniformly with
// native HTTP servers.
//
// Mutates cfg in place: synthesized entries are inserted into
// cfg.HTTPServers, cfg.StdioServers is cleared to nil, and
// applyDefaults is called. Callers MUST NOT rely on cfg.StdioServers
// retaining its prior contents after Desugar returns.
//
// bridgePath MUST be the absolute path to the clown-stdio-bridge
// binary. Returns an error if bridgePath is empty while
// cfg.StdioServers is non-empty, or if a name in cfg.StdioServers
// collides with a name already in cfg.HTTPServers.
//
// Idempotent on cfg.StdioServers == nil / empty.
func Desugar(cfg *ClownConfig, bridgePath string) error {
	if len(cfg.StdioServers) == 0 {
		return nil
	}
	if bridgePath == "" {
		return fmt.Errorf("clown.json declares stdioServers but bridge path is unset")
	}
	if cfg.HTTPServers == nil {
		cfg.HTTPServers = map[string]ServerDef{}
	}
	for name, stdio := range cfg.StdioServers {
		if _, conflict := cfg.HTTPServers[name]; conflict {
			return fmt.Errorf("server name %q is declared in both httpServers and stdioServers", name)
		}
		args := []string{"--command", stdio.Command, "--"}
		args = append(args, stdio.Args...)
		cfg.HTTPServers[name] = ServerDef{
			Command:   bridgePath,
			Args:      args,
			Env:       stdio.Env,
			Transport: "streamable-http",
			Timeout:   stdio.Timeout,
			Healthcheck: HealthcheckDef{
				Path:     "/healthz",
				Interval: JSONDuration{Duration: 1 * time.Second},
				Timeout:  JSONDuration{Duration: 30 * time.Second},
			},
		}
	}
	cfg.StdioServers = nil
	applyDefaults(cfg)
	return nil
}

// MCPServerEntry mirrors one entry in claude-code's mcpServers map. The
// Type discriminator is required by claude-code's MCP configuration
// schema; valid values for HTTP-transport servers are "http" and "sse".
//
// Timeout, when non-zero, overrides claude-code's default per-tool
// MCP request timeout. Units are milliseconds, matching claude-code's
// wire format. Omitted from the marshalled output when zero so plugins
// that do not set the field on the input side keep claude-code's
// default behavior.
type MCPServerEntry struct {
	Type    string `json:"type"`
	URL     string `json:"url"`
	Timeout int    `json:"timeout,omitempty"`
}
