package jugglerrun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/jugglerrun/jugglerruntest"
)

func spawnForNarrows(h *harness, task string) (*ChildRecord, error) {
	rec, _, err := SpawnChild(context.Background(), h.deps, SpawnRequest{
		Brief: templateBytes(), Task: []byte(task), RunKey: "rec-42", JugglerBin: "/bin/juggler",
		MoxinPath: "/spawner/moxins", SpawnerDir: "/spawner/cwd",
	})
	return rec, err
}

func mintCalls(t *testing.T, h *harness) int {
	t.Helper()
	n := 0
	for _, c := range h.fakes.Calls(t, "troupe") {
		if c.Argv[0] == "mint" {
			n++
		}
	}
	return n
}

func TestSpawnChild_NarrowingBriefIsSpawnedUnderItsParentReference(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	newRun(t, h, "rec-42")
	t.Setenv(SessionIDEnv, "spawner-principal")
	t.Setenv("TROUPE_XMPP_USER", "spawner-user")
	rec, err := spawnForNarrows(h, testTask)
	if err != nil {
		t.Fatal(err)
	}

	moxy := h.fakes.Calls(t, "moxy")
	if len(moxy) != 3 || moxy[0].Argv[0] != "render" ||
		strings.Join(moxy[1].Argv, " ") != "show-effective-moxyfile --dir /spawner/cwd" ||
		moxy[1].Env["MOXIN_PATH"] != "/spawner/moxins" || moxy[2].Argv[0] != "narrows" {
		t.Fatalf("moxy calls = %+v", moxy)
	}
	for _, c := range moxy {
		if _, ok := c.Env[SessionIDEnv]; ok {
			t.Errorf("the spawner's principal must not reach moxy: %v", c.Env)
		}
		if _, ok := c.Env["TROUPE_XMPP_USER"]; ok {
			t.Errorf("the spawner's XMPP identity must not reach moxy: %v", c.Env)
		}
	}
	units := h.fakes.Calls(t, "systemd-run")
	argv := units[len(units)-1].Argv
	if tail := strings.Join(argv[len(argv)-2:], " "); tail != "--moxy "+rec.MoxyBin || !filepath.IsAbs(rec.MoxyBin) {
		t.Errorf("the unit must serve with the checking moxy %q: argv tail %q", rec.MoxyBin, tail)
	}
	if got := strings.Join(moxy[2].Argv, " "); got != "narrows --parent "+rec.ParentMoxyfile+" --child "+h.deps.Store.stagedChildMoxyfilePath("rec-42", rec.BriefDigest) {
		t.Errorf("narrows argv = %q", got)
	}
	parent, err := os.ReadFile(rec.ParentMoxyfile)
	if err != nil || string(parent) != jugglerruntest.FakeEffectiveMoxyfile {
		t.Fatalf("parent reference = %q, %v", parent, err)
	}
	if fi, _ := os.Stat(rec.ParentMoxyfile); fi.Mode().Perm() != 0o600 {
		t.Errorf("parent reference mode = %o", fi.Mode().Perm())
	}
	if rec.ParentMoxyfileDigest != sha256Digest(parent) || rec.Launch().ParentMoxyfileDigest != rec.ParentMoxyfileDigest || rec.MoxyBin != h.fakes.Moxy {
		t.Errorf("record = %+v", rec)
	}
	if got := unitSetenvs(t, h, "MOXY_PARENT_MOXYFILE"); len(got) != 1 || got[0] != "--setenv=MOXY_PARENT_MOXYFILE="+rec.ParentMoxyfile {
		t.Errorf("unit MOXY_PARENT_MOXYFILE = %q", got)
	}
}

func TestSpawnChild_WideningBriefIsRefusedBeforeTheMint(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	res := newRun(t, h, "rec-42")
	mints := mintCalls(t, h)
	h.fakes.Fail(t, "moxy", "narrows-widen")
	_, err := spawnForNarrows(h, testTask)
	if err == nil || err.Error() != "brief moxyfile widens the spawner's:\n"+strings.Join(jugglerruntest.FakeNarrowsViolations, "\n") {
		t.Fatalf("err = %v", err)
	}
	if n := mintCalls(t, h); n != mints {
		t.Errorf("nothing may be minted: %d mints became %d", mints, n)
	}
	if len(h.fakes.Calls(t, "systemd-run")) != 0 || len(h.fakes.Jobs(t, testRoot)) != 0 {
		t.Error("nothing may be started")
	}
	digest := BriefDigest(templateBytes(), []byte(testTask))
	for _, p := range []string{h.deps.Store.stagedParentMoxyfilePath("rec-42", digest), h.deps.Store.stagedChildMoxyfilePath("rec-42", digest)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s must be removed: %v", p, err)
		}
	}
	data, err := JobLedger(context.Background(), h.deps, res.RunJob, "")
	if err != nil {
		t.Fatal(err)
	}
	var l RunLedger
	if err := json.Unmarshal(data, &l); err != nil {
		t.Fatal(err)
	}
	refusals := callsOf(l, "narrows")
	if len(refusals) != 1 || refusals[0].Kind != "refusal" || refusals[0].OK || len(refusals[0].Violations) != 2 ||
		refusals[0].Violations[0].Key != "allow-tools" || refusals[0].ViolationsMissing ||
		refusals[0].MoxyBin != h.fakes.Moxy || !strings.HasPrefix(refusals[0].ParentMoxyfileDigest, "sha256:") {
		t.Errorf("ledger = %s", data)
	}
}

func TestSpawnChild_RefusalWithoutStructuredViolationsSaysSo(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	res := newRun(t, h, "rec-42")
	h.fakes.Fail(t, "moxy", "narrows-widen")
	h.fakes.Fail(t, "moxy", "narrows-nojson")
	if _, err := spawnForNarrows(h, testTask); err == nil || !strings.Contains(err.Error(), "servers.shell") {
		t.Fatalf("err = %v", err)
	}
	data, _ := JobLedger(context.Background(), h.deps, res.RunJob, "")
	if !strings.Contains(string(data), `"violations_missing": true`) || strings.Contains(string(data), `"violations": [`) {
		t.Errorf("ledger = %s", data)
	}
}

func TestSpawnChild_MintFailureRemovesTheStagedMoxyfiles(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	newRun(t, h, "rec-42")
	h.fakes.Fail(t, "troupe", "mint")
	if _, err := spawnForNarrows(h, testTask); err == nil || !strings.Contains(err.Error(), "minting the agent principal") {
		t.Fatalf("err = %v", err)
	}
	digest := BriefDigest(templateBytes(), []byte(testTask))
	for _, p := range []string{h.deps.Store.stagedParentMoxyfilePath("rec-42", digest), h.deps.Store.stagedChildMoxyfilePath("rec-42", digest)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s must be removed after a failed mint: %v", p, err)
		}
	}
}

func TestSpawnChild_BrokenNarrowsCheckRefuses(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	newRun(t, h, "rec-42")
	h.fakes.Fail(t, "moxy", "narrows-broken")
	if _, err := spawnForNarrows(h, testTask); err == nil || !strings.Contains(err.Error(), "expected '='") {
		t.Fatalf("exit 2 must refuse with moxy's stderr: %v", err)
	}
	if mintCalls(t, h) != 1 { // the root's only
		t.Error("nothing may be minted")
	}
}

func TestSpawnChild_RenderFailureOrMissingMoxyRefuses(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	newRun(t, h, "rec-42")
	h.fakes.Fail(t, "moxy", "show-effective-moxyfile")
	if _, err := spawnForNarrows(h, "a"); err == nil || !strings.Contains(err.Error(), "rendering the spawner's effective moxyfile") {
		t.Errorf("render failure: %v", err)
	}

	h.deps.MoxyCLI = ExecMoxyCLI{Bin: "/nonexistent/moxy"}
	if _, err := spawnForNarrows(h, "b"); err == nil || !strings.Contains(err.Error(), "could not be run") {
		t.Errorf("missing moxy: %v", err)
	}
	h.deps.MoxyCLI = nil
	if _, err := spawnForNarrows(h, "c"); err == nil || !strings.Contains(err.Error(), "no moxy binary") {
		t.Errorf("no MoxyCLI configured: %v", err)
	}
	if mintCalls(t, h) != 1 {
		t.Error("nothing may be minted")
	}
}

// clown#261 point 2/3: a tools entry render can't resolve (moxy's own
// "not invertible" case, e.g. a bare builtin name with no separator at
// all) refuses the spawn pre-mint, with its own ledger refusal kind.
func TestSpawnChild_UnresolvableToolRefusesPreMint(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	res := newRun(t, h, "rec-42")
	mints := mintCalls(t, h)
	if err := os.MkdirAll(filepath.Join(h.fakes.Dir, "fail"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.fakes.Dir, "fail", "moxy-render-unresolvable"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := spawnForNarrows(h, testTask)
	if err == nil || !strings.Contains(err.Error(), "ring_create_issue") || !strings.Contains(err.Error(), "not invertible") {
		t.Fatalf("err = %v", err)
	}
	if n := mintCalls(t, h); n != mints {
		t.Errorf("nothing may be minted: %d mints became %d", mints, n)
	}
	digest := BriefDigest(templateBytes(), []byte(testTask))
	for _, p := range []string{h.deps.Store.stagedParentMoxyfilePath("rec-42", digest), h.deps.Store.stagedChildMoxyfilePath("rec-42", digest)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s must not be staged: %v", p, err)
		}
	}
	data, err := JobLedger(context.Background(), h.deps, res.RunJob, "")
	if err != nil {
		t.Fatal(err)
	}
	var l RunLedger
	if err := json.Unmarshal(data, &l); err != nil {
		t.Fatal(err)
	}
	refusals := callsOf(l, "tool-resolve")
	if len(refusals) != 1 || refusals[0].Kind != "refusal" || refusals[0].OK || !strings.Contains(refusals[0].Reason, "ring_create_issue") {
		t.Errorf("ledger = %s", data)
	}
}

func writeFakeMoxyScript(t *testing.T, stdout string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	data := filepath.Join(dir, "out")
	if err := os.WriteFile(data, []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "moxy")
	script := fmt.Sprintf("#!/bin/sh\ncat %q\nexit %d\n", data, exitCode)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// clown#261 review LOW 6: ResolveToolName's parsing must be strict — a
// second tab or a second line is refused, never silently taking the first
// match.
func TestExecMoxyCLI_ResolveToolName(t *testing.T) {
	cases := []struct {
		name                             string
		stdout                           string
		exitCode                         int
		wantServer, wantTool, wantErrSub string
	}{
		{name: "ok", stdout: "ring\tcreate_issue\n", wantServer: "ring", wantTool: "create_issue"},
		{name: "extra tab", stdout: "a\tb\tc\n", wantErrSub: "exactly one line"},
		{name: "extra line", stdout: "a\tb\nc\td\n", wantErrSub: "exactly one line"},
		{name: "no tab", stdout: "a\n", wantErrSub: "exactly one line"},
		{name: "empty server", stdout: "\tb\n", wantErrSub: "non-empty"},
		{name: "empty tool", stdout: "a\t\n", wantErrSub: "non-empty"},
		{name: "nonzero exit", stdout: "not invertible\n", exitCode: 2, wantErrSub: "resolving"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cli := ExecMoxyCLI{Bin: writeFakeMoxyScript(t, c.stdout, c.exitCode)}
			server, tool, err := cli.ResolveToolName(context.Background(), nil, "{server}_{tool}", "x_y")
			if c.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErrSub) {
					t.Fatalf("err = %v, want containing %q", err, c.wantErrSub)
				}
				return
			}
			if err != nil || server != c.wantServer || tool != c.wantTool {
				t.Fatalf("server, tool, err = %q, %q, %v", server, tool, err)
			}
		})
	}
}
