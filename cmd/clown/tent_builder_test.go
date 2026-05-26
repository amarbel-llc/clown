package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestBuildAttribute_Matches_GOARCH(t *testing.T) {
	got := buildAttribute()
	want := ""
	switch runtime.GOARCH {
	case "arm64":
		want = "packages.aarch64-linux.tent-image"
	case "amd64":
		want = "packages.x86_64-linux.tent-image"
	default:
		want = "packages." + runtime.GOARCH + "-linux.tent-image"
	}
	if got != want {
		t.Fatalf("buildAttribute() on %s = %q, want %q", runtime.GOARCH, got, want)
	}
}

func TestResolveBuiltTarball_Success(t *testing.T) {
	tarball := filepath.Join(t.TempDir(), "clown-tent.tar.gz")
	if err := os.WriteFile(tarball, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveBuiltTarball(tarball+"\n", "packages.x86_64-linux.tent-image")
	if err != nil {
		t.Fatalf("resolveBuiltTarball: %v", err)
	}
	if got != tarball {
		t.Errorf("got %q, want %q", got, tarball)
	}
}

func TestResolveBuiltTarball_PrintsFirstPath(t *testing.T) {
	tarball := filepath.Join(t.TempDir(), "clown-tent.tar.gz")
	if err := os.WriteFile(tarball, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Simulate nix printing multiple paths (e.g. blank line padding).
	stdout := "\n\n" + tarball + "\n/other/path\n"
	got, err := resolveBuiltTarball(stdout, "attr")
	if err != nil {
		t.Fatalf("resolveBuiltTarball: %v", err)
	}
	if got != tarball {
		t.Errorf("got %q, want %q", got, tarball)
	}
}

func TestResolveBuiltTarball_EmptyOutput(t *testing.T) {
	_, err := resolveBuiltTarball("\n\n   \n", "packages.foo.tent-image")
	if err == nil {
		t.Fatal("expected error for empty out-path list")
	}
	if !strings.Contains(err.Error(), "no out-path") {
		t.Errorf("error = %v, want substring `no out-path`", err)
	}
}

func TestResolveBuiltTarball_MissingFile(t *testing.T) {
	_, err := resolveBuiltTarball("/nonexistent/store/path\n", "attr")
	if err == nil {
		t.Fatal("expected error when tarball missing")
	}
	if !strings.Contains(err.Error(), "locating tent image tarball") {
		t.Errorf("error = %v, want `locating tent image tarball` wrap", err)
	}
}

func TestResolveBuiltTarball_RejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := resolveBuiltTarball(dir+"\n", "attr")
	if err == nil {
		t.Fatal("expected error when out-path is a directory")
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("error = %v, want `is a directory`", err)
	}
}

func TestTentBuilder_LogLinesRingKeepsMostRecent(t *testing.T) {
	m := newTentBuilderModel("t", nil)
	m.maxLines = 3
	for _, line := range []string{"a", "b", "c", "d", "e"} {
		next, _ := m.Update(logLineMsg(line))
		m = next.(tentBuilderModel)
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

func TestTentBuilder_DoneMsgRecordsError(t *testing.T) {
	m := newTentBuilderModel("t", nil)
	next, cmd := m.Update(doneMsg{err: errFake("boom")})
	m = next.(tentBuilderModel)
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

func TestTentBuilder_ViewInProgressShowsLinesUnderSpinner(t *testing.T) {
	m := newTentBuilderModel("Building…", nil)
	next, _ := m.Update(logLineMsg("evaluating derivations"))
	m = next.(tentBuilderModel)
	out := m.View()
	if !strings.Contains(out, "Building…") {
		t.Errorf("view missing title: %q", out)
	}
	if !strings.Contains(out, "evaluating derivations") {
		t.Errorf("view missing log line: %q", out)
	}
}

func TestTentBuilder_ViewDoneSuccessShowsCheckmark(t *testing.T) {
	m := newTentBuilderModel("Building…", nil)
	next, _ := m.Update(doneMsg{err: nil})
	m = next.(tentBuilderModel)
	out := m.View()
	if !strings.Contains(out, "Tent image built") {
		t.Errorf("view missing built message: %q", out)
	}
	if !strings.Contains(out, "✓") {
		t.Errorf("view missing success glyph: %q", out)
	}
}

func TestTentBuilder_ViewDoneFailureShowsCrossmark(t *testing.T) {
	m := newTentBuilderModel("Building…", nil)
	next, _ := m.Update(doneMsg{err: errFake("nope")})
	m = next.(tentBuilderModel)
	out := m.View()
	if !strings.Contains(out, "failed") {
		t.Errorf("view missing failure message: %q", out)
	}
	if !strings.Contains(out, "✗") {
		t.Errorf("view missing failure glyph: %q", out)
	}
}

func TestTentBuilder_CtrlCCancelsAndKeepsModel(t *testing.T) {
	called := false
	cancel := func() { called = true }
	m := newTentBuilderModel("Building…", cancel)
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

func TestRunTentImageBuild_EmptyFlakeRef(t *testing.T) {
	_, err := runTentImageBuild("")
	if err == nil {
		t.Fatal("expected error for empty flakeRef")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %v, want substring `empty`", err)
	}
}
