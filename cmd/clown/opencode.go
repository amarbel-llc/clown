package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"

	"code.linenisgreat.com/clown/internal/clownfile"
	"code.linenisgreat.com/clown/internal/pluginhost"
	"code.linenisgreat.com/clown/internal/profile"
	"code.linenisgreat.com/clown/internal/staging"
)

type opencodeLocalConfig struct {
	URL   string
	Token string
}

// readOpencodeLocalConfig reads the user's opencode gateway config from the
// canonical ~/.config/clown/opencode.toml (legacy ~/.config/juggler/ read
// fallback with a warning; see userConfigPath).
func readOpencodeLocalConfig() (opencodeLocalConfig, error) {
	path, legacy, err := userConfigPath("opencode.toml")
	if err != nil {
		return opencodeLocalConfig{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return opencodeLocalConfig{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var cfg opencodeLocalConfig
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"`))
		switch k {
		case "url":
			cfg.URL = v
		case "token":
			cfg.Token = v
		}
	}
	if err := scanner.Err(); err != nil {
		return opencodeLocalConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	if cfg.URL == "" {
		return opencodeLocalConfig{}, fmt.Errorf("%s: url is required", path)
	}
	if cfg.Token == "" {
		return opencodeLocalConfig{}, fmt.Errorf("%s: token is required", path)
	}
	if legacy {
		warnLegacyConfig(path)
	}
	return cfg, nil
}

// writeOpencodeLocalConfigFile writes a minimal ~/.config/clown/opencode.toml
// (url + token) to path, creating the parent directory at 0o700 if missing.
// The token is double-quoted to survive any shell-significant characters when
// users hand-edit it later. URL goes through the same treatment for symmetry.
func writeOpencodeLocalConfigFile(path, url, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	body := fmt.Sprintf("url = %q\ntoken = %q\n", url, token)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// promptOpencodeLocalConfig walks the user through creating a missing
// ~/.config/clown/opencode.toml via huh, writes it on confirmation, and
// returns the parsed values. The caller must have verified
// pluginhost.IsInteractive() before invoking — huh requires a TTY for both
// stdin and stderr.
//
// Returns (cfg, nil) on success. When the user cancels at the confirmation
// step or aborts the form (Ctrl-C), the file is not written and the
// returned error explains what happened so runOpencode can surface a
// non-zero exit cleanly.
func promptOpencodeLocalConfig(path string) (opencodeLocalConfig, error) {
	var (
		url     = "http://localhost:11434/v1"
		token   = "local"
		confirm bool
	)

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().
				Title("Configure opencode").
				Description(fmt.Sprintf(
					"No %s found.\n\nClown will create one for you. Provide the OpenAI-compatible\nbase URL and an API token; defaults assume a local Ollama-style\nendpoint and can be edited later.",
					path,
				)),
			huh.NewInput().
				Title("Base URL").
				Description("OpenAI-compatible /v1 endpoint").
				Placeholder("http://localhost:11434/v1").
				Value(&url).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("url is required")
					}
					return nil
				}),
			huh.NewInput().
				Title("Token").
				Description("API key. Use 'local' if your endpoint does not check it.").
				EchoMode(huh.EchoModePassword).
				Value(&token).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("token is required")
					}
					return nil
				}),
			huh.NewConfirm().
				Title(fmt.Sprintf("Save to %s?", path)).
				Affirmative("Save").
				Negative("Cancel").
				Value(&confirm),
		),
	)

	if err := form.Run(); err != nil {
		return opencodeLocalConfig{}, fmt.Errorf("prompt: %w", err)
	}
	if !confirm {
		return opencodeLocalConfig{}, fmt.Errorf("aborted by user; %s not written", path)
	}

	url = strings.TrimSpace(url)
	token = strings.TrimSpace(token)
	if err := writeOpencodeLocalConfigFile(path, url, token); err != nil {
		return opencodeLocalConfig{}, err
	}
	return opencodeLocalConfig{URL: url, Token: token}, nil
}

// writeOpencodeConfigFile writes the synthesized provider config to the
// given file path. Clown points opencode at it via OPENCODE_CONFIG rather
// than hijacking XDG_CONFIG_HOME — XDG_CONFIG_HOME also shadows opencode's
// data-dir derivation, which makes opencode believe each launch is a fresh
// install and re-run its one-time database migration.
// opencodeEnv returns the environment entries clown sets for an opencode
// launch.
//
// OPENCODE_DISABLE_PROJECT_CONFIG is what makes cfgPath authoritative. opencode
// merges every project-level opencode.json AFTER the OPENCODE_CONFIG file
// (packages/opencode/src/config/config.ts:406-409), and verified 2026-07-27
// that a repo-local config REPLACES a same-named entry in clown's `mcp` map —
// so without the suppression any repository clown runs in could silently
// repoint a clown-managed MCP server at a URL of its choosing.
//
// The user's own GLOBAL config still merges, and merges BEFORE clown's, so
// clown still wins there. Only the per-repo file is suppressed, because that is
// the one an untrusted repository controls.
func opencodeEnv(cfgPath string, hermetic bool) []string {
	env := []string{"OPENCODE_CONFIG=" + cfgPath}
	if hermetic {
		env = append(env, "OPENCODE_DISABLE_PROJECT_CONFIG=1")
	}
	return env
}

// opencodeMCPEntry is one entry in opencode's top-level `mcp` object, in its
// McpRemoteConfig shape (packages/core/src/v1/config/mcp.ts).
//
// Note Type is the literal "remote" — opencode's discriminator is
// local/remote, NOT clown's or crush's http/sse. Timeout is milliseconds, the
// same unit clown stores, so it copies straight across (crush is the one that
// needs converting).
type opencodeMCPEntry struct {
	Type    string `json:"type"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
	Timeout int    `json:"timeout,omitempty"`
}

// opencodeMCPBlock translates clown's neutral entries into opencode's schema.
//
// The explicit timeout matters: opencode defaults to 5000ms, which is SHORTER
// than clown's own 30s plugin default, so an omitted timeout would fail
// long-running MCP tools at five seconds. A zero timeout is still omitted so a
// plugin that sets none inherits opencode's default rather than a pinned 0.
func opencodeMCPBlock(mcp map[string]pluginhost.MCPServerEntry) map[string]opencodeMCPEntry {
	if len(mcp) == 0 {
		return nil
	}
	out := make(map[string]opencodeMCPEntry, len(mcp))
	for name, e := range mcp {
		out[name] = opencodeMCPEntry{
			Type:    "remote",
			URL:     e.URL,
			Enabled: true,
			Timeout: e.Timeout,
		}
	}
	return out
}

func writeOpencodeConfigFile(path, url, token, model string, mcp map[string]pluginhost.MCPServerEntry) error {
	if model == "" {
		model = "gpt-4o"
	}
	type modelLimit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	}
	type modelEntry struct {
		Name  string     `json:"name"`
		Limit modelLimit `json:"limit"`
	}
	type providerOptions struct {
		BaseURL string `json:"baseURL"`
		APIKey  string `json:"apiKey"`
	}
	type providerEntry struct {
		NPM     string                `json:"npm"`
		Name    string                `json:"name"`
		Options providerOptions       `json:"options"`
		Models  map[string]modelEntry `json:"models"`
	}
	type opencodeConfig struct {
		Schema   string                      `json:"$schema"`
		Provider map[string]providerEntry    `json:"provider"`
		Model    string                      `json:"model"`
		MCP      map[string]opencodeMCPEntry `json:"mcp,omitempty"`
	}

	cfg := opencodeConfig{
		MCP:    opencodeMCPBlock(mcp),
		Schema: "https://opencode.ai/config.json",
		Provider: map[string]providerEntry{
			"custom": {
				NPM:  "@ai-sdk/openai-compatible",
				Name: "Custom Provider",
				Options: providerOptions{
					BaseURL: url,
					APIKey:  token,
				},
				Models: map[string]modelEntry{
					model: {
						Name:  model,
						Limit: modelLimit{Context: 128000, Output: 16384},
					},
				},
			},
		},
		Model: "custom/" + model,
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

// readJugglerPortfile reads the bare port number juggler writes to
// ~/.local/state/juggler/llama-server.port and returns it as a
// host:port address (127.0.0.1:<port>) suitable for prepending
// "http://" + appending "/v1". juggler writes the bound port only;
// the daemon always binds 127.0.0.1 (cmd/juggler/daemon.go).
//
// For backward compatibility, if the file contains a value that
// already looks like a host:port pair (contains a colon), it's
// returned as-is — older daemon builds wrote "127.0.0.1:<port>".
func readJugglerPortfile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	path := filepath.Join(home, ".local", "state", "juggler", "llama-server.port")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("juggler not running (no portfile at %s): %w", path, err)
	}
	val := strings.TrimSpace(string(data))
	if val == "" {
		return "", fmt.Errorf("juggler portfile is empty")
	}
	if strings.Contains(val, ":") {
		return val, nil
	}
	return "127.0.0.1:" + val, nil
}

// ensureOpencodeMigrationMarker works around anomalyco/opencode#16885:
// opencode's startup migration gate checks for ~/.local/share/opencode
// /opencode.db, but the stable channel uses opencode-stable.db. Without a
// matching marker the JSON->SQLite migration banner reruns on every launch.
// We create an idempotent symlink to the channel-specific DB. Best-effort:
// if anything fails (missing data dir, marker already exists as a regular
// file the user owns, etc.) we leave it alone — the worst case is the
// upstream bug stays visible, not a launch failure.
func ensureOpencodeMigrationMarker() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dataDir := filepath.Join(home, ".local", "share", "opencode")
	target := filepath.Join(dataDir, "opencode-stable.db")
	marker := filepath.Join(dataDir, "opencode.db")

	if _, err := os.Stat(target); err != nil {
		return
	}
	if _, err := os.Lstat(marker); err == nil {
		return
	}
	_ = os.Symlink(target, marker)
}

// openrouterGatewayURL is OpenRouter's OpenAI-compatible endpoint, hardcoded
// for provider=openrouter profiles rather than stored per-profile (Phase B,
// docs/plans/2026-07-24-openrouter-non-anthropic-design.md).
const openrouterGatewayURL = "https://openrouter.ai/api/v1"

// resolveOpencodeGateway resolves a gateway-backend profile's url/token/model
// for runOpencode. clownfile.ResolveEnv expands ${VAR}/$VAR references (e.g.
// a profileTemplates entry's Token: "${OPENROUTER_API_KEY}") — mirrors the
// expansion applyNamedProfile already does for the claude+gateway path;
// without it, the literal "${VAR}" string would be sent as the API key.
// Pulled out of runOpencode so it's unit-testable without the exec-coupled
// control flow around it.
func resolveOpencodeGateway(prof *profile.Profile) (url, token, model string) {
	url, token, model = clownfile.ResolveEnv(prof.URL), clownfile.ResolveEnv(prof.Token), prof.Model
	if prof.Provider == "openrouter" {
		url = openrouterGatewayURL
	}
	return url, token, model
}

// runOpencode launches opencode under clown's plugin host, so its clown-managed
// MCP servers reach opencode through the generated config's `mcp` block
// (FDR 0016 phase 1). hermetic controls phase 0's project-config suppression;
// see opencodeEnv.
func runOpencode(opencodePath string, prof *profile.Profile, flags parsedFlags, pluginDirs []string, root *staging.Root) int {
	args, hermetic := flags.forwarded, flags.hermeticConfig

	if opencodePath == "" {
		fmt.Fprintln(os.Stderr, "clown: opencode binary path not configured (build misconfiguration)")
		return 1
	}

	var url, token, model string
	if prof != nil && prof.Backend == "gateway" {
		url, token, model = resolveOpencodeGateway(prof)
	} else if prof != nil && prof.Backend == "local" {
		addr, err := readJugglerPortfile()
		if err != nil {
			fmt.Fprintf(os.Stderr, "clown: opencode local backend: %v\n", err)
			return 1
		}
		url = "http://" + addr + "/v1"
		token = "local"
		model = prof.Model
	} else {
		localCfg, err := readOpencodeLocalConfig()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && pluginhost.IsInteractive() {
				path, perr := userConfigWritePath("opencode.toml")
				if perr != nil {
					fmt.Fprintf(os.Stderr, "clown: opencode config: %v\n", perr)
					return 1
				}
				prompted, perr := promptOpencodeLocalConfig(path)
				if perr != nil {
					fmt.Fprintf(os.Stderr, "clown: opencode config: %v\n", perr)
					return 1
				}
				localCfg = prompted
			} else {
				fmt.Fprintf(os.Stderr, "clown: opencode config: %v\n", err)
				if errors.Is(err, fs.ErrNotExist) {
					path, _ := userConfigWritePath("opencode.toml")
					fmt.Fprintf(os.Stderr, "  create %s with:\n    url = \"https://your-endpoint/v1\"\n    token = \"your-api-key\"\n  or run clown interactively to be prompted.\n", path)
				}
				return 1
			}
		}
		url, token = localCfg.URL, localCfg.Token
		if prof != nil {
			model = prof.Model
		}
	}

	// Safe against the provider outliving this frame: runWithPluginHost runs the
	// provider as a subprocess (cmd.Run, never syscall.Exec) and returns only
	// after it exits, so the config file is still on disk for the whole session
	// — the launch root outlives this frame too, and owns the directory.
	tmpDir, err := root.Dir("clown-opencode-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: create config dir: %v\n", err)
		return 1
	}

	ensureOpencodeMigrationMarker()

	cfgPath := filepath.Join(tmpDir, "opencode.json")
	binding := &configFileBinding{
		baseArgs: args,
		writeConfig: func(mcp map[string]pluginhost.MCPServerEntry) ([]string, error) {
			if err := writeOpencodeConfigFile(cfgPath, url, token, model, mcp); err != nil {
				return nil, fmt.Errorf("write opencode config: %w", err)
			}
			return opencodeEnv(cfgPath, hermetic), nil
		},
	}

	return runWithPluginHost(&directExecutor{cliPath: opencodePath}, pluginDirs, flags, nil, "", root, binding)
}
