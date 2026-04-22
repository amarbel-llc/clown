package provider

import (
	"bufio"
	"os"
	"strings"
)

type ClaudeArgs struct {
	CLIPath             string
	AgentsFile          string
	DisallowedToolsFile string
	SystemPromptFile    string
	AppendFragments     string
}

// BuildClaudeArgs assembles the argument list for the claude provider CLI.
// It returns the args (excluding the binary path) and a cleanup function
// that removes any temp files created for prompt fragments. The caller
// must invoke cleanup after the downstream process exits.
func BuildClaudeArgs(cfg ClaudeArgs, forwarded []string) ([]string, func(), error) {
	var args []string
	var cleanups []string

	if cfg.DisallowedToolsFile != "" {
		patterns, err := readDisallowedTools(cfg.DisallowedToolsFile)
		if err != nil {
			return nil, nil, err
		}
		for _, p := range patterns {
			args = append(args, "--disallowed-tools", p)
		}
	}

	if cfg.AgentsFile != "" {
		data, err := os.ReadFile(cfg.AgentsFile)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, "--agents", string(data))
	}

	if cfg.SystemPromptFile != "" {
		args = append(args, "--system-prompt-file", cfg.SystemPromptFile)
	}

	if cfg.AppendFragments != "" {
		f, err := os.CreateTemp("", "clown-prompt-*.txt")
		if err != nil {
			return nil, nil, err
		}
		if _, err := f.WriteString(cfg.AppendFragments); err != nil {
			f.Close()
			os.Remove(f.Name())
			return nil, nil, err
		}
		f.Close()
		cleanups = append(cleanups, f.Name())
		args = append(args, "--append-system-prompt-file", f.Name())
	}

	args = append(args, forwarded...)

	cleanup := func() {
		for _, path := range cleanups {
			os.Remove(path)
		}
	}
	return args, cleanup, nil
}

func readDisallowedTools(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var patterns []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns, scanner.Err()
}
