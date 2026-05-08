package main

// Crush provider dispatch.
//
// Crush (charmbracelet/crush) is an OpenAI/Anthropic-compatible TUI agent.
// Like opencode, clown launches it with a generated config that pins one
// custom provider, and overrides the search path via $CRUSH_GLOBAL_CONFIG
// so the call is hermetic w.r.t. the user's own ~/.config/crush/crush.json.
// Crush's CRUSH_GLOBAL_CONFIG names a *directory*; crush appends
// "crush.json" itself.
//
// Three backends are supported, mirroring opencode:
//
//   - anthropic: passthrough. Crush's builtin Anthropic provider is used,
//     reading ANTHROPIC_API_KEY from the environment. Clown still writes a
//     config to disable provider auto-update so we don't reach out to
//     Catwalk on every launch.
//   - gateway: OpenAI-compatible endpoint configured by the user in
//     ~/.config/circus/crush.toml (parsed identically to opencode.toml).
//     Clown writes a "custom" provider with type=openai-compat.
//   - local: the circus-managed llama-server. The portfile at
//     ~/.local/state/circus/llama-server.port gives us the bound port;
//     readCircusPortfile() prefixes 127.0.0.1 to produce the host:port
//     pair this code uses to build the openai-compat base_url.
//
// Safety defaults: crush already prompts for tool permissions by default.
// Its only escape hatches are permissions.allowed_tools (allowlist) and
// the --yolo flag (skip all prompts). We do NOT default to --yolo. We
// also do NOT pre-populate allowed_tools — leaving the prompt-for-each
// behavior intact matches opencode's deferred posture and keeps the user
// in the loop. If a future safety policy lands, this is the seam.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/amarbel-llc/clown/internal/pluginhost"
	"github.com/amarbel-llc/clown/internal/profile"
)

type crushLocalConfig struct {
	URL   string
	Token string
}

// crushLocalConfigPath returns ~/.config/circus/crush.toml.
func crushLocalConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".config", "circus", "crush.toml"), nil
}

func readCrushLocalConfig() (crushLocalConfig, error) {
	path, err := crushLocalConfigPath()
	if err != nil {
		return crushLocalConfig{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return crushLocalConfig{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var cfg crushLocalConfig
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
		return crushLocalConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	if cfg.URL == "" {
		return crushLocalConfig{}, fmt.Errorf("%s: url is required", path)
	}
	if cfg.Token == "" {
		return crushLocalConfig{}, fmt.Errorf("%s: token is required", path)
	}
	return cfg, nil
}

func writeCrushLocalConfigFile(path, url, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	body := fmt.Sprintf("url = %q\ntoken = %q\n", url, token)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func promptCrushLocalConfig(path string) (crushLocalConfig, error) {
	var (
		url     = "http://localhost:11434/v1"
		token   = "local"
		confirm bool
	)

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().
				Title("Configure crush").
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
		return crushLocalConfig{}, fmt.Errorf("prompt: %w", err)
	}
	if !confirm {
		return crushLocalConfig{}, fmt.Errorf("aborted by user; %s not written", path)
	}

	url = strings.TrimSpace(url)
	token = strings.TrimSpace(token)
	if err := writeCrushLocalConfigFile(path, url, token); err != nil {
		return crushLocalConfig{}, err
	}
	return crushLocalConfig{URL: url, Token: token}, nil
}

// crushBackend names the three configurations writeCrushConfig knows
// how to emit. The string values are not part of any external schema —
// they're internal to clown and chosen to read clearly at the call site.
type crushBackend string

const (
	crushBackendAnthropic   crushBackend = "anthropic"
	crushBackendOpenAICompat crushBackend = "openai-compat"
)

// writeCrushConfig writes a crush.json config to <configDir>/crush.json
// with one provider entry under the id "custom". Crush's config schema
// (see github.com/charmbracelet/crush/internal/config/config.go) keys
// providers by id and selects the "large" / "small" model via a
// top-level `models` map; we register one model and point both slots at
// it so any agent (Coder, Task, Title, Summarizer) resolves cleanly.
//
// For the anthropic backend, model and apiKey may be empty: crush's
// builtin Anthropic provider is used and authenticates via the
// ANTHROPIC_API_KEY env var passed through the parent environment. We
// still write disable_provider_auto_update so launches are reproducible
// and don't depend on a network call to Catwalk.
func writeCrushConfig(configDir string, backend crushBackend, baseURL, apiKey, model string) error {
	if model == "" {
		switch backend {
		case crushBackendAnthropic:
			model = "claude-sonnet-4-5"
		default:
			model = "gpt-4o"
		}
	}

	type modelEntry struct {
		ID                 string `json:"id"`
		Name               string `json:"name"`
		ContextWindow      int    `json:"context_window"`
		DefaultMaxTokens   int    `json:"default_max_tokens"`
	}
	type providerEntry struct {
		ID      string       `json:"id"`
		Name    string       `json:"name"`
		Type    string       `json:"type"`
		BaseURL string       `json:"base_url,omitempty"`
		APIKey  string       `json:"api_key,omitempty"`
		Models  []modelEntry `json:"models"`
	}
	type selectedModel struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
	}
	type options struct {
		DisableProviderAutoUpdate bool `json:"disable_provider_auto_update"`
	}
	type crushConfig struct {
		Schema    string                   `json:"$schema,omitempty"`
		Providers map[string]providerEntry `json:"providers,omitempty"`
		Models    map[string]selectedModel `json:"models,omitempty"`
		Options   options                  `json:"options"`
	}

	cfg := crushConfig{
		Options: options{DisableProviderAutoUpdate: true},
	}

	switch backend {
	case crushBackendAnthropic:
		cfg.Providers = map[string]providerEntry{
			"anthropic": {
				ID:   "anthropic",
				Name: "Anthropic",
				Type: "anthropic",
				APIKey: "$ANTHROPIC_API_KEY",
				Models: []modelEntry{{
					ID:               model,
					Name:             model,
					ContextWindow:    200000,
					DefaultMaxTokens: 16384,
				}},
			},
		}
		cfg.Models = map[string]selectedModel{
			"large": {Model: model, Provider: "anthropic"},
			"small": {Model: model, Provider: "anthropic"},
		}
	case crushBackendOpenAICompat:
		cfg.Providers = map[string]providerEntry{
			"custom": {
				ID:      "custom",
				Name:    "Custom Provider",
				Type:    "openai-compat",
				BaseURL: baseURL,
				APIKey:  apiKey,
				Models: []modelEntry{{
					ID:               model,
					Name:             model,
					ContextWindow:    128000,
					DefaultMaxTokens: 16384,
				}},
			},
		}
		cfg.Models = map[string]selectedModel{
			"large": {Model: model, Provider: "custom"},
			"small": {Model: model, Provider: "custom"},
		}
	default:
		return fmt.Errorf("unknown crush backend %q", backend)
	}

	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(filepath.Join(configDir, "crush.json"), data, 0o600)
}

func runCrush(crushPath string, args []string, prof *profile.Profile) int {
	if crushPath == "" {
		fmt.Fprintln(os.Stderr, "clown: crush binary path not configured (build misconfiguration)")
		return 1
	}

	var (
		backend crushBackend
		baseURL string
		apiKey  string
		model   string
	)

	switch {
	case prof != nil && prof.Backend == "anthropic":
		backend = crushBackendAnthropic
		model = prof.Model
	case prof != nil && prof.Backend == "gateway":
		backend = crushBackendOpenAICompat
		baseURL, apiKey, model = prof.URL, prof.Token, prof.Model
	case prof != nil && prof.Backend == "local":
		addr, err := readCircusPortfile()
		if err != nil {
			fmt.Fprintf(os.Stderr, "clown: crush local backend: %v\n", err)
			return 1
		}
		backend = crushBackendOpenAICompat
		baseURL = "http://" + addr + "/v1"
		apiKey = "local"
		model = prof.Model
	default:
		// No profile: read user's local crush.toml as a gateway config,
		// matching the opencode default-when-no-profile flow.
		localCfg, err := readCrushLocalConfig()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && pluginhost.IsInteractive() {
				path, perr := crushLocalConfigPath()
				if perr != nil {
					fmt.Fprintf(os.Stderr, "clown: crush config: %v\n", perr)
					return 1
				}
				prompted, perr := promptCrushLocalConfig(path)
				if perr != nil {
					fmt.Fprintf(os.Stderr, "clown: crush config: %v\n", perr)
					return 1
				}
				localCfg = prompted
			} else {
				fmt.Fprintf(os.Stderr, "clown: crush config: %v\n", err)
				if errors.Is(err, fs.ErrNotExist) {
					path, _ := crushLocalConfigPath()
					fmt.Fprintf(os.Stderr, "  create %s with:\n    url = \"https://your-endpoint/v1\"\n    token = \"your-api-key\"\n  or run clown interactively to be prompted.\n", path)
				}
				return 1
			}
		}
		backend = crushBackendOpenAICompat
		baseURL, apiKey = localCfg.URL, localCfg.Token
	}

	tmpDir, err := os.MkdirTemp("", "clown-crush-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: create temp dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmpDir)

	if err := writeCrushConfig(tmpDir, backend, baseURL, apiKey, model); err != nil {
		fmt.Fprintf(os.Stderr, "clown: write crush config: %v\n", err)
		return 1
	}

	cmd := exec.Command(crushPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// CRUSH_GLOBAL_CONFIG points at the *directory* — crush appends
	// "crush.json" itself (see charmbracelet/crush internal/config/load.go's
	// GlobalConfig function).
	cmd.Env = append(os.Environ(), "CRUSH_GLOBAL_CONFIG="+tmpDir)

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "clown: crush: %v\n", err)
		return 1
	}
	return 0
}
