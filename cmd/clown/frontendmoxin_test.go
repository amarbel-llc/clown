package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"code.linenisgreat.com/clown/internal/agentscope"
	"code.linenisgreat.com/clown/internal/buildcfg"
	"code.linenisgreat.com/clown/internal/pluginhost"
)

// fakeMoxinDir makes <root>/<name>/_moxin.toml and returns root, a MOXIN_PATH
// entry.
func fakeMoxinDir(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name, "_moxin.toml"), []byte("schema = 1\nname = \""+name+"\"\ndescription = \"fake\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// tokenFile makes the circus issue-filer token file under a fresh
// XDG_STATE_HOME with fake content, and returns its path.
func tokenFile(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	path := circusStatePath("issue-filer", "cf-access.headers")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("CF-Access-Client-Id: fake-id\nCF-Access-Client-Secret: fake-secret-do-not-leak\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCircusStatePath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/srv/state")
	if got := circusStatePath("issue-filer", "x"); got != "/srv/state/circus/issue-filer/x" {
		t.Errorf("got %s", got)
	}
	t.Setenv("XDG_STATE_HOME", "relative/is/ignored")
	t.Setenv("HOME", "/home/op")
	if got := circusStatePath("a"); got != "/home/op/.local/state/circus/a" {
		t.Errorf("relative XDG_STATE_HOME: got %s", got)
	}
}

func TestPlanServesAMoxinOnlyWhenFoundAndItsFilesExist(t *testing.T) {
	tok := tokenFile(t)
	moxins := []frontendMoxin{{Name: "issue-filer", Needs: func() []string { return []string{tok} }}}
	onPath := fakeMoxinDir(t, "issue-filer")

	serve, skipped := planFrontendMoxins(moxins, onPath, nil)
	if !slices.Equal(serve, []string{"issue-filer"}) || len(skipped) != 0 {
		t.Fatalf("found + token: serve %v skipped %v", serve, skipped)
	}

	// A path list: the moxin may be on a later entry.
	serve, _ = planFrontendMoxins(moxins, t.TempDir()+string(os.PathListSeparator)+onPath, nil)
	if !slices.Equal(serve, []string{"issue-filer"}) {
		t.Errorf("later path entry: serve %v", serve)
	}

	serve, skipped = planFrontendMoxins(moxins, t.TempDir(), nil)
	if len(serve) != 0 || len(skipped) != 1 || !strings.Contains(skipped[0], "not on the moxin path") {
		t.Errorf("not on the path: serve %v skipped %v", serve, skipped)
	}

	if err := os.Remove(tok); err != nil {
		t.Fatal(err)
	}
	serve, skipped = planFrontendMoxins(moxins, onPath, nil)
	if len(serve) != 0 || len(skipped) != 1 || !strings.Contains(skipped[0], "needs") {
		t.Errorf("token missing: serve %v skipped %v", serve, skipped)
	}

	// A directory where the file should be is not a regular file.
	if err := os.MkdirAll(tok, 0o700); err != nil {
		t.Fatal(err)
	}
	if serve, _ = planFrontendMoxins(moxins, onPath, nil); len(serve) != 0 {
		t.Errorf("a directory in place of the token file: serve %v", serve)
	}

	// The plan never opens the file: a file nobody can read still counts.
	if err := os.Remove(tok); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tok, []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if serve, _ = planFrontendMoxins(moxins, onPath, nil); !slices.Equal(serve, []string{"issue-filer"}) {
		t.Errorf("an unreadable token file: serve %v (stat only, never read)", serve)
	}
}

// The mechanism is a list: a second moxin is one more entry, and each is
// judged on its own.
func TestPlanIsAListOfMoxins(t *testing.T) {
	root := fakeMoxinDir(t, "alpha")
	moxins := []frontendMoxin{{Name: "alpha"}, {Name: "beta"}}
	serve, skipped := planFrontendMoxins(moxins, root, nil)
	if !slices.Equal(serve, []string{"alpha"}) || len(skipped) != 1 || !strings.Contains(skipped[0], "beta") {
		t.Fatalf("serve %v skipped %v", serve, skipped)
	}
}

func TestMoxinPathLayers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	t.Setenv("MOXIN_PATH", "/env/moxins")
	if got := moxinPathFor(nil, nil); got != "/env/moxins" {
		t.Errorf("env: %s", got)
	}
	t.Setenv("MOXIN_PATH", "")
	plug := t.TempDir()
	if err := os.WriteFile(filepath.Join(plug, "clown.json"), []byte(`{"version":1,"stdioServers":{"moxy":{"command":"moxy","env":{"MOXIN_PATH":"/plugin/moxins"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := moxinPathFor([]string{t.TempDir(), plug}, nil); got != "/plugin/moxins" {
		t.Errorf("plugin's moxy server env: %s", got)
	}
	httpPlug := t.TempDir()
	if err := os.WriteFile(filepath.Join(httpPlug, "clown.json"), []byte(`{"version":1,"httpServers":{"moxy":{"command":"moxy","env":{"MOXIN_PATH":"/http/moxins"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := moxinPathFor([]string{httpPlug}, nil); got != "/http/moxins" {
		t.Errorf("http server env: %s", got)
	}
	if got := moxinPathFor([]string{t.TempDir()}, nil); got != "/cfg/moxy/moxins" {
		t.Errorf("per-user dir: %s", got)
	}
}

// The synthesized plugin names one frontend-scope stdio server per moxin, runs
// `moxy serve-moxin --name <moxin>`, carries MOXIN_PATH, loads through the
// plugin host's own loader, and holds no secret.
func TestSynthFrontendMoxinPluginDir(t *testing.T) {
	tok := tokenFile(t)
	root := testStagingRoot(t)
	dir, err := synthFrontendMoxinPluginDir(root, "/nix/store/m/bin/moxy", "/moxins", []string{"issue-filer", "other"})
	if err != nil {
		t.Fatal(err)
	}
	name, err := pluginhost.PluginName(dir)
	if err != nil || name != frontendMoxinPlugin {
		t.Fatalf("plugin name %q, %v", name, err)
	}
	cfg, err := pluginhost.LoadClownConfig(dir)
	if err != nil {
		t.Fatalf("clown.json does not load: %v", err)
	}
	for _, m := range []string{"issue-filer", "other"} {
		s, ok := cfg.StdioServers[m]
		if !ok {
			t.Fatalf("no stdio server %q in %v", m, cfg.StdioServers)
		}
		if s.Command != "/nix/store/m/bin/moxy" || !slices.Equal(s.Args, []string{"serve-moxin", "--name", m}) {
			t.Errorf("%s runs %s %v", m, s.Command, s.Args)
		}
		if s.Env["MOXIN_PATH"] != "/moxins" {
			t.Errorf("%s env %v", m, s.Env)
		}
		if s.Scope != pluginhost.ScopeFrontend {
			t.Errorf("%s scope %q: only a frontend-scope server is forwarded into the unit and kept out of it", m, s.Scope)
		}
	}
	// No secret and no token path in anything written.
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, bad := range []string{"fake-secret-do-not-leak", "fake-id", tok, "cf-access"} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s contains %q", p, bad)
			}
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is mode %v", p, fi.Mode())
		}
		return nil
	})
}

func TestAddFrontendMoxinPlugin(t *testing.T) {
	tokenFile(t)
	moxins := []frontendMoxin{{Name: "issue-filer", Needs: func() []string { return []string{circusStatePath("issue-filer", "cf-access.headers")} }}}
	moxinDir := fakeMoxinDir(t, "issue-filer")
	t.Setenv("MOXIN_PATH", moxinDir)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "moxy"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	old := buildcfg.StdioBridgePath
	buildcfg.StdioBridgePath = "/bridge"
	t.Cleanup(func() { buildcfg.StdioBridgePath = old })
	t.Setenv("CLOWN_DISABLE_FRONTEND_MOXINS", "")

	var mu sync.Mutex
	var notes []string
	note := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		notes = append(notes, format)
	}
	root := testStagingRoot(t)

	// Unconfined: nothing, quietly.
	if dir, err := addFrontendMoxinPlugin(root, false, nil, nil, moxins, note); dir != "" || err != nil || len(notes) != 0 {
		t.Fatalf("unconfined: %q %v %v", dir, err, notes)
	}
	// Confined: served, and the plugin names moxy by absolute path.
	dir, err := addFrontendMoxinPlugin(root, true, nil, nil, moxins, note)
	if err != nil || dir == "" {
		t.Fatalf("confined: %q %v", dir, err)
	}
	cfg, err := pluginhost.LoadClownConfig(dir)
	if err != nil || !filepath.IsAbs(cfg.StdioServers["issue-filer"].Command) || filepath.Base(cfg.StdioServers["issue-filer"].Command) != "moxy" {
		t.Fatalf("clown.json: %+v %v", cfg, err)
	}
	// The kill switch.
	t.Setenv("CLOWN_DISABLE_FRONTEND_MOXINS", "1")
	if dir, err := addFrontendMoxinPlugin(root, true, nil, nil, moxins, note); dir != "" || err != nil {
		t.Errorf("disabled: %q %v", dir, err)
	}
	t.Setenv("CLOWN_DISABLE_FRONTEND_MOXINS", "")
	// No moxy on PATH: skipped with a note, no error.
	t.Setenv("PATH", t.TempDir())
	notes = nil
	if dir, err := addFrontendMoxinPlugin(root, true, nil, nil, moxins, note); dir != "" || err != nil || len(notes) != 1 {
		t.Errorf("no moxy: %q %v %v", dir, err, notes)
	}
	// A build without the bridge cannot declare stdio servers.
	t.Setenv("PATH", bin)
	buildcfg.StdioBridgePath = ""
	notes = nil
	if dir, err := addFrontendMoxinPlugin(root, true, nil, nil, moxins, note); dir != "" || err != nil || len(notes) != 1 {
		t.Errorf("no bridge: %q %v %v", dir, err, notes)
	}
}

// Review finding: the frontend runs moxy and the moxin outside the unit with
// the operator's authority, so nothing it runs or reads its moxin definition
// from may be writable by the unit (its worktree and read-write binds).

func agentscopeResolve(p string) string { return agentscope.ResolvePath(p) }

func writableWorktree(t *testing.T) (worktree string, writable []string) {
	t.Helper()
	wt := t.TempDir()
	return wt, []string{agentscopeResolve(wt)}
}

func TestTrustedMoxinPathDropsWritableAndRelativeEntries(t *testing.T) {
	wt, writable := writableWorktree(t)
	good := t.TempDir()
	inWT := filepath.Join(wt, "moxins")
	if err := os.MkdirAll(inWT, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(inWT, link); err != nil {
		t.Fatal(err)
	}
	list := strings.Join([]string{"./moxins", ".moxy/moxins", inWT, link, "", good}, string(os.PathListSeparator))
	kept, dropped := trustedMoxinPath(list, writable)
	if kept != good {
		t.Errorf("kept %q, want only %q", kept, good)
	}
	if len(dropped) != 4 {
		t.Errorf("dropped %v, want the two relative entries, the worktree entry and the link into it", dropped)
	}
}

func TestAMoxinUnderAWritablePathIsNotServed(t *testing.T) {
	tok := tokenFile(t)
	moxins := []frontendMoxin{{Name: "issue-filer", Needs: func() []string { return []string{tok} }}}
	wt, writable := writableWorktree(t)

	planted := fakeMoxinDir(t, "issue-filer") // somewhere else, then reached through the worktree
	inWT := filepath.Join(wt, "moxins")
	if err := os.Symlink(planted, inWT); err != nil {
		t.Fatal(err)
	}
	// The path is not under the worktree by name but its moxin dir is a link
	// into it: a safe entry holding a link to a writable moxin.
	entry := t.TempDir()
	real := filepath.Join(wt, "issue-filer")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "_moxin.toml"), []byte("schema = 1\nname = \"issue-filer\"\ndescription = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(entry, "issue-filer")); err != nil {
		t.Fatal(err)
	}
	serve, skipped := planFrontendMoxins(moxins, entry, writable)
	if len(serve) != 0 || len(skipped) != 1 {
		t.Errorf("moxin dir linked into the worktree: serve %v skipped %v", serve, skipped)
	}
	// The same moxin on a path the unit cannot write is served.
	if serve, _ = planFrontendMoxins(moxins, planted, writable); !slices.Equal(serve, []string{"issue-filer"}) {
		t.Errorf("a trusted entry: serve %v", serve)
	}
}

// Second review (spinclass/light-cedar): a writable bind INSIDE the vetted
// object, and a link inside the moxin directory, leave its contents
// unit-writable although the directory itself resolves outside every
// writable path.
func TestAMoxinWithAWritableChildOrAnInnerLinkIsNotServed(t *testing.T) {
	tok := tokenFile(t)
	moxins := []frontendMoxin{{Name: "issue-filer", Needs: func() []string { return []string{tok} }}}
	_, writable := writableWorktree(t)

	entry := fakeMoxinDir(t, "issue-filer")
	if serve, _ := planFrontendMoxins(moxins, entry, writable); !slices.Equal(serve, []string{"issue-filer"}) {
		t.Fatalf("control: serve %v", serve)
	}
	// A read-write bind of a child of the moxin directory.
	child := filepath.Join(entry, "issue-filer", "scripts")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	withChild := append(slices.Clone(writable), agentscope.ResolvePath(child))
	if serve, skipped := planFrontendMoxins(moxins, entry, withChild); len(serve) != 0 || len(skipped) != 1 {
		t.Errorf("rw child of the moxin dir: serve %v skipped %v", serve, skipped)
	}
	// ... and of the MOXIN_PATH entry (trustedMoxinPath drops the entry).
	if kept, dropped := trustedMoxinPath(entry, withChild); kept != "" || len(dropped) != 1 {
		t.Errorf("rw child of a path entry: kept %q dropped %v", kept, dropped)
	}
	// A link inside the moxin directory that resolves into the worktree.
	wt := writable[0]
	other := fakeMoxinDir(t, "issue-filer")
	if err := os.Symlink(wt, filepath.Join(other, "issue-filer", "lib")); err != nil {
		t.Fatal(err)
	}
	if serve, skipped := planFrontendMoxins(moxins, other, writable); len(serve) != 0 || len(skipped) != 1 {
		t.Errorf("inner link into the worktree: serve %v skipped %v", serve, skipped)
	}
	// A link inside that stays outside every writable path is fine.
	fine := fakeMoxinDir(t, "issue-filer")
	if err := os.Symlink(t.TempDir(), filepath.Join(fine, "issue-filer", "lib")); err != nil {
		t.Fatal(err)
	}
	if serve, _ := planFrontendMoxins(moxins, fine, writable); !slices.Equal(serve, []string{"issue-filer"}) {
		t.Errorf("inner link elsewhere: serve %v", serve)
	}
}

func TestPathWithinRoot(t *testing.T) {
	if !pathWithin("/anything/at/all", "/") {
		t.Error(`nothing is within "/"`)
	}
	if pathWithin("/a/bc", "/a/b") || !pathWithin("/a/b/c", "/a/b") || !pathWithin("/a/b", "/a/b") {
		t.Error("pathWithin is not the component-wise prefix test")
	}
}

// The plugin runs the resolved moxy, not a link that could be swapped later.
func TestAddFrontendMoxinPluginRunsTheResolvedMoxy(t *testing.T) {
	tok := tokenFile(t)
	moxins := []frontendMoxin{{Name: "issue-filer", Needs: func() []string { return []string{tok} }}}
	real := t.TempDir()
	if err := os.WriteFile(filepath.Join(real, "moxy-real"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(filepath.Join(real, "moxy-real"), filepath.Join(bin, "moxy")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("MOXIN_PATH", fakeMoxinDir(t, "issue-filer"))
	t.Setenv("CLOWN_DISABLE_FRONTEND_MOXINS", "")
	old := buildcfg.StdioBridgePath
	buildcfg.StdioBridgePath = "/bridge"
	t.Cleanup(func() { buildcfg.StdioBridgePath = old })
	dir, err := addFrontendMoxinPlugin(testStagingRoot(t), true, nil, nil, moxins, func(string, ...any) {})
	if err != nil || dir == "" {
		t.Fatalf("%q %v", dir, err)
	}
	cfg, err := pluginhost.LoadClownConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.StdioServers["issue-filer"].Command, agentscope.ResolvePath(filepath.Join(real, "moxy-real")); got != want {
		t.Errorf("command %q, want the resolved binary %q", got, want)
	}
}

func TestMoxinPathIgnoresAPluginDirTheUnitCanWrite(t *testing.T) {
	t.Setenv("MOXIN_PATH", "")
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	wt, writable := writableWorktree(t)
	if err := os.WriteFile(filepath.Join(wt, "clown.json"), []byte(`{"version":1,"stdioServers":{"moxy":{"command":"moxy","env":{"MOXIN_PATH":"/planted"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := moxinPathFor([]string{wt}, writable); got != "/cfg/moxy/moxins" {
		t.Errorf("a plugin dir inside the worktree set MOXIN_PATH: %s", got)
	}
}

func TestAddFrontendMoxinPluginKeepsOnlyTrustedPathsAndABinaryTheUnitCannotWrite(t *testing.T) {
	tok := tokenFile(t)
	moxins := []frontendMoxin{{Name: "issue-filer", Needs: func() []string { return []string{tok} }}}
	wt, writable := writableWorktree(t)
	good := fakeMoxinDir(t, "issue-filer")
	planted := filepath.Join(wt, "moxins")
	if err := os.MkdirAll(filepath.Join(planted, "issue-filer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planted, "issue-filer", "_moxin.toml"), []byte("schema = 1\nname = \"issue-filer\"\ndescription = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The unit-writable entry comes first and would shadow the real moxin.
	t.Setenv("MOXIN_PATH", planted+string(os.PathListSeparator)+good)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "moxy"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	old := buildcfg.StdioBridgePath
	buildcfg.StdioBridgePath = "/bridge"
	t.Cleanup(func() { buildcfg.StdioBridgePath = old })
	t.Setenv("CLOWN_DISABLE_FRONTEND_MOXINS", "")
	var notes []string
	note := func(format string, args ...any) { notes = append(notes, format) }
	root := testStagingRoot(t)

	dir, err := addFrontendMoxinPlugin(root, true, nil, writable, moxins, note)
	if err != nil || dir == "" {
		t.Fatalf("%q %v", dir, err)
	}
	cfg, err := pluginhost.LoadClownConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.StdioServers["issue-filer"].Env["MOXIN_PATH"]; got != good {
		t.Errorf("MOXIN_PATH = %q, want only the trusted entry %q (the planted one must not shadow it)", got, good)
	}
	if len(notes) < 2 {
		t.Errorf("the dropped entry was not noted: %v", notes)
	}

	// A moxy found in a directory the unit can write is never run.
	wbin := filepath.Join(wt, "bin")
	if err := os.MkdirAll(wbin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wbin, "moxy"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wbin)
	notes = nil
	if dir, err := addFrontendMoxinPlugin(root, true, nil, writable, moxins, note); dir != "" || err != nil || len(notes) != 1 {
		t.Errorf("moxy in the worktree: %q %v %v", dir, err, notes)
	}
}

// The profile that keeps the served moxin keeps it by "<plugin>/<moxin>", the
// name a plugin host discovers for it.
func TestFrontendMoxinServerNameMatchesTheProfileKeepName(t *testing.T) {
	if got := frontendMoxinPlugin + "/issue-filer"; got != "clown-builtin-moxins/issue-filer" {
		t.Fatalf("server name %q: update the worker builtin's context_servers", got)
	}
	if !slices.ContainsFunc(frontendMoxins, func(m frontendMoxin) bool { return m.Name == "issue-filer" }) {
		t.Fatal("issue-filer is not in frontendMoxins")
	}
}
