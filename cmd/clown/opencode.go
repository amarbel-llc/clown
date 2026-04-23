package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type opencodeLocalConfig struct {
	URL   string
	Token string
}

func readOpencodeLocalConfig() (opencodeLocalConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return opencodeLocalConfig{}, fmt.Errorf("home dir: %w", err)
	}
	path := filepath.Join(home, ".config", "circus", "opencode.toml")
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

func writeOpencodeConfig(configDir, url, token string) error {
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
					"claude-sonnet-4-6": {
						Name:  "Claude Sonnet 4.6",
						Limit: modelLimit{Context: 200000, Output: 64000},
					},
				},
			},
		},
		Model: "custom/claude-sonnet-4-6",
	}

	dir := filepath.Join(configDir, "opencode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "opencode.json"), data, 0o600)
}

func runOpencode(opencodePath string, args []string) int {
	if opencodePath == "" {
		fmt.Fprintln(os.Stderr, "clown: opencode binary path not configured (build misconfiguration)")
		return 1
	}

	localCfg, err := readOpencodeLocalConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: opencode config: %v\n", err)
		return 1
	}

	tmpDir, err := os.MkdirTemp("", "clown-opencode-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: create temp dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmpDir)

	if err := writeOpencodeConfig(tmpDir, localCfg.URL, localCfg.Token); err != nil {
		fmt.Fprintf(os.Stderr, "clown: write opencode config: %v\n", err)
		return 1
	}

	cmd := exec.Command(opencodePath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpDir)

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "clown: opencode: %v\n", err)
		return 1
	}
	return 0
}
