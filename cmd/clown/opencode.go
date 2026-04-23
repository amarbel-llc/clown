package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func writeOpencodeConfig(configDir, token string) error {
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
			"etsy": {
				NPM:  "@ai-sdk/openai-compatible",
				Name: "Etsy AI Gateway",
				Options: providerOptions{
					BaseURL: "https://models.ag.genai-dev.gke.etsycloud.com/v1",
					APIKey:  token,
				},
				Models: map[string]modelEntry{
					"claude-sonnet-4-6": {
						Name:  "Claude Sonnet 4.6 (via Etsy)",
						Limit: modelLimit{Context: 200000, Output: 64000},
					},
				},
			},
		},
		Model: "etsy/claude-sonnet-4-6",
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

	token := os.Getenv("ETSY_LLM_KEY_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "clown: ETSY_LLM_KEY_TOKEN is not set (required for --provider opencode)")
		return 1
	}

	tmpDir, err := os.MkdirTemp("", "clown-opencode-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: create temp dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmpDir)

	if err := writeOpencodeConfig(tmpDir, token); err != nil {
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
