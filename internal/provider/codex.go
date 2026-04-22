package provider

import (
	"os"
)

type CodexArgs struct {
	CLIPath          string
	SystemPromptFile string
	AppendFragments  string
}

// BuildCodexArgs assembles the argument list for the codex provider CLI.
// It returns the args (excluding the binary path) and a cleanup function
// that removes any temp files created for prompt content. The caller
// must invoke cleanup after the downstream process exits.
//
// Codex combines system-prompt and append fragments into a single temp
// file passed via --config experimental_instructions_file=<path>.
func BuildCodexArgs(cfg CodexArgs, forwarded []string) ([]string, func(), error) {
	var args []string
	var cleanups []string

	args = append(args, "--sandbox", "workspace-write")

	if cfg.SystemPromptFile != "" || cfg.AppendFragments != "" {
		f, err := os.CreateTemp("", "clown-prompt-*.txt")
		if err != nil {
			return nil, nil, err
		}

		if cfg.SystemPromptFile != "" {
			data, err := os.ReadFile(cfg.SystemPromptFile)
			if err != nil {
				f.Close()
				os.Remove(f.Name())
				return nil, nil, err
			}
			if _, err := f.Write(data); err != nil {
				f.Close()
				os.Remove(f.Name())
				return nil, nil, err
			}
			if cfg.AppendFragments != "" {
				if _, err := f.WriteString("\n\n" + cfg.AppendFragments); err != nil {
					f.Close()
					os.Remove(f.Name())
					return nil, nil, err
				}
			}
		} else {
			if _, err := f.WriteString(cfg.AppendFragments); err != nil {
				f.Close()
				os.Remove(f.Name())
				return nil, nil, err
			}
		}
		f.Close()
		cleanups = append(cleanups, f.Name())
		args = append(args, "--config", "experimental_instructions_file="+f.Name())
	}

	args = append(args, forwarded...)

	cleanup := func() {
		for _, path := range cleanups {
			os.Remove(path)
		}
	}
	return args, cleanup, nil
}
