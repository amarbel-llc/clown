package main

// Trapeze provider dispatch — the headless XMPP agent (prototype 2).
//
// Unlike the other providers, `--provider trapeze` runs no TUI: it execs
// `trapeze xmpp-agent`, which logs in to XMPP as a 1:1 chat bot and answers
// each direct message with an OpenRouter chat completion (pure conversational,
// no tools). clown is deliberately THIN here — it resolves the OpenRouter
// credentials and exports them, then forwards everything else straight through:
//
//	clown --provider trapeze -- \
//	  --jid agent@krone --server krone:5222 --insecure --model openai/gpt-4o-mini
//
// Credential resolution (highest precedence first):
//  1. a named --profile (its url/token/model),
//  2. ~/.config/circus/openrouter.toml (url/token/model — same shape as
//     opencode.toml / crush.toml),
//  3. the inherited OPENROUTER_* environment (clown sets nothing; trapeze reads
//     it directly).
//
// The XMPP connection flags (--jid/--password/--server/--insecure) and the
// model/system-prompt flags are trapeze's own; clown passes them through after
// `--`. trapeze also honours TRAPEZE_XMPP_PASSWORD / OPENROUTER_* from the env.
import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/amarbel-llc/clown/internal/profile"
)

// openRouterConfig is the resolved OpenRouter backend for the trapeze provider.
type openRouterConfig struct {
	URL   string
	Token string
	Model string
}

// openRouterConfigPath returns ~/.config/circus/openrouter.toml.
func openRouterConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".config", "circus", "openrouter.toml"), nil
}

// readOpenRouterConfig parses url/token/model from openrouter.toml. A missing
// file is NOT an error (returns the zero config) — the env fallback covers it.
func readOpenRouterConfig() (openRouterConfig, error) {
	path, err := openRouterConfigPath()
	if err != nil {
		return openRouterConfig{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return openRouterConfig{}, nil
		}
		return openRouterConfig{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var cfg openRouterConfig
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
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch k {
		case "url":
			cfg.URL = v
		case "token":
			cfg.Token = v
		case "model":
			cfg.Model = v
		}
	}
	return cfg, scanner.Err()
}

// resolveOpenRouter picks the OpenRouter backend per the precedence above.
func resolveOpenRouter(prof *profile.Profile) (openRouterConfig, error) {
	if prof != nil && prof.Token != "" {
		return openRouterConfig{URL: prof.URL, Token: prof.Token, Model: prof.Model}, nil
	}
	return readOpenRouterConfig()
}

func runTrapeze(trapezePath string, args []string, prof *profile.Profile) int {
	cfg, err := resolveOpenRouter(prof)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: trapeze: %v\n", err)
		return 1
	}

	cmd := exec.Command(trapezePath, append([]string{"xmpp-agent"}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Overlay resolved creds onto the inherited env; leave unset fields to the
	// environment (trapeze reads OPENROUTER_*/TRAPEZE_XMPP_PASSWORD itself).
	env := os.Environ()
	if cfg.Token != "" {
		env = append(env, "OPENROUTER_API_KEY="+cfg.Token)
	}
	if cfg.URL != "" {
		env = append(env, "OPENROUTER_BASE_URL="+cfg.URL)
	}
	if cfg.Model != "" {
		env = append(env, "OPENROUTER_MODEL="+cfg.Model)
	}
	cmd.Env = env

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "clown: trapeze: %v\n", err)
		return 1
	}
	return 0
}
