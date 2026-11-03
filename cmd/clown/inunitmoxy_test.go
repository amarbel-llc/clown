package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/alfa/agentscope"
)

// fakeMoxy writes a stand-in moxy binary into its own directory. runMoxy
// hands it only HOME, MOXIN_PATH and PATH (the point of the scrub), so its
// knobs are files beside it: `show-effective-moxyfile` prints the global
// moxyfile under $HOME plus a resolved-moxins record unless `no-record`
// exists; `narrows` exits with the number in `narrows` (default 0; 1 prints
// a violation line, 2 an unknown-subcommand error); every call appends its
// argv and HOME/MOXIN_PATH to `calls.log`.
type fakeMoxy struct{ bin, dir string }

func newFakeMoxy(t *testing.T) fakeMoxy {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "moxy")
	script := `#!/bin/sh
here=$(dirname "$0")
echo "$* HOME=$HOME MOXIN_PATH=$MOXIN_PATH" >>"$here/calls.log"
case "$1" in
  show-effective-moxyfile)
    cat "$HOME/.config/moxy/moxyfile"
    [ -e "$here/no-record" ] || printf '\n[resolved-moxins]\nfolio = "/nix/store/x-moxins/folio"\n'
    ;;
  narrows)
    mode=0; [ -e "$here/narrows" ] && mode=$(cat "$here/narrows")
    case "$mode" in
      1) echo "allow-tools: child holds grit.commit at use, parent at none"; exit 1 ;;
      2) echo "unknown subcommand narrows" >&2; exit 2 ;;
    esac
    ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return fakeMoxy{bin: bin, dir: dir}
}

func (f fakeMoxy) knob(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f fakeMoxy) calls(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "calls.log"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func inUnitMoxyFixture(t *testing.T) (dir, agentHome, workDir string) {
	t.Helper()
	root := t.TempDir()
	dir, agentHome, workDir = filepath.Join(root, "unit"), filepath.Join(root, "wt", ".tmp", "agent-home"), filepath.Join(root, "wt")
	for _, d := range []string{dir, agentHome, workDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir, agentHome, workDir
}

func TestPrepareInUnitMoxyWithoutAMoxyWritesOnlyTheChild(t *testing.T) {
	dir, agentHome, workDir := inUnitMoxyFixture(t)
	f, err := prepareInUnitMoxy(dir, agentHome, workDir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(agentHome, ".config", "moxy", "moxyfile")
	raw, err := os.ReadFile(filepath.Join(dir, "moxyfile"))
	if err != nil || string(raw) != agentscope.InUnitMoxyfile {
		t.Fatalf("child = %q, %v", raw, err)
	}
	if inUnit, _ := os.ReadFile(child); string(inUnit) != agentscope.InUnitMoxyfile {
		t.Errorf("the bind target does not hold the ceiling: %q", inUnit)
	}
	if !slices.Equal(f.readOnlyBinds(), []string{filepath.Join(dir, "moxyfile") + ":" + child}) {
		t.Errorf("binds = %v", f.readOnlyBinds())
	}
	if f.serverEnv() != nil || f.parent != "" {
		t.Errorf("no moxy, but a parent or env: %+v", f)
	}
}

func TestPrepareInUnitMoxyRendersTheParentAndChecksNarrowing(t *testing.T) {
	dir, agentHome, workDir := inUnitMoxyFixture(t)
	moxy := newFakeMoxy(t)
	f, err := prepareInUnitMoxy(dir, agentHome, workDir, moxy.bin, "/nix/store/x-moxins")
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(agentHome, ".config", "moxy", "parent-moxyfile")
	raw, err := os.ReadFile(f.parent)
	if err != nil || !strings.Contains(string(raw), "resolved-moxins") || !strings.Contains(string(raw), `permission-mode = "enforce"`) {
		t.Fatalf("parent = %q, %v", raw, err)
	}
	if inUnit, _ := os.ReadFile(parent); string(inUnit) != string(raw) {
		t.Errorf("the parent's bind target differs from the parent")
	}
	if !slices.Contains(f.readOnlyBinds(), f.parent+":"+parent) {
		t.Errorf("binds %v lack the parent", f.readOnlyBinds())
	}
	env := f.serverEnv()
	if env["MOXY_PARENT_MOXYFILE"] != parent || env["MOXIN_PATH"] != "/nix/store/x-moxins" {
		t.Errorf("server env = %v", env)
	}
	calls := moxy.calls(t)
	if len(calls) != 2 {
		t.Fatalf("moxy calls = %q", calls)
	}
	// The render runs under the scratch HOME holding only the seed; the
	// narrowing check under the unit's HOME against the provider's cwd.
	scratch := filepath.Join(dir, "moxy-parent-home")
	if calls[0] != "show-effective-moxyfile --dir "+scratch+" HOME="+scratch+" MOXIN_PATH=/nix/store/x-moxins" {
		t.Errorf("render call = %q", calls[0])
	}
	if calls[1] != "narrows --parent "+f.parent+" --child "+workDir+" HOME="+agentHome+" MOXIN_PATH=/nix/store/x-moxins" {
		t.Errorf("narrows call = %q", calls[1])
	}
}

func TestPrepareInUnitMoxyRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		knob, content string
		moxinPath     string
		want          []string
	}{
		"no pinned moxin path":  {moxinPath: "", want: []string{"MOXIN_PATH", "refusing"}},
		"parent without record": {knob: "no-record", moxinPath: "/m", want: []string{"resolved-moxins"}},
		"child widens":          {knob: "narrows", content: "1", moxinPath: "/m", want: []string{"widen the ceiling", "grit.commit"}},
		"narrows unavailable":   {knob: "narrows", content: "2", moxinPath: "/m", want: []string{"could not run", "unknown subcommand"}},
	} {
		t.Run(name, func(t *testing.T) {
			dir, agentHome, workDir := inUnitMoxyFixture(t)
			moxy := newFakeMoxy(t)
			if tc.knob != "" {
				moxy.knob(t, tc.knob, tc.content)
			}
			_, err := prepareInUnitMoxy(dir, agentHome, workDir, moxy.bin, tc.moxinPath)
			if err == nil {
				t.Fatal("want a refusal")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

// runMoxy hands moxy the unit's HOME and MOXIN_PATH and nothing else of the
// frontend's configuration, so a frontend NIX_CONFIG, XDG_CONFIG_HOME or
// MOXY_* setting never shapes the parent or the check.
func TestRunMoxyScrubsTheFrontendEnvironment(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "moxy")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nenv | sort\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", "/frontend/config")
	t.Setenv("MOXY_PARENT_MOXYFILE", "/frontend/parent")
	out, err := runMoxy(bin, "/unit/home", "/pinned", "narrows")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{"HOME=/unit/home\n", "MOXIN_PATH=/pinned\n", "PATH="} {
		if !strings.Contains(got, want) {
			t.Errorf("env lacks %q:\n%s", want, got)
		}
	}
	for _, leaked := range []string{"XDG_CONFIG_HOME", "MOXY_PARENT_MOXYFILE", "/frontend/"} {
		if strings.Contains(got, leaked) {
			t.Errorf("frontend env leaked into moxy: %q in\n%s", leaked, got)
		}
	}
}

func TestInUnitMoxyServerPinsATrustedMoxinPath(t *testing.T) {
	wt := t.TempDir()
	t.Setenv("MOXIN_PATH", "")
	servers := []agentscope.InUnitServer{
		{Name: "other/server", Command: "/bin/other"},
		{Name: inUnitMoxyName, Command: "/nix/store/m/bin/moxy", Env: map[string]string{"MOXIN_PATH": filepath.Join(wt, "planted") + ":/nix/store/m-moxins:relative/moxins"}},
	}
	i, moxy, path := inUnitMoxyServer(servers, []string{wt})
	if i != 1 || moxy != "/nix/store/m/bin/moxy" || path != "/nix/store/m-moxins" {
		t.Errorf("got %d %q %q; want the moxy server with only the store entry kept", i, moxy, path)
	}
	// No MOXIN_PATH on the server: the frontend's, filtered the same way.
	t.Setenv("MOXIN_PATH", "/nix/store/env-moxins")
	servers[1].Env = nil
	if _, _, path := inUnitMoxyServer(servers, []string{wt}); path != "/nix/store/env-moxins" {
		t.Errorf("frontend MOXIN_PATH not used: %q", path)
	}
	if i, _, _ := inUnitMoxyServer(servers[:1], []string{wt}); i != -1 {
		t.Errorf("found a moxy where there is none: %d", i)
	}
}
