package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMoveMonitorsToFrontendRewritesTheManifest: under --agent-scope claude is
// given `clown agent-monitor <socket>` for every job-platform monitor, and the
// original commands are returned to run in the frontend.
func TestMoveMonitorsToFrontendRewritesTheManifest(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	dir := t.TempDir()
	manifest := jobMonitorPlugin{Name: "clown-builtin-jobs", Version: "1", Monitors: []jobMonitorEntry{
		{Name: "ringmaster-monitor", Command: "/rm monitor --session k"},
		{Name: "troupe-receive", Command: "env CLOWN_SESSION_ID=k /troupe receive"},
	}}
	raw, _ := json.Marshal(manifest)
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	specs, err := moveMonitorsToFrontend(dir, "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 || specs[0].Command != "/rm monitor --session k" || specs[1].Command != "env CLOWN_SESSION_ID=k /troupe receive" {
		t.Fatalf("specs = %+v, want the original commands", specs)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	var got jobMonitorPlugin
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for i, m := range got.Monitors {
		want := " agent-monitor " + filepath.Join(runtime, "clown", "monitors", "k", m.Name+".sock")
		if !strings.HasSuffix(m.Command, want) || specs[i].Socket != filepath.Join(runtime, "clown", "monitors", "k", m.Name+".sock") {
			t.Errorf("monitor %s: command %q, socket %q; want the relay reader on its socket", m.Name, m.Command, specs[i].Socket)
		}
	}
}

func TestMoveMonitorsToFrontendRefusesAPrincipalThatIsAPath(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	for _, p := range []string{"../other", "..", ".", "a b", "a:b", ""} {
		if _, err := moveMonitorsToFrontend(t.TempDir(), p); err == nil {
			t.Errorf("principal %q was accepted as a socket directory", p)
		}
	}
}

// TestCopyLinesDropsATrailingFragment: a relay write cut short is replayed
// whole, so the reader must never pass the fragment on.
func TestCopyLinesDropsATrailingFragment(t *testing.T) {
	var out strings.Builder
	got, err := copyLines(&out, strings.NewReader("one\ntwo\nthr"))
	if err != nil || !got || out.String() != "one\ntwo\n" {
		t.Errorf("copyLines = %v, %v, %q; want the two complete lines", got, err, out.String())
	}
}
