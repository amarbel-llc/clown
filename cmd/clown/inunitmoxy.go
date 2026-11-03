package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/clown/internal/alfa/agentscope"
)

// The in-unit moxy's ceiling (V3 structural moxy, clown#339;
// docs/plans/2026-11-03-structural-moxy-v3-build-plan.md). clown writes two
// files under the unit's socket dir and binds both read-only into the unit:
//
//   - the child: the ceiling itself (agentscope.InUnitMoxyfile), bound over
//     $HOME/.config/moxy/moxyfile, the in-unit moxy's global moxyfile;
//   - the parent: the same ceiling rendered by `moxy show-effective-moxyfile`
//     under a scratch HOME that holds only the seed, with MOXIN_PATH pinned,
//     so it carries the resolved-moxins record; MOXY_PARENT_MOXYFILE names it
//     and the in-unit moxy refuses, at startup and on every reload, any
//     configuration below the ceiling that would widen it (moxyfile(5)
//     NARROWING).
//
// Before the unit starts, `moxy narrows --parent P --child <workdir>` runs
// here with the unit's HOME and MOXIN_PATH, so a worktree moxyfile that would
// already widen the ceiling refuses the launch instead of the server.

// inUnitMoxyName is the plugin server name of the in-unit moxy, as the
// profile keep-sets and spinclass's R3 name it.
const inUnitMoxyName = "moxy/moxy"

// inUnitMoxyFiles is what prepareInUnitMoxy produced: the frontend paths of
// the child and parent files, where each is bound inside the unit, and the
// pinned MOXIN_PATH.
type inUnitMoxyFiles struct {
	child, childInUnit   string
	parent, parentInUnit string
	moxinPath            string
}

// readOnlyBinds are the plumbing binds (src:dst) for the files.
func (f inUnitMoxyFiles) readOnlyBinds() []string {
	binds := []string{f.child + ":" + f.childInUnit}
	if f.parent != "" {
		binds = append(binds, f.parent+":"+f.parentInUnit)
	}
	return binds
}

// serverEnv is what the in-unit moxy server's environment gains.
func (f inUnitMoxyFiles) serverEnv() map[string]string {
	if f.parent == "" {
		return nil
	}
	return map[string]string{"MOXY_PARENT_MOXYFILE": f.parentInUnit, "MOXIN_PATH": f.moxinPath}
}

// prepareInUnitMoxy writes the ceiling files for a launch. dir is the unit's
// frontend socket dir, agentHome the unit's HOME, workDir the provider's
// working directory (the in-unit moxy's cwd), moxy the moxy binary the unit
// runs (empty: the profile keeps no in-unit moxy, so only the child is
// written, as before), moxinPath the pinned absolute list (empty with a moxy
// refuses: an unpinned in-unit moxy would discover moxins under the
// worker-writable agent home).
func prepareInUnitMoxy(dir, agentHome, workDir, moxy, moxinPath string) (inUnitMoxyFiles, error) {
	var f inUnitMoxyFiles
	cfg := filepath.Join(agentHome, ".config", "moxy")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		return f, err
	}
	ceiling := []byte(agentscope.InUnitMoxyfile)
	f.child = filepath.Join(dir, "moxyfile")
	f.childInUnit = filepath.Join(cfg, "moxyfile")
	if err := os.WriteFile(f.child, ceiling, 0o600); err != nil {
		return f, err
	}
	// The same bytes at the bind target too: a frontend-side `moxy narrows`
	// with HOME=agentHome then sees exactly what the unit will (the bind
	// makes it read-only inside).
	if err := os.WriteFile(f.childInUnit, ceiling, 0o600); err != nil {
		return f, err
	}
	if moxy == "" {
		return f, nil
	}
	if moxinPath == "" {
		return f, errors.New("in-unit moxy: no pinned absolute MOXIN_PATH the unit cannot write; refusing (an unpinned moxy would discover moxins under the agent home)")
	}
	f.moxinPath = moxinPath

	// The parent: the ceiling rendered under a scratch HOME holding only it.
	scratch := filepath.Join(dir, "moxy-parent-home")
	if err := os.MkdirAll(filepath.Join(scratch, ".config", "moxy"), 0o700); err != nil {
		return f, err
	}
	if err := os.WriteFile(filepath.Join(scratch, ".config", "moxy", "moxyfile"), ceiling, 0o600); err != nil {
		return f, err
	}
	rendered, err := runMoxy(moxy, scratch, moxinPath, "show-effective-moxyfile", "--dir", scratch)
	if err != nil {
		return f, fmt.Errorf("in-unit moxy: rendering the parent moxyfile: %w", err)
	}
	if !bytes.Contains(rendered, []byte("resolved-moxins")) {
		return f, errors.New("in-unit moxy: the rendered parent moxyfile carries no resolved-moxins record, so moxy could not enforce narrowing; is moxy older than its FDR 0015?")
	}
	f.parent = filepath.Join(dir, "parent-moxyfile")
	f.parentInUnit = filepath.Join(cfg, "parent-moxyfile")
	for _, p := range []string{f.parent, f.parentInUnit} {
		if err := os.WriteFile(p, rendered, 0o600); err != nil {
			return f, err
		}
	}

	// The self-check: the unit's own view (HOME = agent home, its global
	// moxyfile, the worktree's layers) must narrow the parent now, or the
	// launch is refused with moxy's lines.
	out, err := runMoxy(moxy, agentHome, moxinPath, "narrows", "--parent", f.parent, "--child", workDir)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return f, fmt.Errorf("in-unit moxy: the unit's moxyfile layers would widen the ceiling (a moxyfile in the worktree?); moxy narrows says:\n%s", strings.TrimSpace(string(out)))
		}
		return f, fmt.Errorf("in-unit moxy: `moxy narrows` could not run (moxy at %s may predate its narrowing check): %w", moxy, err)
	}
	return f, nil
}

// runMoxy runs the moxy binary with HOME and MOXIN_PATH set as the unit's
// moxy would see them and nothing else of the frontend's configuration (PATH
// stays, for moxy's own helpers). It returns stdout; on a non-zero exit the
// error wraps *exec.ExitError and the returned bytes are stdout+stderr.
func runMoxy(moxy, home, moxinPath string, args ...string) ([]byte, error) {
	cmd := exec.Command(moxy, args...)
	cmd.Env = []string{"HOME=" + home, "MOXIN_PATH=" + moxinPath, "PATH=" + os.Getenv("PATH")}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		combined := append(stdout.Bytes(), stderr.Bytes()...)
		return combined, fmt.Errorf("%s %s: %w: %s", filepath.Base(moxy), strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// inUnitMoxyServer finds the in-unit moxy among the servers the relay starts
// inside the unit, and the MOXIN_PATH its plugin gave it (else the
// frontend's), kept to the entries the unit cannot write (trustedMoxinPath).
// It returns the server's index (-1: none), its binary and the pinned path.
func inUnitMoxyServer(inUnit []agentscope.InUnitServer, writable []string) (index int, moxy, moxinPath string) {
	for i, s := range inUnit {
		if s.Name != inUnitMoxyName {
			continue
		}
		raw := s.Env["MOXIN_PATH"]
		if raw == "" {
			raw = os.Getenv("MOXIN_PATH")
		}
		kept, _ := trustedMoxinPath(raw, writable)
		return i, s.Command, kept
	}
	return -1, "", ""
}
