package pluginhost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type ClownConfig struct {
	Version     int                  `json:"version"`
	HTTPServers map[string]ServerDef `json:"httpServers"`
}

type ServerDef struct {
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	Transport   string            `json:"transport"`
	Healthcheck HealthcheckDef    `json:"healthcheck"`
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

type MCPConfig struct {
	MCPServers map[string]MCPServerEntry `json:"mcpServers"`
}

// MCPServerEntry mirrors one entry in claude-code's mcpServers map. The
// Type discriminator is required by claude-code's MCP configuration
// schema; valid values for HTTP-transport servers are "http" and "sse".
type MCPServerEntry struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

func GenerateMCPConfig(entries map[string]MCPServerEntry) ([]byte, error) {
	cfg := MCPConfig{MCPServers: entries}
	return json.MarshalIndent(cfg, "", "  ")
}
