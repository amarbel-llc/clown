package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
)

//go:embed registry.json
var registryJSON []byte

type registryEntry struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	Description string `json:"description"`
}

func loadRegistry() ([]registryEntry, error) {
	var entries []registryEntry
	if err := json.Unmarshal(registryJSON, &entries); err != nil {
		return nil, fmt.Errorf("parse registry: %w", err)
	}
	return entries, nil
}

func findInRegistry(name string, entries []registryEntry) (registryEntry, bool) {
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return registryEntry{}, false
}

func verifySHA256(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expected {
		return fmt.Errorf("sha256 mismatch: got %s, want %s", got, expected)
	}
	return nil
}

type (
	progressMsg float64
	doneMsg     struct{ err error }
)

type progressModel struct {
	bar      progress.Model
	total    int64
	finalErr error // set when doneMsg arrives
}

func (m progressModel) Init() tea.Cmd { return nil }

func (m progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case progressMsg:
		return m, m.bar.SetPercent(float64(msg))
	case doneMsg:
		m.finalErr = msg.err
		return m, tea.Quit
	case progress.FrameMsg:
		updated, cmd := m.bar.Update(msg)
		m.bar = updated.(progress.Model)
		return m, cmd
	}
	return m, nil
}

func (m progressModel) View() string {
	return "\n" + m.bar.View() + "\n"
}

type progressWriter struct {
	total   int64
	written int64
	program *tea.Program
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.written += int64(n)
	if pw.total > 0 {
		pct := float64(pw.written) / float64(pw.total)
		pw.program.Send(progressMsg(pct))
	}
	return n, nil
}

func cmdDownload(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: circus download <name>")
		return 1
	}
	name := args[0]

	entries, err := loadRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: %v\n", err)
		return 1
	}
	entry, ok := findInRegistry(name, entries)
	if !ok {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name)
		}
		fmt.Fprintf(os.Stderr, "circus: unknown model %q; available: %v\n", name, names)
		return 1
	}

	dir := modelsDir()
	dest := filepath.Join(dir, name+".gguf")
	if _, err := os.Stat(dest); err == nil {
		fmt.Fprintf(os.Stderr, "circus: model %q already installed at %s\n", name, dest)
		return 1
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "circus: mkdir: %v\n", err)
		return 1
	}

	tmp, err := os.CreateTemp(dir, name+".*.gguf.tmp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "circus: create temp: %v\n", err)
		return 1
	}
	tmpPath := tmp.Name()
	cleanup := func() { os.Remove(tmpPath) }

	client := &http.Client{Timeout: 2 * time.Hour}
	resp, err := client.Get(entry.URL)
	if err != nil {
		tmp.Close()
		cleanup()
		fmt.Fprintf(os.Stderr, "circus: download: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		tmp.Close()
		cleanup()
		fmt.Fprintf(os.Stderr, "circus: download: HTTP %d\n", resp.StatusCode)
		return 1
	}

	var bar progress.Model
	if entry.Size > 0 {
		bar = progress.New(progress.WithDefaultGradient())
	} else {
		bar = progress.New(progress.WithDefaultGradient(), progress.WithoutPercentage())
	}
	m := progressModel{bar: bar, total: entry.Size}
	p := tea.NewProgram(m)

	pw := &progressWriter{total: entry.Size, program: p}
	reader := io.TeeReader(resp.Body, pw)

	go func() {
		_, copyErr := io.Copy(tmp, reader)
		tmp.Close()
		if copyErr == nil {
			p.Send(progressMsg(1.0))
			time.Sleep(400 * time.Millisecond)
		}
		p.Send(doneMsg{err: copyErr})
	}()

	result, err := p.Run()
	if err != nil {
		cleanup()
		fmt.Fprintf(os.Stderr, "circus: progress: %v\n", err)
		return 1
	}
	if pm, ok := result.(progressModel); ok && pm.finalErr != nil {
		cleanup()
		fmt.Fprintf(os.Stderr, "circus: write: %v\n", pm.finalErr)
		return 1
	}

	if err := verifySHA256(tmpPath, entry.SHA256); err != nil {
		cleanup()
		fmt.Fprintf(os.Stderr, "circus: %v\n", err)
		return 1
	}

	if err := os.Rename(tmpPath, dest); err != nil {
		cleanup()
		fmt.Fprintf(os.Stderr, "circus: rename: %v\n", err)
		return 1
	}

	fmt.Printf("circus: downloaded %s to %s\n", name, dest)
	return 0
}
