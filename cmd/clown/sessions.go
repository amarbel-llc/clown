package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/amarbel-llc/clown/internal/sessions"
)

// sessionsCompleteArgs holds the parsed `clown sessions-complete` flags.
type sessionsCompleteArgs struct {
	pwdOnly bool
}

// runSessionsComplete implements the `clown sessions-complete` subcommand:
// emits one line per resumable session in fish completion format
// (`<value>\t<description>`). The value is the canonical clown:// URI;
// the description is `<reldate>  <title-or-id>`.
//
// In v1 only the claude provider is enumerated; codex is filed as #27.
func runSessionsComplete(args []string) int {
	parsed, err := parseSessionsCompleteArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}

	all, err := sessions.ListClaudeSessions(homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: listing claude sessions: %v\n", err)
		return 1
	}

	if parsed.pwdOnly {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "clown: %v\n", err)
			return 1
		}
		all = sessions.FilterByCWD(all, cwd)
	}

	w := os.Stdout
	for _, s := range all {
		fmt.Fprintf(w, "%s\t%s\n", s.URI(), formatSessionCompletionDesc(s))
	}
	return 0
}

// formatSessionCompletionDesc returns the fish-completion hint for a
// session: `<reldate>  <title-or-id>`. Title is preferred; falls back
// to the session id if no title was recorded. Tabs and newlines are
// stripped from the title because fish parses each completion line as
// `<value>\t<description>` and embedded whitespace would confuse it.
func formatSessionCompletionDesc(s sessions.Session) string {
	label := s.Title
	if label == "" {
		label = s.ID
	}
	label = strings.NewReplacer("\t", " ", "\n", " ").Replace(label)
	return formatRelDate(s.ModTime) + "  " + label
}

func parseSessionsCompleteArgs(args []string) (sessionsCompleteArgs, error) {
	var out sessionsCompleteArgs
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--pwd-only":
			out.pwdOnly = true
		case "--help", "-h":
			printSessionsCompleteHelp()
			os.Exit(0)
		default:
			if strings.HasPrefix(args[i], "-") {
				return sessionsCompleteArgs{}, fmt.Errorf("unknown sessions-complete flag %q", args[i])
			}
			return sessionsCompleteArgs{}, fmt.Errorf("unexpected positional argument %q", args[i])
		}
	}
	return out, nil
}

func printSessionsCompleteHelp() {
	fmt.Print(`Usage: clown sessions-complete [--pwd-only]

Emit one line per resumable session in fish completion format:
  <clown://provider/id>\t<reldate>  <title-or-id>

Intended for shell completions; not a stable user-facing UI.

Flags:
  --pwd-only   Only list sessions whose recorded cwd exactly matches
               $PWD. Useful when completing 'clown resume'.
  --help, -h   Show this help text.
`)
}
