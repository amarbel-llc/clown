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
	provider         string
	providerExplicit bool
	yes              bool
	uri              string // positional clown://<provider>/<id>; empty for picker mode
	forwarded        []string
}

// runResume implements the `clown resume` subcommand. Dispatches between
// two flows depending on whether a URI positional was given.
func runResume(args []string) int {
	parsed, err := parseResumeArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}
	if parsed.uri != "" {
		return resumeByURI(parsed)
	}
	return resumeByPicker(parsed)
}

// resumeByPicker is the original flow: enumerate $PWD-matching sessions,
// pick interactively (or auto-resume on a single match).
func resumeByPicker(args resumeArgs) int {
	if args.provider != "claude" {
		fmt.Fprintf(os.Stderr, "clown: resume only supports --provider claude in v1 (got %q)\n", args.provider)
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
		return resumeSingle(matches[0], args)
	default:
		return resumePick(matches, args)
	}
}

// resumeByURI handles the direct flow: the caller named a specific
// session, so we look it up by id rather than enumerating $PWD matches.
// PWD mismatch surfaces a warning confirm dialog (default Cancel) since
// reattachment from a different directory may not work as expected.
func resumeByURI(args resumeArgs) int {
	provider, id, err := sessions.ParseURI(args.uri)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}
	if provider != "claude" {
		fmt.Fprintf(os.Stderr, "clown: resume only supports clown://claude/<id> in v1 (got provider %q)\n", provider)
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
	s := sessions.FindByID(all, id)
	if s == nil {
		fmt.Fprintf(os.Stderr, "clown: no claude session with id %q\n", id)
		return 1
	}

	if sessions.SameDir(s.CWD, cwd, "") {
		return launchResume(*s, args)
	}

	if !isInteractiveTerminal() {
		fmt.Fprintf(os.Stderr,
			"clown: session was recorded at %q; current directory is %q.\n"+
				"clown: refusing to launch non-interactively — reattachment may not work.\n",
			s.CWD, cwd)
		return 1
	}
	ok, err := confirmMismatchedResume(*s, cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: confirmation prompt: %v\n", err)
		return 1
	}
	if !ok {
		return 0
	}
	return launchResume(*s, args)
}

// resumeSingle handles the picker-mode lucky case: exactly one session
// matches $PWD. With --yes we skip straight to launch; otherwise we
// render a huh confirmation dialog so the user sees what's about to be
// resumed. Without a tty and without --yes we fail rather than silently
// launch.
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

// confirmResume renders the standard resume confirmation. Default is
// Resume — single Enter launches.
func confirmResume(s sessions.Session) (bool, error) {
	return runResumeConfirm(
		fmt.Sprintf("Resume %q?", sessionTitle(s)),
		buildResumeDesc(s, "", false),
		true,
	)
}

// confirmMismatchedResume renders a warning confirmation when the named
// session's recorded cwd does not match $PWD. Default is Cancel — the
// user has to actively choose to proceed.
func confirmMismatchedResume(s sessions.Session, currentPWD string) (bool, error) {
	return runResumeConfirm(
		fmt.Sprintf("Resume %q from a different directory?", sessionTitle(s)),
		buildResumeDesc(s, currentPWD, true),
		false,
	)
}

// runResumeConfirm renders a huh confirm form with esc bound to dismiss
// (huh's default keymap binds Quit to ctrl+c only). Returns (ok, nil)
// on a normal answer and (false, nil) when the user dismisses with
// esc/ctrl-c.
func runResumeConfirm(title, description string, defaultYes bool) (bool, error) {
	ok := defaultYes
	confirm := huh.NewConfirm().
		Title(title).
		Description(description).
		Affirmative("Resume").
		Negative("Cancel").
		Value(&ok)

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

func sessionTitle(s sessions.Session) string {
	if s.Title == "" {
		return "(untitled)"
	}
	return s.Title
}

func buildResumeDesc(s sessions.Session, currentPWD string, warnMismatch bool) string {
	var desc strings.Builder
	if warnMismatch {
		fmt.Fprintln(&desc, "Warning: this session may not reattach properly.")
		fmt.Fprintf(&desc, "  recorded:  %s\n", s.CWD)
		fmt.Fprintf(&desc, "  current:   %s\n", currentPWD)
		fmt.Fprintln(&desc)
	}
	if s.Provider != "" {
		fmt.Fprintf(&desc, "  provider:  %s\n", s.Provider)
	}
	fmt.Fprintf(&desc, "  uri:       %s\n", s.URI())
	fmt.Fprintf(&desc, "  last:      %s\n", formatRelDate(s.ModTime))
	if s.GitBranch != "" {
		fmt.Fprintf(&desc, "  branch:    %s\n", s.GitBranch)
	}
	return desc.String()
}

// parseResumeArgs parses the args after `clown resume`. Validates flag
// combos at the end, since the URI positional disallows --provider and
// -y/--yes regardless of order.
func parseResumeArgs(args []string) (resumeArgs, error) {
	out := resumeArgs{provider: "claude"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			if i+1 < len(args) {
				out.forwarded = args[i+1:]
			}
			return validateResumeArgs(out)
		case arg == "--provider":
			if i+1 >= len(args) {
				return resumeArgs{}, fmt.Errorf("--provider requires an argument")
			}
			out.provider = args[i+1]
			out.providerExplicit = true
			i++
		case strings.HasPrefix(arg, "--provider="):
			out.provider = strings.TrimPrefix(arg, "--provider=")
			out.providerExplicit = true
		case arg == "--yes" || arg == "-y":
			out.yes = true
		case arg == "--help" || arg == "-h":
			printResumeHelp()
			os.Exit(0)
		case strings.HasPrefix(arg, "-"):
			return resumeArgs{}, fmt.Errorf("unknown resume flag %q", arg)
		default:
			if out.uri != "" {
				return resumeArgs{}, fmt.Errorf("multiple positional arguments; only one URI is accepted (got %q and %q)", out.uri, arg)
			}
			out.uri = arg
		}
	}
	return validateResumeArgs(out)
}

func validateResumeArgs(a resumeArgs) (resumeArgs, error) {
	if a.uri != "" {
		if a.providerExplicit {
			return resumeArgs{}, fmt.Errorf("--provider is incompatible with a positional URI; the URI carries the provider")
		}
		if a.yes {
			return resumeArgs{}, fmt.Errorf("-y/--yes is incompatible with a positional URI; naming the URI is the confirmation")
		}
	}
	return a, nil
}

func printResumeHelp() {
	fmt.Print(`Usage: clown resume [--provider <name>] [-y|--yes] [-- <provider-args>]
       clown resume <uri>                       [-- <provider-args>]

Picker mode (no positional):
  Lists resumable sessions whose recorded working directory exactly
  matches $PWD. Auto-resumes when exactly one matches, otherwise opens
  an interactive picker. The single-match case shows a confirmation
  dialog unless -y/--yes is passed.

Direct mode (positional URI):
  Looks up the named session by id and launches it. If the session's
  recorded cwd does not match $PWD a warning confirmation appears
  (defaults to Cancel) since reattachment may not work as expected.
  --provider and -y/--yes are not accepted in this mode — the URI
  carries the provider, and naming a specific URI is itself the
  confirmation.

Flags (picker mode):
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
