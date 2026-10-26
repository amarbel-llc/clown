package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"code.linenisgreat.com/clown/internal/agentscope"
	"code.linenisgreat.com/clown/internal/buildcfg"
	"code.linenisgreat.com/clown/internal/pluginhost"
	"code.linenisgreat.com/clown/internal/staging"
)

// Frontend-side moxins (clown#325, docs/plans/2026-10-26-confined-moxy-sequencing.md).
//
// A confined worker's moxy runs inside the unit, where the operator's uid
// owns every file bound in. A moxin that needs a credential the unit must not
// read (the issue-filer's Cloudflare service token) therefore runs in the
// frontend instead: `moxy serve-moxin --name <moxin>` as a "scope":"frontend"
// stdio server of a synthesized plugin, which clown's plugin host starts
// outside the unit and forwards in over its per-session socket exactly like
// ringmaster and troupe (frontendServerAddrs). The call leaves from the
// frontend; the unit sees only the tool.
//
// To serve one more moxin this way, add a line to frontendMoxins. The moxin
// must have no filesystem or exec effect for its caller, because anything it
// does it does with the operator's authority outside the confinement.

// frontendMoxinPlugin is the synthesized plugin's name; a profile keeps a
// served moxin as "clown-builtin-moxins/<moxin>" and the agent sees its tools
// as mcp__plugin_clown-builtin-moxins_<moxin>__<tool>.
const frontendMoxinPlugin = "clown-builtin-moxins"

// frontendMoxin is one moxin served frontend-side.
type frontendMoxin struct {
	// Name is the moxin's name on MOXIN_PATH.
	Name string
	// Needs names the files the moxin needs to be usable at all (a
	// credential). They are only stat'ed, never opened: the frontend never
	// reads a secret on the moxin's behalf, and a moxin whose file is missing
	// is not served (the tool would only answer an error).
	Needs func() []string
	// MaxCalls caps the session's tool calls to this moxin per hour
	// (moxincap.go): 0 means defaultMoxinMaxCalls, a negative value no cap.
	// The moxin takes caller-chosen text and targets, so the default is small.
	MaxCalls int
}

// maxCalls is the effective per-hour cap, negative for none.
func (m frontendMoxin) maxCalls() int {
	if m.MaxCalls == 0 {
		return defaultMoxinMaxCalls
	}
	return m.MaxCalls
}

// frontendMoxins is the list of moxins served frontend-side under
// --agent-scope. The issue-filer's token file is the one circus provisions
// (`circus provision-issue-filer`).
var frontendMoxins = []frontendMoxin{
	{Name: "issue-filer", Needs: func() []string { return []string{circusStatePath("issue-filer", "cf-access.headers")} }},
}

// circusStatePath is $XDG_STATE_HOME/circus/<parts...> (~/.local/state when
// the variable is unset or not absolute), where circus keeps its host state.
func circusStatePath(parts ...string) string {
	base := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(append([]string{base, "circus"}, parts...)...)
}

// frontendMoxinsDisabled is the kill switch: CLOWN_DISABLE_FRONTEND_MOXINS=1.
func frontendMoxinsDisabled() bool {
	return os.Getenv("CLOWN_DISABLE_FRONTEND_MOXINS") == "1"
}

// pathWithin reports whether p is dir or lies under it (both cleaned and
// symlink-resolved by the caller).
func pathWithin(p, dir string) bool {
	return dir == "/" || p == dir || strings.HasPrefix(p, dir+"/")
}

// inWritable reports whether the unit can write p, or anything under it: p,
// symlinks resolved, is one of writable (already resolved) or under one, or a
// writable path lies inside p (a read-write bind of a child of a moxin
// directory leaves that directory's contents unit-writable).
func inWritable(p string, writable []string) bool {
	real := agentscope.ResolvePath(p)
	for _, w := range writable {
		if pathWithin(real, w) || pathWithin(w, real) {
			return true
		}
	}
	return false
}

// maxMoxinTree bounds the entries examined in one moxin directory.
const maxMoxinTree = 4096

// moxinTreeWritable reports whether anything the frontend would read from the
// moxin directory dir can be written by the unit: a link anywhere inside it
// that resolves into a writable path, or a directory too large to vet.
func moxinTreeWritable(dir string, writable []string) bool {
	n := 0
	bad := false
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			bad = true
			return filepath.SkipAll
		}
		if n++; n > maxMoxinTree {
			bad = true
			return filepath.SkipAll
		}
		if d.Type()&os.ModeSymlink != 0 && inWritable(p, writable) {
			bad = true
			return filepath.SkipAll
		}
		return nil
	})
	return bad
}

// trustedMoxinPath keeps the absolute MOXIN_PATH entries that the unit cannot
// write, and names the rest. The frontend runs the moxin it finds there with
// the operator's authority, so an entry the unit can write (the worktree, a
// read-write bind), or a relative one, which would resolve against the
// worktree, would let the unit plant the code that runs outside it and shadow
// the real moxin.
func trustedMoxinPath(moxinPath string, writable []string) (kept string, dropped []string) {
	var ok []string
	for _, entry := range filepath.SplitList(moxinPath) {
		switch {
		case entry == "":
		case !filepath.IsAbs(entry):
			dropped = append(dropped, entry+" (not absolute)")
		case inWritable(entry, writable):
			dropped = append(dropped, entry+" (writable from inside the unit)")
		default:
			ok = append(ok, entry)
		}
	}
	return strings.Join(ok, string(os.PathListSeparator)), dropped
}

// moxinOnPath reports whether some entry of the MOXIN_PATH-style list holds a
// moxin directory name (a subdirectory with a _moxin.toml), the first entry
// that does being the one moxy serves. A moxin directory that is itself a link
// into a path the unit can write does not count.
func moxinOnPath(name, moxinPath string, writable []string) bool {
	for _, entry := range filepath.SplitList(moxinPath) {
		if entry == "" {
			continue
		}
		if fi, err := os.Stat(filepath.Join(entry, name, "_moxin.toml")); err == nil && fi.Mode().IsRegular() {
			dir := filepath.Join(entry, name)
			return !inWritable(dir, writable) && !inWritable(filepath.Join(dir, "_moxin.toml"), writable) && !moxinTreeWritable(dir, writable)
		}
	}
	return false
}

// moxinPathFor is the moxin search path the served moxins are looked up on,
// the way a moxy the operator runs would see it: clown's own MOXIN_PATH, else
// the MOXIN_PATH one of the plugin dirs gives its "moxy" server (the
// operator's moxy plugin sets it there), else moxy's per-user directory.
func moxinPathFor(pluginDirs []string, writable []string) string {
	if p := os.Getenv("MOXIN_PATH"); p != "" {
		return p
	}
	for _, dir := range pluginDirs {
		// A plugin dir the unit can write could name any MOXIN_PATH.
		if inWritable(dir, writable) {
			continue
		}
		if p := moxinPathOfPlugin(dir); p != "" {
			return p
		}
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "moxy", "moxins")
}

// moxinPathOfPlugin reads the MOXIN_PATH a plugin's clown.json gives a server
// named "moxy" (an httpServers or stdioServers entry), or "".
func moxinPathOfPlugin(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "clown.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		HTTPServers  map[string]struct{ Env map[string]string } `json:"httpServers"`
		StdioServers map[string]struct{ Env map[string]string } `json:"stdioServers"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return ""
	}
	if s, ok := cfg.HTTPServers["moxy"]; ok && s.Env["MOXIN_PATH"] != "" {
		return s.Env["MOXIN_PATH"]
	}
	if s, ok := cfg.StdioServers["moxy"]; ok && s.Env["MOXIN_PATH"] != "" {
		return s.Env["MOXIN_PATH"]
	}
	return ""
}

// planFrontendMoxins picks the moxins to serve and says why it skipped the
// rest. A moxin is served when moxy can find it on moxinPath and every file it
// needs is a regular file (stat follows a link to one).
func planFrontendMoxins(moxins []frontendMoxin, moxinPath string, writable []string) (serve []string, skipped []string) {
	for _, m := range moxins {
		if !moxinOnPath(m.Name, moxinPath, writable) {
			skipped = append(skipped, "moxin "+m.Name+" is not on the moxin path ("+moxinPath+")")
			continue
		}
		missing := ""
		if m.Needs != nil {
			for _, f := range m.Needs() {
				if fi, err := os.Stat(f); f == "" || err != nil || !fi.Mode().IsRegular() {
					missing = f
					break
				}
			}
		}
		if missing != "" {
			skipped = append(skipped, "moxin "+m.Name+" needs "+missing+", which is not a regular file")
			continue
		}
		serve = append(serve, m.Name)
	}
	return serve, skipped
}

// synthFrontendMoxinPluginDir writes the built-in plugin that serves the
// named moxins frontend-side and returns its directory (under the launch's
// staging root, which removes it). Each moxin is one stdioServers entry named
// after it: `moxy serve-moxin --name <moxin>` with MOXIN_PATH, scope frontend.
// Nothing secret is written: the moxin reads its own credential file in the
// frontend. moxyBin must be absolute (the plugin dir holds no binary for the
// bridge to resolve a relative command against).
//
// With a cap (moxincap.go) the command is `<clown> frontend-moxin --name N
// --session S --max-calls K -- <moxy> serve-moxin --name N`: clown's own
// executable proxies the server's stdio and refuses calls past the cap. capExe
// and session name the proxy and the session its count belongs to.
func synthFrontendMoxinPluginDir(root *staging.Root, moxyBin, moxinPath, capExe, session string, serve []frontendMoxin) (string, error) {
	dir, err := root.Dir("clown-moxins-plugin-*")
	if err != nil {
		return "", err
	}
	manifestDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		return "", err
	}
	manifest, err := json.MarshalIndent(map[string]any{"name": frontendMoxinPlugin, "version": "1"}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), manifest, 0o600); err != nil {
		return "", err
	}
	servers := map[string]any{}
	for _, m := range serve {
		command, args := moxyBin, []string{"serve-moxin", "--name", m.Name}
		if n := m.maxCalls(); n > 0 {
			command = capExe
			args = append([]string{"frontend-moxin", "--name", m.Name, "--session", session, "--max-calls", strconv.Itoa(n), "--", moxyBin}, args...)
		}
		servers[m.Name] = map[string]any{
			"command": command,
			"args":    args,
			"env":     map[string]string{"MOXIN_PATH": moxinPath},
			// Forwarded into the agent unit; see the top of this file.
			"scope": pluginhost.ScopeFrontend,
		}
	}
	cb, err := json.MarshalIndent(map[string]any{"version": 1, "stdioServers": servers}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "clown.json"), cb, 0o600); err != nil {
		return "", err
	}
	return dir, nil
}

// unitWritablePaths are the paths the unit of the spec at specPath can write,
// symlinks resolved: its worktree and its read-write binds.
func unitWritablePaths(specPath string) ([]string, error) {
	spec, err := agentscope.LoadSpec(specPath)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range append([]string{spec.Worktree}, spec.BindsRW...) {
		out = append(out, agentscope.ResolvePath(p))
	}
	return out, nil
}

// addFrontendMoxinPlugin is the launch-time step: under --agent-scope only
// (an unconfined session reaches moxy directly), with the stdio bridge and a
// moxy on PATH, it plans the moxins, synthesizes the plugin and returns its
// dir ("" when nothing is served). Skips are reported to note (the
// close-self-run log), never fatal: a confined worker without the moxin still
// works. pluginDirs are the dirs already gathered, for the MOXIN_PATH lookup.
// writable are the resolved paths the unit can write (its worktree and
// read-write binds): the frontend runs moxy and the moxin outside the unit
// with the operator's authority, so nothing it runs or reads its moxin
// definition from may lie under them.
func addFrontendMoxinPlugin(root *staging.Root, confined bool, session string, pluginDirs []string, writable []string, moxins []frontendMoxin, note func(format string, args ...any)) (string, error) {
	if !confined || frontendMoxinsDisabled() {
		return "", nil
	}
	if buildcfg.StdioBridgePath == "" {
		note("frontend moxins not served: this build has no stdio bridge")
		return "", nil
	}
	moxyBin, err := exec.LookPath("moxy")
	if err == nil {
		moxyBin, err = filepath.Abs(moxyBin)
	}
	if err != nil {
		note("frontend moxins not served: moxy is not on PATH")
		return "", nil
	}
	// Run the resolved binary, not a link that could be swapped after this
	// check.
	moxyBin = agentscope.ResolvePath(moxyBin)
	if inWritable(moxyBin, writable) || inWritable(filepath.Dir(moxyBin), writable) {
		note("frontend moxins not served: moxy (%s) is under a path the unit can write", moxyBin)
		return "", nil
	}
	moxinPath, dropped := trustedMoxinPath(moxinPathFor(pluginDirs, writable), writable)
	for _, d := range dropped {
		note("frontend moxins: ignoring moxin path entry %s", d)
	}
	serve, skipped := planFrontendMoxins(moxins, moxinPath, writable)
	for _, s := range skipped {
		note("frontend moxin not served: %s", s)
	}
	if len(serve) == 0 {
		return "", nil
	}
	// A capped moxin runs behind clown's own proxy, which needs this
	// executable and the session its count belongs to; without them it is not
	// served rather than served uncapped.
	capExe := clownExePath()
	var chosen []frontendMoxin
	for _, m := range moxins {
		if !slices.Contains(serve, m.Name) {
			continue
		}
		if m.maxCalls() > 0 && (capExe == "" || session == "") {
			note("frontend moxin not served: %s has a call cap and clown's executable or the session key is unknown", m.Name)
			continue
		}
		chosen = append(chosen, m)
	}
	if len(chosen) == 0 {
		return "", nil
	}
	serve = serve[:0]
	for _, m := range chosen {
		serve = append(serve, m.Name)
	}
	dir, err := synthFrontendMoxinPluginDir(root, moxyBin, moxinPath, capExe, session, chosen)
	if err != nil {
		return "", err
	}
	note("frontend moxins served for the unit: %s", strings.Join(serve, ", "))
	return dir, nil
}
