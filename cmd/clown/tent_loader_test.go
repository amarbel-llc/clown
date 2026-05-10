package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTentLoader_LogLinesAppendUntilCap(t *testing.T) {
	m := newTentLoaderModel("t", nil)
	for i := 0; i < m.maxLines; i++ {
		next, _ := m.Update(logLineMsg("line"))
		m = next.(tentLoaderModel)
	}
	if got, want := len(m.lines), m.maxLines; got != want {
		t.Fatalf("lines len = %d, want %d", got, want)
	}
}

func TestTentLoader_LogLinesRingBuffersBeyondCap(t *testing.T) {
	m := newTentLoaderModel("t", nil)
	total := m.maxLines + 3
	for i := 0; i < total; i++ {
		next, _ := m.Update(logLineMsg("L"))
		m = next.(tentLoaderModel)
	}
	if got, want := len(m.lines), m.maxLines; got != want {
		t.Fatalf("ring should cap at %d, got %d", want, got)
	}
}

func TestTentLoader_LogLinesRingKeepsMostRecent(t *testing.T) {
	m := newTentLoaderModel("t", nil)
	m.maxLines = 3
	for _, line := range []string{"a", "b", "c", "d", "e"} {
		next, _ := m.Update(logLineMsg(line))
		m = next.(tentLoaderModel)
	}
	want := []string{"c", "d", "e"}
	if len(m.lines) != 3 {
		t.Fatalf("expected 3 lines, got %v", m.lines)
	}
	for i, w := range want {
		if m.lines[i] != w {
			t.Errorf("lines[%d] = %q, want %q", i, m.lines[i], w)
		}
	}
}

func TestTentLoader_DoneMsgRecordsError(t *testing.T) {
	m := newTentLoaderModel("t", nil)
	next, cmd := m.Update(doneMsg{err: errFake("boom")})
	m = next.(tentLoaderModel)
	if !m.done {
		t.Error("done should be true after doneMsg")
	}
	if m.err == nil || m.err.Error() != "boom" {
		t.Errorf("err = %v, want `boom`", m.err)
	}
	if cmd == nil {
		t.Error("doneMsg should return a tea.Quit command")
	}
}

func TestTentLoader_ViewInProgressShowsLinesUnderSpinner(t *testing.T) {
	m := newTentLoaderModel("Loading…", nil)
	next, _ := m.Update(logLineMsg("Copying blob abc"))
	m = next.(tentLoaderModel)
	out := m.View()
	if !strings.Contains(out, "Loading…") {
		t.Errorf("view missing title: %q", out)
	}
	if !strings.Contains(out, "Copying blob abc") {
		t.Errorf("view missing log line: %q", out)
	}
}

func TestTentLoader_ViewDoneSuccessShowsCheckmark(t *testing.T) {
	m := newTentLoaderModel("Loading…", nil)
	next, _ := m.Update(doneMsg{err: nil})
	m = next.(tentLoaderModel)
	out := m.View()
	if !strings.Contains(out, "Tent image cached") {
		t.Errorf("view missing cached message: %q", out)
	}
	if !strings.Contains(out, "✓") {
		t.Errorf("view missing success glyph: %q", out)
	}
}

func TestTentLoader_ViewDoneFailureShowsCrossmark(t *testing.T) {
	m := newTentLoaderModel("Loading…", nil)
	next, _ := m.Update(doneMsg{err: errFake("nope")})
	m = next.(tentLoaderModel)
	out := m.View()
	if !strings.Contains(out, "failed") {
		t.Errorf("view missing failure message: %q", out)
	}
	if !strings.Contains(out, "✗") {
		t.Errorf("view missing failure glyph: %q", out)
	}
}

func TestTentLoader_CtrlCCancelsAndKeepsModel(t *testing.T) {
	called := false
	cancel := func() { called = true }
	m := newTentLoaderModel("Loading…", cancel)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Error("Ctrl-C handler must wait for doneMsg, not quit immediately")
	}
	if next == nil {
		t.Fatal("Update returned nil model")
	}
	if !called {
		t.Error("Ctrl-C should call cancel()")
	}
}

// errFake is a minimal error implementation for tests.
type errFake string

func (e errFake) Error() string { return string(e) }
