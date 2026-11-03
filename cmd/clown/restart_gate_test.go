package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/alfa/sessions"
)

func recordedPin(pin *sessions.ProfilePin) profileLookup {
	return func(id string) (sessions.ProfilePin, bool, error) {
		if pin == nil || id != restartTestPrincipal {
			return sessions.ProfilePin{}, false, nil
		}
		return *pin, true, nil
	}
}

func testRestartProfileCheck(profileName string, now *sessions.ProfilePin, nowErr error, recorded *sessions.ProfilePin) restartProfileCheck {
	f := parsedFlags{provider: "claude", profile: profileName, forwarded: []string{"--resume", restartTestPrincipal}}
	f.identity.Key = restartTestPrincipal
	return restartProfileCheck{
		relaunch: f,
		pinNow: func(name string) (*sessions.ProfilePin, error) {
			if nowErr != nil {
				return nil, nowErr
			}
			return now, nil
		},
		recorded: recordedPin(recorded),
	}
}

// The relaunch's profile gate, run now: the same pin passes; a changed digest,
// a profile that no longer resolves, or dropping the pinned profile refuses.
func TestRestartProfileCheck(t *testing.T) {
	pin := &sessions.ProfilePin{Name: "worker", Digest: "d1"}
	if err := testRestartProfileCheck("worker", pin, nil, pin).check(); err != nil {
		t.Fatalf("unchanged profile refused: %v", err)
	}
	if err := testRestartProfileCheck("", nil, nil, nil).check(); err != nil {
		t.Fatalf("no profile, no pin refused: %v", err)
	}
	for name, c := range map[string]restartProfileCheck{
		"changed digest":  testRestartProfileCheck("worker", &sessions.ProfilePin{Name: "worker", Digest: "d2"}, nil, pin),
		"unresolvable":    testRestartProfileCheck("worker", nil, errors.New("unknown profile \"worker\""), pin),
		"pinned, dropped": testRestartProfileCheck("", nil, nil, pin),
		"unpinnable":      testRestartProfileCheck("worker", &sessions.ProfilePin{Name: "worker", Digest: sessions.UnpinnableDigest}, nil, &sessions.ProfilePin{Name: "worker", Digest: sessions.UnpinnableDigest}),
	} {
		if err := c.check(); err == nil {
			t.Errorf("%s: the relaunch would refuse, the dry run must too", name)
		}
	}
}

// clown#340: the dry run's refusal carries what the relaunch's would: the
// field diff and the line on the build that pinned.
func TestRestartProfileCheckRefusalCarriesTheDiffAndProvenance(t *testing.T) {
	c := testRestartProfileCheck("worker", &sessions.ProfilePin{Name: "worker", Digest: "d2"}, nil, &sessions.ProfilePin{Name: "worker", Digest: "d1"})
	c.describeChange = func(old, new sessions.ProfilePin) string {
		return "changes:\n  model: a -> b (" + old.Digest + " -> " + new.Digest + ")"
	}
	c.describeProvenance = func(id string) string { return "the pin was written by this same clown build (" + id + ")" }
	err := c.check()
	if err == nil {
		t.Fatal("changed digest must refuse")
	}
	for _, want := range []string{"model: a -> b (d1 -> d2)", "written by this same clown build (" + restartTestPrincipal + ")"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
}

// clown#340: after this binary's gate passes, the clown on PATH (the binary
// the relaunch execs) is asked for its digest of the profile. Agreement
// passes; another digest refuses, naming both builds and the pin; a clown
// without the verb (an older build) or none on PATH leaves this binary's
// verdict; a clown that cannot answer refuses. Nothing is asked without a
// profile, a pin, or for an unpinnable pin.
func TestRestartProfileCheckWithPathClown(t *testing.T) {
	pin := &sessions.ProfilePin{Name: "worker", Digest: "d1"}
	digest := func(d string, supported bool, err error) func(string) (string, bool, error) {
		return func(string) (string, bool, error) { return d, supported, err }
	}
	asked := func() (func(string) (string, bool, error), *[]string) {
		var names []string
		return func(name string) (string, bool, error) { names = append(names, name); return "d1", true, nil }, &names
	}
	for name, c := range map[string]struct {
		check  restartProfileCheck
		digest func(string) (string, bool, error)
		refuse []string
	}{
		"same digest":               {testRestartProfileCheck("worker", pin, nil, pin), digest("d1", true, nil), nil},
		"older clown on PATH":       {testRestartProfileCheck("worker", pin, nil, pin), digest("", false, nil), nil},
		"no clown on PATH":          {testRestartProfileCheck("worker", pin, nil, pin), nil, nil},
		"other digest":              {testRestartProfileCheck("worker", pin, nil, pin), digest("d2", true, nil), []string{"/nix/store/x/bin/clown", `"worker"`, "digest d2", "pinned to digest d1", restartTestPrincipal, "two builds", "CLOWN_PROFILE_DIGEST", "written by"}},
		"cannot answer":             {testRestartProfileCheck("worker", pin, nil, pin), digest("", true, errors.New("exited 1: registry skewed")), []string{"/nix/store/x/bin/clown", "could not resolve", "registry skewed"}},
		"this binary refuses first": {testRestartProfileCheck("worker", &sessions.ProfilePin{Name: "worker", Digest: "d2"}, nil, pin), digest("d1", true, nil), []string{"d2"}},
	} {
		t.Run(name, func(t *testing.T) {
			c.check.pathDigest = c.digest
			c.check.pathClown = "/nix/store/x/bin/clown"
			c.check.describeProvenance = func(string) string { return "the pin was written by clown 0.1" }
			err := c.check.checkWithPathClown()
			if c.refuse == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want a refusal")
			}
			for _, s := range c.refuse {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("refusal %q lacks %q", err, s)
				}
			}
		})
	}
	for name, c := range map[string]restartProfileCheck{
		"no profile":     testRestartProfileCheck("", nil, nil, nil),
		"unpinned":       testRestartProfileCheck("worker", pin, nil, nil),
		"unpinnable pin": testRestartProfileCheck("worker", &sessions.ProfilePin{Name: "worker", Digest: sessions.UnpinnableDigest}, nil, &sessions.ProfilePin{Name: "worker", Digest: sessions.UnpinnableDigest}),
	} {
		t.Run(name+" asks nothing", func(t *testing.T) {
			d, names := asked()
			c.pathDigest = d
			err := c.checkWithPathClown()
			if name == "unpinnable pin" && err == nil {
				t.Fatal("an unpinnable pin is refused by this binary's gate")
			}
			if len(*names) != 0 {
				t.Fatalf("PATH's clown was asked for %q", *names)
			}
		})
	}
}

// fakePathClown is a `clown` whose `profile digest <name>` prints stdout and
// stderr and exits with code; any other verb exits 2 like the real usage
// error. It records the name it was asked and its CLOWN_PROFILE_DIGEST (which
// must be unset: the relaunch drops it).
func fakePathClown(t *testing.T, stdout, stderr, code string) (bin, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	bin = filepath.Join(dir, "clown")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = profile ] && [ \"$2\" = digest ]; then\n" +
		"  printf '%s|%s\\n' \"$3\" \"${CLOWN_PROFILE_DIGEST-unset}\" >> '" + calls + "'\n" +
		"  printf '%s' '" + stdout + "'; printf '%s' '" + stderr + "' >&2; exit " + code + "\n" +
		"fi\n" +
		"echo usage >&2; exit 2\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, calls
}

// pathProfileDigest runs `clown profile digest` on PATH's clown under the
// relaunch's environment: a digest is returned; a usage error is an older
// build (unsupported); exit 1 carries the clown's stderr; anything else
// printed is an error; no binary is unsupported.
func TestPathProfileDigest(t *testing.T) {
	const d = "51e0da89f795a3309b2fc35fb6c4f848d9ac2649c3eedf5111b4402d2e17c074"
	env := []string{"PATH=/bin:/usr/bin", "CLOWN_PROFILE_DIGEST=" + d, "CLOWN_PROFILE_SESSION=x"}
	bin, calls := fakePathClown(t, d+"\n", "", "0")
	got, supported, err := pathProfileDigest(bin, env, time.Minute)("worker")
	if err != nil || !supported || got != d {
		t.Fatalf("digest = %q, %v, %v", got, supported, err)
	}
	if c := readCalls(t, calls); len(c) != 1 || c[0] != "worker|unset" {
		t.Fatalf("calls = %q, want the profile name, with the launcher's approval dropped from the environment", c)
	}

	bin, _ = fakePathClown(t, "", "usage: clown profile <list|add|edit <name>|remove <name>>", "2")
	if _, supported, err := pathProfileDigest(bin, env, time.Minute)("worker"); err != nil || supported {
		t.Errorf("older clown: supported %v, err %v; want unsupported, no error", supported, err)
	}
	bin, _ = fakePathClown(t, "", "clown: profile \"worker\" cannot be pinned: registry skewed", "1")
	if _, supported, err := pathProfileDigest(bin, env, time.Minute)("worker"); err == nil || !supported || !strings.Contains(err.Error(), "registry skewed") || !strings.Contains(err.Error(), "exited 1") {
		t.Errorf("exit 1: supported %v, err %v; want the clown's stderr", supported, err)
	}
	bin, _ = fakePathClown(t, "not a digest\n", "", "0")
	if _, _, err := pathProfileDigest(bin, env, time.Minute)("worker"); err == nil || !strings.Contains(err.Error(), "not a digest") {
		t.Errorf("garbage: err %v", err)
	}
	if _, supported, err := pathProfileDigest("", env, time.Minute)("worker"); err != nil || supported {
		t.Errorf("no clown on PATH: supported %v, err %v", supported, err)
	}
	if _, _, err := pathProfileDigest(filepath.Join(t.TempDir(), "missing"), env, time.Minute)("worker"); err == nil {
		t.Error("a missing binary must be an error")
	}
}

// H1: a relaunch the profile gate would refuse must not stop the provider:
// the session keeps running and the refusal is logged.
func TestRestartRefusedByTheProfileGateLeavesTheProviderRunning(t *testing.T) {
	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	logPath := filepath.Join(t.TempDir(), "l.log")
	stops, countdowns := 0, 0
	check := testRestartProfileCheck("worker", &sessions.ProfilePin{Name: "worker", Digest: "d2"}, nil, &sessions.ProfilePin{Name: "worker", Digest: "d1"})
	fr := frontendRestarter{
		principal:    restartTestPrincipal,
		stop:         func() { stops++ },
		countdown:    func(string, bool) (bool, string) { countdowns++; return true, "" },
		profileCheck: check.check,
		logPath:      logPath,
	}
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", Reason: "r"})
	if stops != 0 || countdowns != 0 || pendingRelaunch.Load() != nil {
		t.Fatalf("stops %d, countdowns %d, plan %v; want the session left running", stops, countdowns, pendingRelaunch.Load())
	}
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "profile") {
		t.Fatalf("the refusal must be logged:\n%s", b)
	}
}

// clown#340: the same, when only the clown on PATH disagrees with the pin.
func TestRestartRefusedByThePathClownLeavesTheProviderRunning(t *testing.T) {
	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	pin := &sessions.ProfilePin{Name: "worker", Digest: "d1"}
	check := testRestartProfileCheck("worker", pin, nil, pin)
	check.pathClown = "/nix/store/new/bin/clown"
	check.pathDigest = func(string) (string, bool, error) { return "d2", true, nil }
	got, record := recordRefusals()
	stops := 0
	fr := frontendRestarter{principal: restartTestPrincipal, profile: "worker", refusal: record, stop: func() { stops++ }, profileCheck: check.checkWithPathClown, logPath: filepath.Join(t.TempDir(), "l.log")}
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", Reason: "r"})
	if stops != 0 || pendingRelaunch.Load() != nil {
		t.Fatalf("stops %d, plan %v; want the session left running", stops, pendingRelaunch.Load())
	}
	if len(*got) != 1 || (*got)[0].Kind != "profile-gate" || !strings.Contains((*got)[0].Text, "/nix/store/new/bin/clown") {
		t.Fatalf("refusals = %+v; want one profile-gate record naming PATH's clown", *got)
	}
}
