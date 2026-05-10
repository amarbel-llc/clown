package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// tent_loader.go renders `podman load -i <tarball>` as a bubbletea
// program: a spinner row above a rolling tail of the most recent
// output lines, like nix-build's live build window. Non-TTY callers
// fall back to streaming raw podman output to stderr so CI logs
// keep the full record.

const (
	// loaderMaxLines is how many of podman's most-recent output
	// lines we keep visible under the spinner. Five lines is enough
	// to see progress (one blob per line during `podman load`) without
	// pushing the user's shell history off-screen.
	loaderMaxLines = 5

	loaderTitle = "Loading tent image (first run)…"
)

// runTentImageLoad runs `podman load -i tarball`. When stdout is a
// TTY it uses the bubbletea live-tail UI; otherwise it streams raw
// output to stderr (matching the prior behavior on CI). The combined
// stdout+stderr of podman is always captured so a failure can dump
// the full transcript regardless of UI mode.
func runTentImageLoad(podmanPath, tarball string) error {
	if term.IsTerminal(int(os.Stdout.Fd())) {
		return loadWithSpinner(context.Background(), podmanPath, tarball)
	}
	return loadStreaming(context.Background(), podmanPath, tarball)
}

// loadStreaming is the non-TTY path: forward podman output directly
// to stderr while it runs. Used by CI, redirected stdout, and other
// non-interactive environments.
func loadStreaming(ctx context.Context, podmanPath, tarball string) error {
	fmt.Fprintln(os.Stderr, loaderTitle)
	cmd := exec.CommandContext(ctx, podmanPath, "load", "-i", tarball)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("podman load -i %s: %w", tarball, err)
	}
	return nil
}

// loadWithSpinner runs podman load under a bubbletea program that
// shows a spinner and the last loaderMaxLines lines of output. On
// success the tail collapses to a single "tent image cached" line;
// on failure the captured transcript is dumped to stderr so the user
// sees what podman complained about.
func loadWithSpinner(ctx context.Context, podmanPath, tarball string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, podmanPath, "load", "-i", tarball)
	pipeR, pipeW := io.Pipe()
	cmd.Stdout = pipeW
	cmd.Stderr = pipeW

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting podman load: %w", err)
	}

	model := newTentLoaderModel(loaderTitle, cancel)
	program := tea.NewProgram(model)

	// Forward output lines to the program AND capture them in a
	// buffer for post-mortem display on failure. bytes.Buffer is
	// safe for a single-writer goroutine.
	var captured bytes.Buffer
	teed := io.TeeReader(pipeR, &captured)
	go func() {
		scanner := bufio.NewScanner(teed)
		// podman load lines stay well under 1 KiB but bump the
		// scanner buffer anyway in case future versions emit long
		// JSON progress events.
		scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)
		for scanner.Scan() {
			program.Send(logLineMsg(scanner.Text()))
		}
	}()

	cmdErrCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		// Closing the pipe writer unblocks the scanner goroutine.
		_ = pipeW.Close()
		cmdErrCh <- err
		program.Send(doneMsg{err: err})
	}()

	if _, err := program.Run(); err != nil {
		// bubbletea itself failed (rare). Make sure we don't leave
		// podman running, then surface the error.
		cancel()
		<-cmdErrCh
		return fmt.Errorf("running tent loader UI: %w", err)
	}

	cmdErr := <-cmdErrCh
	if cmdErr != nil {
		os.Stderr.Write(captured.Bytes())
		return fmt.Errorf("podman load -i %s: %w", tarball, cmdErr)
	}
	return nil
}

// logLineMsg carries a single line of podman output into the
// bubbletea Update loop.
type logLineMsg string

// doneMsg is sent when the podman process exits; nil err means
// success.
type doneMsg struct{ err error }

// tentLoaderModel is the bubbletea Model that paints the spinner +
// rolling log tail. Kept tiny: a fixed-size ring of recent lines and
// a charmbracelet/bubbles spinner.
type tentLoaderModel struct {
	spinner  spinner.Model
	title    string
	lines    []string
	maxLines int
	done     bool
	err      error
	// cancel kills the podman child when the user hits Ctrl-C.
	cancel context.CancelFunc
}

func newTentLoaderModel(title string, cancel context.CancelFunc) tentLoaderModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // cyan-ish
	return tentLoaderModel{
		spinner:  s,
		title:    title,
		maxLines: loaderMaxLines,
		cancel:   cancel,
	}
}

func (m tentLoaderModel) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m tentLoaderModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			if m.cancel != nil {
				m.cancel()
			}
			// Don't tea.Quit yet — let the doneMsg arrive so we
			// render the final state. cancel() will cause podman
			// to exit, which sends doneMsg.
			return m, nil
		}
		return m, nil
	case logLineMsg:
		m.lines = append(m.lines, string(msg))
		if len(m.lines) > m.maxLines {
			m.lines = m.lines[len(m.lines)-m.maxLines:]
		}
		return m, nil
	case doneMsg:
		m.done = true
		m.err = msg.err
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

var (
	loaderLogStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "240", Dark: "245"}).
			PaddingLeft(2)
	loaderSuccessStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	loaderFailureStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

func (m tentLoaderModel) View() string {
	if m.done {
		if m.err != nil {
			return loaderFailureStyle.Render("✗ "+m.title+" — failed") + "\n"
		}
		return loaderSuccessStyle.Render("✓ Tent image cached.") + "\n"
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s %s\n", m.spinner.View(), m.title)
	for _, line := range m.lines {
		fmt.Fprintln(&b, loaderLogStyle.Render("│ "+line))
	}
	return b.String()
}
