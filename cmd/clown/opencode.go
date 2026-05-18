package main

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

type opencodeLocalConfig struct {
	URL   string
	Token string
}

// opencodeLocalConfigPath returns ~/.config/circus/opencode.toml.
func opencodeLocalConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".config", "circus", "opencode.toml"), nil
}

func readOpencodeLocalConfig() (opencodeLocalConfig, error) {
	path, err := opencodeLocalConfigPath()
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
	return cfg, nil
}

// writeOpencodeLocalConfigFile writes a minimal ~/.config/circus/opencode.toml
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
// ~/.config/circus/opencode.toml via huh, writes it on confirmation, and
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
func writeOpencodeConfigFile(path, url, token, model string) error {
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
		Schema   string                   `json:"$schema"`
		Provider map[string]providerEntry `json:"provider"`
		Model    string                   `json:"model"`
	}

	cfg := opencodeConfig{
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

// readCircusPortfile reads the bare port number circus writes to
// ~/.local/state/circus/llama-server.port and returns it as a
// host:port address (127.0.0.1:<port>) suitable for prepending
// "http://" + appending "/v1". circus writes the bound port only;
// the daemon always binds 127.0.0.1 (cmd/circus/daemon.go).
//
// For backward compatibility, if the file contains a value that
// already looks like a host:port pair (contains a colon), it's
// returned as-is — older daemon builds wrote "127.0.0.1:<port>".
func readCircusPortfile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	path := filepath.Join(home, ".local", "state", "circus", "llama-server.port")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("circus not running (no portfile at %s): %w", path, err)
	}
	val := strings.TrimSpace(string(data))
	if val == "" {
		return "", fmt.Errorf("circus portfile is empty")
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

func runOpencode(opencodePath string, args []string, prof *profile.Profile) int {
	if opencodePath == "" {
		fmt.Fprintln(os.Stderr, "clown: opencode binary path not configured (build misconfiguration)")
		return 1
	}

	var url, token, model string
	if prof != nil && prof.Backend == "gateway" {
		url, token, model = prof.URL, prof.Token, prof.Model
	} else if prof != nil && prof.Backend == "local" {
		addr, err := readCircusPortfile()
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
				path, perr := opencodeLocalConfigPath()
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
					path, _ := opencodeLocalConfigPath()
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

	tmpDir, err := os.MkdirTemp("", "clown-opencode-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: create temp dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmpDir)

	cfgPath := filepath.Join(tmpDir, "opencode.json")
	if err := writeOpencodeConfigFile(cfgPath, url, token, model); err != nil {
		fmt.Fprintf(os.Stderr, "clown: write opencode config: %v\n", err)
		return 1
	}

	ensureOpencodeMigrationMarker()

	cmd := exec.Command(opencodePath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "OPENCODE_CONFIG="+cfgPath)

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "clown: opencode: %v\n", err)
		return 1
	}
	return 0
}
