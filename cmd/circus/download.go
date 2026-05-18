package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"

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

func cmdDownload(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: circus download <name>")
		return 1
	}
	name := args[0]

	entries, err := circusmodels.Registry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: %v\n", err)
		return 1
	}
	entry, ok := circusmodels.FindEntry(name, entries)
	if !ok {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name)
		}
		fmt.Fprintf(os.Stderr, "circus: unknown model %q; available: %v\n", name, names)
		return 1
	}

	dir := circusmodels.Dir()

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
