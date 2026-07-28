package provider

import (
	"bufio"
	"errors"
	"os"
	"strings"

	"code.linenisgreat.com/clown/internal/staging"
)

type ClaudeArgs struct {
	CLIPath             string
	AgentsFile          string
	DisallowedToolsFile string
	SystemPromptFile    string
	AppendFragments     string
	// Staging is the launch's staging root, under which the
	// --append-system-prompt-file file is written. Required whenever
	// AppendFragments is non-empty, which is every real clown launch: clown
	// always prepends a build-identity fragment.
	Staging *staging.Root
	// SettingsJSON, when non-empty, is emitted as `--settings <json>`: an inline
	// settings source claude merges at the highest CLI precedence. clown ships no
	// managed-settings (clown#133), so this flag is how clown injects settings
	// claude actually reads — currently the AskUserQuestion AFK-timeout override
	// (clown#163). It is placed before the forwarded user args so a user's own
	// `--settings` (after the `--`) wins on last-flag precedence.
	SettingsJSON string
}

// BuildClaudeArgs assembles the argument list for the claude provider CLI.
// It returns the args (excluding the binary path) and the path of the
// --append-system-prompt-file file (empty when no append fragments were
// written).
//
// cfg.Staging owns the prompt file's lifetime, which is why no cleanup
// function is returned: the file lives under the launch root and is removed
// when that root closes. A cleanup the caller could also run would make two
// things responsible for one file, and the loser of that race is a caller
// still holding a path to something already unlinked.
//
// The append-file path is surfaced so the plugin-host path can fold dynamic,
// plugin-contributed fragments into the same file after its servers are
// healthy but before claude is exec'd (RFC-0002 §dynamic fragments). clown
// always prepends a build-identity fragment, so in practice this path is
// non-empty for every real claude launch.
func BuildClaudeArgs(cfg ClaudeArgs, forwarded []string) ([]string, string, error) {
	var args []string
	var appendFile string

	if cfg.DisallowedToolsFile != "" {
		patterns, err := readDisallowedTools(cfg.DisallowedToolsFile)
		if err != nil {
			return nil, "", err
		}
		for _, p := range patterns {
			args = append(args, "--disallowed-tools", p)
		}
	}

	if cfg.AgentsFile != "" {
		data, err := os.ReadFile(cfg.AgentsFile)
		if err != nil {
			return nil, "", err
		}
		args = append(args, "--agents", string(data))
	}

	if cfg.SystemPromptFile != "" {
		args = append(args, "--system-prompt-file", cfg.SystemPromptFile)
	}

	if cfg.AppendFragments != "" {
		if cfg.Staging == nil {
			return nil, "", errors.New("claude args: staging root is required to write the prompt-append file")
		}
		f, err := cfg.Staging.File("clown-prompt-*.txt")
		if err != nil {
			return nil, "", err
		}
		if _, err := f.WriteString(cfg.AppendFragments); err != nil {
			f.Close()
			return nil, "", err
		}
		// Closed before returning, and deliberately so: the staging root
		// unlinks the tree on Close, and a write through a handle that
		// outlived it would land in an unlinked inode and be silently lost.
		// The plugin-host path reopens this path by name to fold in dynamic
		// fragments.
		if err := f.Close(); err != nil {
			return nil, "", err
		}
		appendFile = f.Name()
		args = append(args, "--append-system-prompt-file", f.Name())
	}

	if cfg.SettingsJSON != "" {
		args = append(args, "--settings", cfg.SettingsJSON)
	}

	args = append(args, forwarded...)

	return args, appendFile, nil
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
