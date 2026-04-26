package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"golang.org/x/term"

	"github.com/amarbel-llc/clown/internal/sessions"
)

// resumeArgs holds the parsed result of `clown resume <args>`.
type resumeArgs struct {
	provider  string
	yes       bool
	forwarded []string
}

// runResume implements the `clown resume` subcommand: picks a Claude
// session whose recorded cwd exactly matches $PWD, then re-enters the
// main provider pipeline with --resume <id> forwarded to claude.
func runResume(args []string) int {
	parsed, err := parseResumeArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}
	if parsed.provider != "claude" {
		fmt.Fprintf(os.Stderr, "clown: resume only supports --provider claude in v1 (got %q)\n", parsed.provider)
		return 1
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}

	all, err := sessions.ListClaudeSessions(homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: listing claude sessions: %v\n", err)
		return 1
	}
	matches := sessions.FilterByCWD(all, cwd)

	switch len(matches) {
	case 0:
		fmt.Fprintf(os.Stderr, "clown: no resumable claude sessions recorded for %s\n", cwd)
		return 0
	case 1:
		return resumeSingle(matches[0], parsed)
	default:
		return resumePick(matches, parsed)
	}
}

// resumeSingle handles the lucky case: exactly one session matches
// $PWD. With --yes we skip straight to launch; otherwise we render a
// huh confirmation dialog so the user sees what's about to be resumed.
// Without a tty and without --yes we fail rather than silently launch.
func resumeSingle(s sessions.Session, args resumeArgs) int {
	if !args.yes {
		if !isInteractiveTerminal() {
			fmt.Fprintln(os.Stderr, "clown: resume requires an interactive terminal (or pass -y to skip confirmation)")
			return 1
		}
		ok, err := confirmResume(s)
		if err != nil {
			fmt.Fprintf(os.Stderr, "clown: confirmation prompt: %v\n", err)
			return 1
		}
		if !ok {
			return 0
		}
	}
	return launchResume(s, args)
}

// resumePick handles the multi-match case. The picker is interactive,
// so a non-tty terminal is fatal. --yes does not auto-pick — selection
// is the user's call when there is more than one candidate.
func resumePick(ss []sessions.Session, args resumeArgs) int {
	if !isInteractiveTerminal() {
		fmt.Fprintln(os.Stderr, "clown: resume requires an interactive terminal")
		return 1
	}
	chosen, err := pickSession(ss)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: session picker: %v\n", err)
		return 1
	}
	if chosen == nil {
		return 0
	}
	return launchResume(*chosen, args)
}

func launchResume(s sessions.Session, args resumeArgs) int {
	flags := parsedFlags{
		provider:         "claude",
		providerExplicit: true,
		forwarded:        append(args.forwarded, "--resume", s.ID),
	}
	return runWithFlags(flags)
}

func isInteractiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// confirmResume renders a huh confirmation showing the session's title,
// id, branch, and last-modified time. Mirrors the pattern used by
// pluginhost.ConfirmContinueWithFailures.
func confirmResume(s sessions.Session) (bool, error) {
	title := s.Title
	if title == "" {
		title = "(untitled)"
	}
	var desc strings.Builder
	if s.Provider != "" {
		fmt.Fprintf(&desc, "  provider:  %s\n", s.Provider)
	}
	fmt.Fprintf(&desc, "  uri:       %s\n", s.URI())
	fmt.Fprintf(&desc, "  last:      %s\n", formatRelDate(s.ModTime))
	if s.GitBranch != "" {
		fmt.Fprintf(&desc, "  branch:    %s\n", s.GitBranch)
	}

	ok := true // default to Resume
	confirm := huh.NewConfirm().
		Title(fmt.Sprintf("Resume %q?", title)).
		Description(desc.String()).
		Affirmative("Resume").
		Negative("Cancel").
		Value(&ok)

	// huh's default keymap only binds ctrl+c to Quit; esc does nothing.
	// Bind esc as well so the user can dismiss the dialog with either
	// key. Both produce huh.ErrUserAborted, which we treat as a soft
	// cancel — the user dismissed the dialog, no need to error out.
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("esc", "ctrl+c"))

	form := huh.NewForm(huh.NewGroup(confirm)).
		WithKeyMap(km).
		WithShowHelp(false)

	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, err
	}
	return ok, nil
}

// parseResumeArgs parses the args after `clown resume`.
func parseResumeArgs(args []string) (resumeArgs, error) {
	out := resumeArgs{provider: "claude"}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--":
			if i+1 < len(args) {
				out.forwarded = args[i+1:]
			}
			return out, nil
		case args[i] == "--provider":
			if i+1 >= len(args) {
				return resumeArgs{}, fmt.Errorf("--provider requires an argument")
			}
			out.provider = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--provider="):
			out.provider = strings.TrimPrefix(args[i], "--provider=")
		case args[i] == "--yes" || args[i] == "-y":
			out.yes = true
		case args[i] == "--help" || args[i] == "-h":
			printResumeHelp()
			os.Exit(0)
		default:
			return resumeArgs{}, fmt.Errorf("unknown resume flag %q", args[i])
		}
	}
	return out, nil
}

func printResumeHelp() {
	fmt.Print(`Usage: clown resume [--provider <name>] [-y|--yes] [-- <provider-args>]

Pick a resumable session whose recorded working directory exactly matches
$PWD, then launch the chosen provider with the right resume flag.

When exactly one session matches, a confirmation dialog appears unless
-y/--yes is passed.

Flags:
  --provider <name>   Provider to resume from (default: claude). Only
                      claude is supported in v1.
  -y, --yes           Skip the single-match confirmation dialog and
                      launch directly. No effect when zero or multiple
                      sessions match.
  --help, -h          Show this help text.

Args after -- are forwarded to the provider alongside --resume <id>.
`)
}

// sessionItem adapts sessions.Session to the bubbles list.Item interface.
type sessionItem struct{ s sessions.Session }

func (i sessionItem) Title() string {
	t := i.s.Title
	if t == "" {
		t = "(untitled)"
	}
	return t
}

func (i sessionItem) Description() string {
	var parts []string
	if i.s.Provider != "" {
		parts = append(parts, i.s.Provider)
	}
	parts = append(parts, formatRelDate(i.s.ModTime))
	if i.s.GitBranch != "" {
		parts = append(parts, "@"+i.s.GitBranch)
	}
	parts = append(parts, i.s.URI())
	return strings.Join(parts, "  ")
}
func (i sessionItem) FilterValue() string { return i.s.Title + " " + i.s.URI() }

type sessionPickerModel struct {
	list   list.Model
	chosen *sessions.Session
	quit   bool
}

func (m sessionPickerModel) Init() tea.Cmd { return nil }

func (m sessionPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if i, ok := m.list.SelectedItem().(sessionItem); ok {
				s := i.s
				m.chosen = &s
			}
			return m, tea.Quit
		case "q", "ctrl+c", "esc":
			m.quit = true
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.list.SetWidth(msg.Width)
		m.list.SetHeight(msg.Height - 2)
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m sessionPickerModel) View() string { return m.list.View() }

func pickSession(ss []sessions.Session) (*sessions.Session, error) {
	items := make([]list.Item, len(ss))
	for i, s := range ss {
		items[i] = sessionItem{s}
	}
	l := list.New(items, list.NewDefaultDelegate(), 60, 16)
	l.Title = "Select a session to resume"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	m, err := tea.NewProgram(sessionPickerModel{list: l}, tea.WithAltScreen()).Run()
	if err != nil {
		return nil, err
	}
	pm := m.(sessionPickerModel)
	if pm.quit {
		return nil, nil
	}
	return pm.chosen, nil
}

func formatRelDate(t time.Time) string {
	delta := time.Since(t)
	switch {
	case delta < time.Minute:
		return "just now"
	case delta < time.Hour:
		return fmt.Sprintf("%dm ago", int(delta.Minutes()))
	case delta < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(delta.Hours()))
	case delta < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(delta.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}
