package pluginhost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	// Monitors declares background shell commands whose stdout lines
	// Claude Code streams into the chat as notifications. clown does
	// not spawn or supervise monitors; the compile step injects this
	// array verbatim into the produced .claude-plugin/plugin.json so
	// Claude Code (>= 2.1.105) handles spawning, ${...} substitution,
	// and lifecycle. See man clown-json(5) and Anthropic's plugin
	// monitors reference.
	Monitors []MonitorDef `json:"monitors,omitempty"`
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

// MonitorDef mirrors Anthropic's plugin monitor schema exactly. Fields
// are passed through to the compiled .claude-plugin/plugin.json
// verbatim; clown does no ${...} substitution, no spawning, and no
// supervision. Claude Code owns the runtime.
type MonitorDef struct {
	Name        string `json:"name"`
	Command     string `json:"command"`
	Description string `json:"description"`
	// When controls activation. Empty (default) and "always" both mean
	// the monitor starts at session start and on plugin reload.
	// "on-skill-invoke:<skill-name>" defers the start until the named
	// skill is dispatched. clown validates the prefix form but does
	// not look up the skill name.
	When string `json:"when,omitempty"`
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
	if err := validateMonitors(cfg.Monitors); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	applyDefaults(&cfg)
	return &cfg, nil
}

// monitorWhenSkillRE matches the on-skill-invoke:<skill-name> form
// permitted in MonitorDef.When. Skill names allow ASCII alphanumerics,
// underscores, and hyphens, which is the widest character set
// Anthropic's plugin namespacing has used; tighten if upstream
// narrows it.
var monitorWhenSkillRE = regexp.MustCompile(`^on-skill-invoke:[A-Za-z0-9_-]+$`)

func validateMonitors(monitors []MonitorDef) error {
	seen := make(map[string]struct{}, len(monitors))
	for i, m := range monitors {
		if m.Name == "" {
			return fmt.Errorf("monitors[%d]: name is required", i)
		}
		if _, dup := seen[m.Name]; dup {
			return fmt.Errorf("monitors[%d]: duplicate name %q", i, m.Name)
		}
		seen[m.Name] = struct{}{}
		if m.Command == "" {
			return fmt.Errorf("monitors[%d] (%s): command is required", i, m.Name)
		}
		if m.Description == "" {
			return fmt.Errorf("monitors[%d] (%s): description is required", i, m.Name)
		}
		if m.When != "" && m.When != "always" && !monitorWhenSkillRE.MatchString(m.When) {
			return fmt.Errorf(`monitors[%d] (%s): when=%q is not "always" or "on-skill-invoke:<skill>"`, i, m.Name, m.When)
		}
	}
	return nil
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
// pluginDir is the absolute plugin directory. When stdio.Command is
// relative (does not start with `/`), Desugar prepends pluginDir so
// the bridge's `--command` arg points at the plugin-shipped binary
// regardless of the bridge's runtime CWD. This mirrors the
// absolutization that ManagedServer.Start applies to ServerDef.Command
// for the bridge binary itself, but reaches the wrapped command too.
//
// Idempotent on cfg.StdioServers == nil / empty.
func Desugar(cfg *ClownConfig, bridgePath, pluginDir string) error {
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
		cmdArg := stdio.Command
		if cmdArg != "" && !strings.HasPrefix(cmdArg, "/") && pluginDir != "" {
			cmdArg = pluginDir + "/" + cmdArg
		}
		args := []string{"--command", cmdArg, "--"}
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
