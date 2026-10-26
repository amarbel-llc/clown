package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/profile"
	"code.linenisgreat.com/clown/internal/promptwalk"
)

// withAttach sets the package-level [attach] state for one test.
func withAttach(t *testing.T, id string, carried bool) {
	t.Helper()
	prevID, prevCarried := attachedID, unattendedCarried
	attachedID, unattendedCarried = id, carried
	t.Cleanup(func() { attachedID, unattendedCarried = prevID, prevCarried })
}

// The [attach] wrap hands the multiplexer clown's environment, and a
// multiplexer whose server already runs may give the inner clown its own
// instead. The outer therefore carries the posture in the inner's argv, as it
// carries the attach id, and the inner applies it even when its environment
// lost the marker (clown#323).
func TestAttachCarriesTheUnattendedPosture(t *testing.T) {
	withAttach(t, "", false)
	t.Setenv(unattendedEnv, "1")
	entry := attachEntry("/bin/clown", "k1", parsedFlags{provider: "claude"})
	if !slices.Contains(entry, unattendedCarrierFlag) {
		t.Fatalf("attach entry %q carries no %s", entry, unattendedCarrierFlag)
	}

	// The inner, its environment stripped by the multiplexer.
	t.Setenv(unattendedEnv, "")
	id, rest := extractAttachID(entry[1:])
	carried, rest := extractUnattendedCarrier(rest)
	if err := unattendedCarrierError(carried, id); err != nil {
		t.Fatalf("inner with its attach id: %v", err)
	}
	withAttach(t, id, carried)
	if slices.Contains(rest, unattendedCarrierFlag) {
		t.Fatalf("carrier left in the inner's args %q", rest)
	}
	inner, err := parseFlags(rest)
	if err != nil {
		t.Fatal(err)
	}
	if err := unattendedGate(&inner); err != nil {
		t.Fatalf("inner gate: %v", err)
	}
	if mode, _ := permissionsDefaultMode(t, claudeSafetySettingsJSON("", "")); mode != "dontAsk" {
		t.Fatalf("inner with the carrier and no marker: defaultMode %q; want dontAsk", mode)
	}
	// Its own restart relaunches through the same wrap state, so it carries
	// the posture again; a restart of an unwrapped launch does not need to.
	if argv := restartArgv(parsedFlags{provider: "claude", identity: sessionIdentity{Key: "k1"}}, id); !slices.Contains(argv, unattendedCarrierFlag) {
		t.Fatalf("restart of a carried inner: argv %q has no carrier", argv)
	}
}

// Without the marker on the outer, nothing is carried and the inner runs
// attended.
func TestAttachWithoutTheMarkerCarriesNothing(t *testing.T) {
	withAttach(t, "", false)
	t.Setenv(unattendedEnv, "")
	entry := attachEntry("/bin/clown", "k1", parsedFlags{provider: "claude"})
	if slices.Contains(entry, unattendedCarrierFlag) {
		t.Fatalf("attach entry %q carries the posture without the marker", entry)
	}
	id, rest := extractAttachID(entry[1:])
	carried, _ := extractUnattendedCarrier(rest)
	withAttach(t, id, carried)
	if _, present := permissionsDefaultMode(t, claudeSafetySettingsJSON("", "")); present {
		t.Fatal("inner without carrier or marker: permissions set; want attended")
	}
	if argv := restartArgv(parsedFlags{provider: "claude", identity: sessionIdentity{Key: "k1"}}, ""); slices.Contains(argv, unattendedCarrierFlag) {
		t.Fatalf("unwrapped restart argv %q carries the posture", argv)
	}
}

// The carrier is not a way to ask for the posture from the command line:
// without the attach id that only the wrap passes, it is refused.
func TestUnattendedCarrierNeedsTheAttachID(t *testing.T) {
	if err := unattendedCarrierError(true, ""); err == nil || !strings.Contains(err.Error(), unattendedCarrierFlag) {
		t.Fatalf("carrier without an attach id: err %v; want a refusal naming %s", err, unattendedCarrierFlag)
	}
	for _, args := range [][]string{{unattendedCarrierFlag}, {"-v", unattendedCarrierFlag, "--", "x"}} {
		carried, rest := extractUnattendedCarrier(args)
		if !carried || slices.Contains(rest[:slices.Index(append(rest, "--"), "--")], unattendedCarrierFlag) {
			t.Errorf("extractUnattendedCarrier(%q) = %v, %q", args, carried, rest)
		}
	}
	// After `--` it is the provider's, untouched.
	if carried, rest := extractUnattendedCarrier([]string{"--", unattendedCarrierFlag}); carried || !slices.Equal(rest, []string{"--", unattendedCarrierFlag}) {
		t.Errorf("after --: %v, %q", carried, rest)
	}
	// run() refuses it before anything else.
	withAttach(t, "", false)
	if code := run([]string{unattendedCarrierFlag, "--version"}); code == 0 {
		t.Fatal("run with a bare carrier succeeded; want a refusal")
	}
}

// The marker is environment only (the 2026-10-26 ruling): a clownfile
// [profile].env or a named profile's env table naming it is refused, and the
// ambient value is left alone.
func TestEnvTablesMayNotSetTheUnattendedMarker(t *testing.T) {
	for _, ambient := range []string{"", "1"} {
		t.Setenv(unattendedEnv, ambient)
		f := parsedFlags{provider: "claude"}
		err := applyNamedProfile(&f, profile.Profile{Name: "p", Provider: "claude", Backend: "anthropic", Env: map[string]string{unattendedEnv: "1"}})
		if err == nil || !strings.Contains(err.Error(), unattendedEnv) {
			t.Errorf("ambient %q, profile env naming the marker: err %v; want a refusal", ambient, err)
		}
		if got := os.Getenv(unattendedEnv); got != ambient {
			t.Errorf("ambient %q: marker now %q; the refusal must not touch it", ambient, got)
		}
		if err := unattendedEnvTableError("clownfile [profile].env", map[string]string{unattendedEnv: "1", "OTHER": "x"}); err == nil {
			t.Errorf("ambient %q: clownfile env naming the marker allowed", ambient)
		}
	}
	if err := unattendedEnvTableError("clownfile [profile].env", map[string]string{"OTHER": "x"}); err != nil {
		t.Errorf("an env table without the marker: %v", err)
	}
}

// runWithFlags checks the clownfile table before applying it: the marker it
// names never reaches the environment, and the launch is refused.
func TestRunWithFlagsRefusesAClownfileThatSetsTheMarker(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(unattendedEnv, "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "clownfile"), []byte("[profile]\nenv = { CLOWN_UNATTENDED = \"1\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	flags := parsedFlags{provider: "claude", providerExplicit: true, printLaunchPlan: true, noAttach: true}
	if code := runWithFlags(flags); code != 1 {
		t.Fatalf("runWithFlags = %d; want 1", code)
	}
	if got := os.Getenv(unattendedEnv); got != "" {
		t.Fatalf("the clownfile set the marker to %q", got)
	}
}

// The unattended gate runs before the confinement gate, so a launch the
// posture refuses writes no confinement record.
func TestUnattendedGateRunsBeforeTheConfinementGate(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv(unattendedEnv, "yes")
	var order []string
	origU, origC := unattendedGate, confinementGate
	unattendedGate = func(f *parsedFlags) error { order = append(order, "unattended"); return origU(f) }
	confinementGate = func(f *parsedFlags, w io.Writer) error { order = append(order, "confinement"); return origC(f, w) }
	t.Cleanup(func() { unattendedGate, confinementGate = origU, origC })
	flags := parsedFlags{provider: "claude", providerExplicit: true, printLaunchPlan: true, noAttach: true}
	if code := runWithFlags(flags); code != 1 || !slices.Equal(order, []string{"unattended"}) {
		t.Fatalf("runWithFlags = %d, gates %q; want only the unattended gate, refusing", code, order)
	}
}

// Both claude launch paths (runClaude, runClownbox) build their args with
// claudeLaunchArgs, which carries the posture into --settings; without the
// marker the settings bytes are exactly what they were before #323.
func TestClaudeLaunchArgsCarryThePosture(t *testing.T) {
	withAttach(t, "", false)
	flags := parsedFlags{provider: "claude", identity: sessionIdentity{Key: "k"}}
	t.Setenv(unattendedEnv, "")
	attended := claudeLaunchArgs("/bin/claude", "", flags, []string{"--model", "m"}, promptwalk.PromptResult{}, nil).SettingsJSON
	if attended != claudeSafetySettingsJSON("k", "m") || strings.Contains(attended, "permissions") {
		t.Fatalf("attended settings %s changed", attended)
	}
	t.Setenv(unattendedEnv, "1")
	got := claudeLaunchArgs("/bin/claude", "", flags, []string{"--model", "m"}, promptwalk.PromptResult{}, nil).SettingsJSON
	if mode, _ := permissionsDefaultMode(t, got); mode != "dontAsk" {
		t.Fatalf("unattended settings %s: defaultMode %q; want dontAsk", got, mode)
	}
}

// launchEnviron is captured from the process environment at start, so a
// clown started with the marker relaunches with it. Checked in a child
// process, the only way to set the environment before package init.
func TestLaunchEnvironCapturesTheMarker(t *testing.T) {
	if os.Getenv("CLOWN_TEST_LAUNCHENV_CHILD") == "1" {
		if !slices.Contains(restartEnv(launchEnviron, &relaunchPlan{principal: "p", reason: "r"}), unattendedEnv+"=1") {
			os.Exit(3)
		}
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLaunchEnvironCapturesTheMarker$")
	cmd.Env = append(os.Environ(), "CLOWN_TEST_LAUNCHENV_CHILD=1", unattendedEnv+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}
