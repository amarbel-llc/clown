package jugglerrun

import (
	"context"
	"encoding/json"
	"os"
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
	rec, err := spawnForNarrows(h, testTask)
	if err != nil {
		t.Fatal(err)
	}

	moxy := h.fakes.Calls(t, "moxy")
	if len(moxy) != 2 || strings.Join(moxy[0].Argv, " ") != "show-effective-moxyfile --dir /spawner/cwd" ||
		moxy[0].Env["MOXIN_PATH"] != "/spawner/moxins" || moxy[1].Argv[0] != "narrows" {
		t.Fatalf("moxy calls = %+v", moxy)
	}
	if got := strings.Join(moxy[1].Argv, " "); got != "narrows --parent "+rec.ParentMoxyfile+" --child "+h.deps.Store.stagedChildMoxyfilePath("rec-42", rec.BriefDigest) {
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
		refusals[0].Violations[0].Key != "allow-tools" {
		t.Errorf("ledger = %s", data)
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
