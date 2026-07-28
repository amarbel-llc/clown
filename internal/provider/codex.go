package provider

import (
	"errors"
	"os"

	"code.linenisgreat.com/clown/internal/staging"
)

type CodexArgs struct {
	CLIPath          string
	SystemPromptFile string
	AppendFragments  string
	// Staging is the launch's staging root, under which the combined
	// instructions file is written. Required whenever SystemPromptFile or
	// AppendFragments is set.
	Staging *staging.Root
}

// BuildCodexArgs assembles the argument list for the codex provider CLI.
// It returns the args (excluding the binary path).
//
// Codex combines system-prompt and append fragments into a single file under
// cfg.Staging, passed via --config experimental_instructions_file=<path>.
//
// No cleanup function is returned: the staging root owns the file. Note that
// for codex the root is never actually reclaimed, because clown syscall.Exec's
// codex and the deferred Close never runs. That is inherent rather than an
// oversight — the exec'd process reads this file after clown is gone, so there
// is no point at which clown could correctly remove it. A locus that replaces
// clown's process cannot have its staging reclaimed by clown; any future
// Placement (remote, container) faces the same question of who reaps artifacts
// once the launcher is gone. Recorded for FDR 0017.
func BuildCodexArgs(cfg CodexArgs, forwarded []string) ([]string, error) {
	var args []string

	args = append(args, "--sandbox", "workspace-write")

	if cfg.SystemPromptFile != "" || cfg.AppendFragments != "" {
		if cfg.Staging == nil {
			return nil, errors.New("codex args: staging root is required to write the instructions file")
		}
		f, err := cfg.Staging.File("clown-prompt-*.txt")
		if err != nil {
			return nil, err
		}

		if cfg.SystemPromptFile != "" {
			data, err := os.ReadFile(cfg.SystemPromptFile)
			if err != nil {
				f.Close()
				return nil, err
			}
			if _, err := f.Write(data); err != nil {
				f.Close()
				return nil, err
			}
			if cfg.AppendFragments != "" {
				if _, err := f.WriteString("\n\n" + cfg.AppendFragments); err != nil {
					f.Close()
					return nil, err
				}
			}
		} else {
			if _, err := f.WriteString(cfg.AppendFragments); err != nil {
				f.Close()
				return nil, err
			}
		}
		// Closed before returning: a write through a handle outliving the root
		// would land in an unlinked inode and be silently lost.
		if err := f.Close(); err != nil {
			return nil, err
		}
		args = append(args, "--config", "experimental_instructions_file="+f.Name())
	}

	args = append(args, forwarded...)

	return args, nil
}
