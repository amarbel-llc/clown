package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// tent_builder.go invokes `nix build` to materialize the tent
// container image when no tarball is wired into the clown binary
// (today: darwin; planned: profile-driven image variation on linux).
// UX mirrors tent_loader.go: bubbletea spinner + rolling tail on a
// TTY, raw stderr stream otherwise.

const (
	builderTitle = "Building tent image (first run; may take a minute)…"
)

// buildAttribute returns the flake attribute path of the tent image
// for the current host. The tent image is always a linux derivation;
// darwin hosts cross-build via nix-darwin's linux-builder, so darwin
// GOARCHes map to their linux counterparts.
func buildAttribute() string {
	system := ""
	switch runtime.GOARCH {
	case "arm64":
		system = "aarch64-linux"
	case "amd64":
		system = "x86_64-linux"
	default:
		// Unknown GOARCH — bake it verbatim and let nix produce the
		// error. Future architectures (riscv64) will surface as a
		// readable "no such attribute" instead of silent fallback.
		system = runtime.GOARCH + "-linux"
	}
	return "packages." + system + ".tent-image"
}

// runTentImageBuild invokes `nix build --no-link --print-out-paths
// <flakeRef>#packages.<linux-system>.tent-image` and returns the
// absolute path of the resulting docker-format tarball. The
// `tent-image` derivation's out-path IS the tarball (same shape as
// linux's baked tentImageTarball), so no joining is needed. On a TTY
// uses the bubbletea spinner + log-tail UI; otherwise streams raw
// nix output to stderr. The returned tarball path is suitable to
// hand to runTentImageLoad.
func runTentImageBuild(flakeRef string) (string, error) {
	if flakeRef == "" {
		return "", fmt.Errorf("tent image build needs a flake ref, but buildcfg.TentImageFlakeRef is empty (dev build?)")
	}
	attr := buildAttribute()
	installable := flakeRef + "#" + attr
	if term.IsTerminal(int(os.Stdout.Fd())) {
		return buildWithSpinner(context.Background(), installable, attr)
	}
	return buildStreaming(context.Background(), installable, attr)
}

// resolveBuiltTarball parses the stdout of `nix build
// --print-out-paths` (one out-path per line; we expect a single one
// for the image derivation) and returns it. Verifies the path exists
// as a regular file before returning so a malformed build output
// surfaces immediately instead of inside `podman load`.
func resolveBuiltTarball(stdout, attr string) (string, error) {
	var tarball string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		tarball = line
		break
	}
	if tarball == "" {
		return "", fmt.Errorf("nix build %s printed no out-path", attr)
	}
	info, err := os.Stat(tarball)
	if err != nil {
		return "", fmt.Errorf("locating tent image tarball at %s: %w", tarball, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("tent image out-path %s is a directory, expected a tarball file", tarball)
	}
	return tarball, nil
}

// buildStreaming is the non-TTY path: forward nix's stderr directly
// to stderr while it runs, capturing only stdout (which carries the
// printed store path).
func buildStreaming(ctx context.Context, installable, attr string) (string, error) {
	fmt.Fprintln(os.Stderr, builderTitle)
	cmd := exec.CommandContext(ctx, "nix", "build", "--no-link", "--print-out-paths", installable)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("nix build %s: %w", installable, err)
	}
	return resolveBuiltTarball(stdout.String(), attr)
}

// buildWithSpinner runs nix build under a bubbletea program that
// shows a spinner and the last builderMaxLines lines of nix's
// progress output (which lands on stderr). stdout is captured
// separately so we can parse the printed store path on success.
func buildWithSpinner(ctx context.Context, installable, attr string) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, "nix", "build", "--no-link", "--print-out-paths", installable)
	stderrR, stderrW := io.Pipe()
	cmd.Stderr = stderrW
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting nix build: %w", err)
	}

	model := newTentBuilderModel(builderTitle, cancel)
	program := tea.NewProgram(model)

	var captured bytes.Buffer
	teed := io.TeeReader(stderrR, &captured)
	go func() {
		scanner := bufio.NewScanner(teed)
		scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)
		for scanner.Scan() {
			program.Send(logLineMsg(scanner.Text()))
		}
	}()

	cmdErrCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = stderrW.Close()
		cmdErrCh <- err
		program.Send(doneMsg{err: err})
	}()

	if _, err := program.Run(); err != nil {
		cancel()
		<-cmdErrCh
		return "", fmt.Errorf("running tent builder UI: %w", err)
	}

	cmdErr := <-cmdErrCh
	if cmdErr != nil {
		os.Stderr.Write(captured.Bytes())
		return "", fmt.Errorf("nix build %s: %w", installable, cmdErr)
	}
	return resolveBuiltTarball(stdout.String(), attr)
}

// tentBuilderModel is the bubbletea Model for the build phase.
// Shape mirrors tentLoaderModel; kept distinct so the success
// message reads "built" rather than "cached" and so a future
// refactor that unifies the two can do so deliberately.
type tentBuilderModel struct {
	spinner  spinner.Model
	title    string
	lines    []string
	maxLines int
	done     bool
	err      error
	cancel   context.CancelFunc
}

func newTentBuilderModel(title string, cancel context.CancelFunc) tentBuilderModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	return tentBuilderModel{
		spinner:  s,
		title:    title,
		maxLines: loaderMaxLines,
		cancel:   cancel,
	}
}

func (m tentBuilderModel) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m tentBuilderModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			if m.cancel != nil {
				m.cancel()
			}
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

func (m tentBuilderModel) View() string {
	if m.done {
		if m.err != nil {
			return loaderFailureStyle.Render("✗ "+m.title+" — failed") + "\n"
		}
		return loaderSuccessStyle.Render("✓ Tent image built.") + "\n"
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s %s\n", m.spinner.View(), m.title)
	for _, line := range m.lines {
		fmt.Fprintln(&b, loaderLogStyle.Render("│ "+line))
	}
	return b.String()
}
