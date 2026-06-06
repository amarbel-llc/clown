package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/amarbel-llc/clown/internal/circusmodels"
)

type (
	progressMsg float64
	doneMsg     struct{ err error }
)

type progressModel struct {
	bar      progress.Model
	total    int64
	done     bool
	finalErr error // set when doneMsg arrives
}

func (m progressModel) Init() tea.Cmd { return nil }

func (m progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case progressMsg:
		return m, m.bar.SetPercent(float64(msg))
	case doneMsg:
		m.finalErr = msg.err
		if msg.err != nil {
			return m, tea.Quit
		}
		m.done = true
		return m, m.bar.SetPercent(1.0)
	case progress.FrameMsg:
		updated, cmd := m.bar.Update(msg)
		m.bar = updated.(progress.Model)
		if m.done && m.bar.Percent() >= 0.999 {
			return m, tea.Quit
		}
		return m, cmd
	}
	return m, nil
}

func (m progressModel) View() string {
	return "\n" + m.bar.View() + "\n"
}

// parseDownloadArgs accepts:
//
//	circus download <name>                                    (registry lookup)
//	circus download <name> --url URL [--sha256 HEX] [--size N]
//
// --alias-style flags support both space and equals forms, matching
// circus start. Returns the parsed name and overrides for url, sha,
// and size (zero values when not provided).
func parseDownloadArgs(args []string) (name, url, sha string, size int64, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--url":
			if i+1 >= len(args) {
				return "", "", "", 0, fmt.Errorf("--url requires an argument")
			}
			url = args[i+1]
			i++
		case strings.HasPrefix(a, "--url="):
			url = strings.TrimPrefix(a, "--url=")
		case a == "--sha256":
			if i+1 >= len(args) {
				return "", "", "", 0, fmt.Errorf("--sha256 requires an argument")
			}
			sha = args[i+1]
			i++
		case strings.HasPrefix(a, "--sha256="):
			sha = strings.TrimPrefix(a, "--sha256=")
		case a == "--size":
			if i+1 >= len(args) {
				return "", "", "", 0, fmt.Errorf("--size requires an argument")
			}
			size, err = strconv.ParseInt(args[i+1], 10, 64)
			if err != nil {
				return "", "", "", 0, fmt.Errorf("--size: %w", err)
			}
			i++
		case strings.HasPrefix(a, "--size="):
			size, err = strconv.ParseInt(strings.TrimPrefix(a, "--size="), 10, 64)
			if err != nil {
				return "", "", "", 0, fmt.Errorf("--size: %w", err)
			}
		case strings.HasPrefix(a, "--"):
			return "", "", "", 0, fmt.Errorf("unknown flag %q", a)
		default:
			if name != "" {
				return "", "", "", 0, fmt.Errorf("unexpected positional arg %q (name already set to %q)", a, name)
			}
			name = a
		}
	}
	return name, url, sha, size, nil
}

func cmdDownload(args []string) int {
	name, urlOverride, shaOverride, sizeOverride, err := parseDownloadArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: download: %v\n", err)
		fmt.Fprintln(os.Stderr, "usage: circus download <name> [--url URL] [--sha256 HEX] [--size BYTES]")
		return 1
	}
	if name == "" {
		fmt.Fprintln(os.Stderr, "usage: circus download <name> [--url URL] [--sha256 HEX] [--size BYTES]")
		return 1
	}

	entries, err := circusmodels.Registry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: %v\n", err)
		return 1
	}
	entry, ok := circusmodels.FindEntry(name, entries)
	switch {
	case !ok && urlOverride == "":
		// Unknown name with no URL — friendly hint listing available models
		// and the escape hatch for ad-hoc downloads.
		var names []string
		for _, e := range entries {
			names = append(names, e.Name)
		}
		fmt.Fprintf(os.Stderr, "circus: unknown model %q; available: %v\n", name, names)
		fmt.Fprintf(os.Stderr, "       to fetch from an arbitrary URL: circus download %s --url <url> [--sha256 HEX] [--size BYTES]\n", name)
		return 1
	case !ok && urlOverride != "":
		// Ad-hoc download: synthesize an entry from the user's overrides.
		entry = circusmodels.RegistryEntry{
			Name:        name,
			URL:         urlOverride,
			SHA256:      shaOverride,
			Size:        sizeOverride,
			Description: "ad-hoc download (not in registry)",
		}
		if shaOverride == "" {
			fmt.Fprintf(os.Stderr, "circus: warning: --sha256 not provided; integrity will NOT be verified\n")
		}
	case ok && urlOverride != "":
		// Registry hit but the user is overriding. Apply any provided
		// fields on top of the registry entry. Useful for, e.g., bumping
		// to a newer quantization at the same name.
		fmt.Fprintf(os.Stderr, "circus: --url override for registry entry %q\n", name)
		entry.URL = urlOverride
		if shaOverride != "" {
			entry.SHA256 = shaOverride
		} else {
			// Suppress mismatched-SHA failure: when the user changes URL
			// without supplying a new SHA, we can't keep the old one.
			entry.SHA256 = ""
			fmt.Fprintln(os.Stderr, "circus: warning: --sha256 not provided alongside --url; integrity will NOT be verified")
		}
		if sizeOverride > 0 {
			entry.Size = sizeOverride
		}
	}

	dir := circusmodels.Dir()

	// The bubbletea progress bar needs a real terminal (it opens /dev/tty
	// for input); off-terminal it aborts the whole download with "could
	// not open a new TTY" (clown#55). Degrade to a bar-less download with
	// a single final summary line instead.
	if !progressUIWanted(os.Stdin, os.Stdout) {
		if err := downloadPlain(context.Background(), entry, dir, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "circus: %v\n", err)
			return 1
		}
		return 0
	}

	var bar progress.Model
	if entry.Size > 0 {
		bar = progress.New(progress.WithDefaultGradient())
	} else {
		bar = progress.New(progress.WithDefaultGradient(), progress.WithoutPercentage())
	}
	m := progressModel{bar: bar, total: entry.Size}
	p := tea.NewProgram(m)

	progressCB := func(written, total int64) {
		if total > 0 {
			p.Send(progressMsg(float64(written) / float64(total)))
		}
	}

	go func() {
		_, err := circusmodels.Download(context.Background(), entry, dir, progressCB)
		p.Send(doneMsg{err: err})
	}()

	result, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: progress: %v\n", err)
		return 1
	}
	if pm, ok := result.(progressModel); ok && pm.finalErr != nil {
		fmt.Fprintf(os.Stderr, "circus: %v\n", pm.finalErr)
		return 1
	}

	dest := filepath.Join(dir, name+".gguf")
	fmt.Printf("circus: downloaded %s to %s\n", name, dest)
	return 0
}

// progressUIWanted reports whether the interactive bubbletea progress bar
// can run: both the input and output streams must be terminals (bubbletea
// falls back to opening /dev/tty when stdin is not one, which fails hard in
// non-TTY contexts — clown#55). Mirrors internal/pluginhost's interactive
// gate.
func progressUIWanted(stdin, stdout *os.File) bool {
	return term.IsTerminal(int(stdin.Fd())) && term.IsTerminal(int(stdout.Fd()))
}

// downloadPlain is the non-TTY download path (clown#55): no progress UI at
// all, just the download followed by a single summary line on w.
func downloadPlain(ctx context.Context, entry circusmodels.RegistryEntry, dir string, w io.Writer) error {
	var written int64
	dest, err := circusmodels.Download(ctx, entry, dir, func(n, _ int64) { written = n })
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "circus: downloaded %s: %d bytes to %s\n", entry.Name, written, dest)
	return nil
}
