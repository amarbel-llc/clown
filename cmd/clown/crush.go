package main

// Crush provider dispatch.
//
// Crush (charmbracelet/crush) is an OpenAI/Anthropic-compatible TUI agent.
// Like opencode, clown launches it with a generated config that pins one
// custom provider, overriding $CRUSH_GLOBAL_CONFIG — which names a *directory*;
// crush appends "crush.json" itself.
//
// That override is NOT hermetic, contrary to what this comment used to claim.
// crush merges, in order: the system config, GlobalConfig() (the only one
// CRUSH_GLOBAL_CONFIG redirects), GlobalConfigData()
// (~/.local/share/crush/crush.json, redirected only by CRUSH_GLOBAL_DATA), and
// every crush.json/.crush.json found walking up from cwd to the git root —
// then the WORKSPACE config at <data-dir>/crush.json last, "so it has highest
// priority" (internal/config/load.go:65-66). Verified 2026-07-27 that a
// repo-local crush.json really does merge over clown's, and that the workspace
// slot really does override it in both directions.
//
// Since crush exposes no switch to disable the project walk, phase 0 takes
// authority by precedence instead: clown writes its config into the workspace
// slot as well and passes --data-dir. See crushDataDir.
//
// Three backends are supported, mirroring opencode:
//
//   - anthropic: passthrough. Crush's builtin Anthropic provider is used,
//     reading ANTHROPIC_API_KEY from the environment. Clown still writes a
//     config to disable provider auto-update so we don't reach out to
//     Catwalk on every launch.
//   - gateway: OpenAI-compatible endpoint configured by the user in
//     ~/.config/clown/crush.toml (parsed identically to opencode.toml).
//     Clown writes a "custom" provider with type=openai-compat.
//   - local: the juggler-managed llama-server. The portfile at
//     ~/.local/state/juggler/llama-server.port gives us the bound port;
//     readJugglerPortfile() prefixes 127.0.0.1 to produce the host:port
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
	"crypto/sha256"
	"encoding/hex"
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
)

type crushLocalConfig struct {
	URL   string
	Token string
}

// readCrushLocalConfig reads the user's crush gateway config from the
// canonical ~/.config/clown/crush.toml (legacy ~/.config/juggler/ read
// fallback with a warning; see userConfigPath).
func readCrushLocalConfig() (crushLocalConfig, error) {
	path, legacy, err := userConfigPath("crush.toml")
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
	if legacy {
		warnLegacyConfig(path)
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
	crushBackendAnthropic    crushBackend = "anthropic"
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
// crushMCPEntry is one entry in crush's top-level `mcp` object
// (internal/config/config.go MCPConfig). Type is crush's own discriminator
// ("stdio" | "sse" | "http"), which happens to match clown's vocabulary for
// HTTP servers — unlike opencode's "remote".
type crushMCPEntry struct {
	Type    string `json:"type"`
	URL     string `json:"url"`
	Timeout int    `json:"timeout,omitempty"`
}

// crushMCPTimeoutSeconds converts clown's millisecond timeout into the SECONDS
// crush expects (internal/config/config.go: "Timeout in seconds for MCP server
// connections", default 15).
//
// This conversion is the whole reason crush needs its own translation step:
// copying clown's 30000 across verbatim would configure an 8-hour timeout, and
// nothing would fail loudly enough to notice. Rounds UP so a sub-second timeout
// never truncates to 0, which crush would read as "unset" and silently replace
// with its 15s default.
func crushMCPTimeoutSeconds(ms int) int {
	if ms <= 0 {
		return 0
	}
	return (ms + 999) / 1000
}

func crushMCPBlock(mcp map[string]pluginhost.MCPServerEntry) map[string]crushMCPEntry {
	if len(mcp) == 0 {
		return nil
	}
	out := make(map[string]crushMCPEntry, len(mcp))
	for name, e := range mcp {
		out[name] = crushMCPEntry{
			Type:    e.Type,
			URL:     e.URL,
			Timeout: crushMCPTimeoutSeconds(e.Timeout),
		}
	}
	return out
}

func writeCrushConfig(configDir string, backend crushBackend, baseURL, apiKey, model string, mcp map[string]pluginhost.MCPServerEntry) error {
	if model == "" {
		switch backend {
		case crushBackendAnthropic:
			model = "claude-sonnet-4-5"
		default:
			model = "gpt-4o"
		}
	}

	type modelEntry struct {
		ID               string `json:"id"`
		Name             string `json:"name"`
		ContextWindow    int    `json:"context_window"`
		DefaultMaxTokens int    `json:"default_max_tokens"`
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
		MCP       map[string]crushMCPEntry `json:"mcp,omitempty"`
		Options   options                  `json:"options"`
	}

	cfg := crushConfig{
		MCP:     crushMCPBlock(mcp),
		Options: options{DisableProviderAutoUpdate: true},
	}

	switch backend {
	case crushBackendAnthropic:
		cfg.Providers = map[string]providerEntry{
			"anthropic": {
				ID:     "anthropic",
				Name:   "Anthropic",
				Type:   "anthropic",
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

// resolveCrushGateway resolves a gateway-backend profile's baseURL/apiKey/
// model for runCrush. clownfile.ResolveEnv expands ${VAR}/$VAR references —
// mirrors the expansion applyNamedProfile already does for the claude+gateway
// path; without it, a literal "${VAR}" string would be sent as the API key.
// Pulled out of runCrush so it's unit-testable without the exec-coupled
// control flow around it.
func resolveCrushGateway(prof *profile.Profile) (baseURL, apiKey, model string) {
	return clownfile.ResolveEnv(prof.URL), clownfile.ResolveEnv(prof.Token), prof.Model
}

// runCrush launches crush under clown's plugin host so its clown-managed MCP
// servers reach crush through the generated config's `mcp` block (FDR 0016
// phase 1). hermetic drives phase 0's workspace-slot precedence; see
// crushDataDir.
func runCrush(crushPath string, args []string, prof *profile.Profile, flags parsedFlags, pluginDirs []string, hermetic bool) int {
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
		baseURL, apiKey, model = resolveCrushGateway(prof)
	case prof != nil && prof.Backend == "local":
		addr, err := readJugglerPortfile()
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
				path, perr := userConfigWritePath("crush.toml")
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
					path, _ := userConfigWritePath("crush.toml")
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
	// Safe: runWithPluginHost runs crush as a subprocess and returns only after
	// it exits, so the config outlives the launch.
	defer os.RemoveAll(tmpDir)

	// Phase 0. crush has no switch to disable its project-config walk, so
	// authority comes from precedence instead: the workspace config at
	// <data-dir>/crush.json is loaded last and overrides a repo-local
	// crush.json (verified in both directions, 2026-07-27). dataDir must be
	// STABLE — it is also where crush keeps sessions, so a mkdtemp here would
	// silently break `crush --continue` on every launch.
	dataDir := ""
	if hermetic {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			fmt.Fprintf(os.Stderr, "clown: getwd: %v\n", cwdErr)
			return 1
		}
		if dataDir, err = crushDataDir(cwd); err != nil {
			fmt.Fprintf(os.Stderr, "clown: crush data dir: %v\n", err)
			return 1
		}
	}

	binding := &configFileBinding{
		baseArgs: crushArgs(args, dataDir),
		writeConfig: func(mcp map[string]pluginhost.MCPServerEntry) ([]string, error) {
			// The lower-priority copy, kept for parity with the pre-phase-0
			// behavior; CRUSH_GLOBAL_CONFIG names the *directory* and crush
			// appends "crush.json" itself (internal/config/load.go GlobalConfig).
			if err := writeCrushConfig(tmpDir, backend, baseURL, apiKey, model, mcp); err != nil {
				return nil, fmt.Errorf("write crush config: %w", err)
			}
			// The authoritative copy in the workspace slot.
			if dataDir != "" {
				if err := writeCrushConfig(dataDir, backend, baseURL, apiKey, model, mcp); err != nil {
					return nil, fmt.Errorf("write crush workspace config: %w", err)
				}
			}
			return []string{"CRUSH_GLOBAL_CONFIG=" + tmpDir}, nil
		},
	}

	return runWithPluginHost(&directExecutor{cliPath: crushPath}, args, pluginDirs, flags, nil, "", binding)
}

// crushDataDir returns the stable, clown-owned crush data directory for
// projectDir. It holds crush's WORKSPACE config — the highest-priority slot in
// crush's merge order (internal/config/load.go:65-66) — which is how clown's
// config outranks a repo-local crush.json.
//
// Two properties matter, both load-bearing:
//
//   - STABLE across launches, because --data-dir is also where crush keeps
//     sessions. A mkdtemp here would silently reset session history and break
//     `crush --continue` on every run.
//   - PER-PROJECT, because that is what crush itself does when left alone
//     (setDefaults resolves DataDirectory to the closest `.crush` bounded by the
//     project, else <workingDir>/.crush). A single shared directory would pool
//     every project's sessions together.
//
// The project path is hashed rather than embedded: worktree paths are long and
// contain characters that are awkward in a directory name. Clown's own state
// root is used rather than <project>/.crush so clown never writes into the
// user's repository.
func crushDataDir(projectDir string) (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("home dir: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	sum := sha256.Sum256([]byte(projectDir))
	dir := filepath.Join(base, "clown", "crush", hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return dir, nil
}

// crushArgs prepends --data-dir so crush reads the workspace config clown wrote
// there. An empty dataDir (hermeticity off) leaves the argv untouched.
func crushArgs(args []string, dataDir string) []string {
	if dataDir == "" {
		return args
	}
	return append([]string{"--data-dir", dataDir}, args...)
}
