package main

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/alfa/sessionstatus"
)

// clown#342: a restart may carry a permission posture for the relaunch. The
// verdict's posture is applied; the request's is only the hand-off's argument.

func TestParsePreflightVerdictPosture(t *testing.T) {
	for _, p := range []string{"", postureUnattended, postureAttended} {
		v, err := parsePreflightVerdict([]byte(`{"verdict":"restart","reason":"r","when":"idle","posture":"` + p + `"}` + "\n"))
		if err != nil || v.Posture != p {
			t.Errorf("posture %q: %+v, %v", p, v, err)
		}
	}
	if v, err := parsePreflightVerdict([]byte(`{"verdict":"restart"}`)); err != nil || v.Posture != postureKeep {
		t.Errorf("absent posture = %+v, %v; want keep", v, err)
	}
	if v, err := parsePreflightVerdict([]byte(`{"verdict":"restart","posture":"keep"}`)); err != nil || v.Posture != postureKeep {
		t.Errorf("explicit keep = %+v, %v; want the empty posture", v, err)
	}
	if _, err := parsePreflightVerdict([]byte(`{"verdict":"restart","posture":"auto"}`)); err == nil || !strings.Contains(err.Error(), "posture") {
		t.Errorf("unknown posture accepted: %v", err)
	}
}

// Each posture yields exactly zero or one marker, whatever the launch had.
func TestRestartEnvPosture(t *testing.T) {
	marker := unattendedEnv + "=1"
	with := []string{"PATH=/bin", marker}
	without := []string{"PATH=/bin"}
	for name, c := range map[string]struct {
		env     []string
		posture string
		want    int
	}{
		"keep, launched unattended":       {with, postureKeep, 1},
		"keep, launched attended":         {without, postureKeep, 0},
		"unattended, launched unattended": {with, postureUnattended, 1},
		"unattended, launched attended":   {without, postureUnattended, 1},
		"attended, launched unattended":   {with, postureAttended, 0},
		"attended, launched attended":     {without, postureAttended, 0},
	} {
		env := restartEnv(c.env, &relaunchPlan{principal: restartTestPrincipal, reason: "r", posture: c.posture})
		if n := countOf(env, marker); n != c.want || !slices.Contains(env, "PATH=/bin") {
			t.Errorf("%s: %d markers in %q, want %d", name, n, env, c.want)
		}
	}
}

func countOf(ss []string, s string) int {
	n := 0
	for _, x := range ss {
		if x == s {
			n++
		}
	}
	return n
}

// unattended ends with exactly one dontAsk flag and no other mode; attended
// drops the dontAsk flags in every spelling and nothing else; keep is
// restartArgv. Operands after `--` are untouched; the [attach] carrier follows
// the posture.
func TestRestartArgvPosture(t *testing.T) {
	t.Setenv(unattendedEnv, "")
	f := parsedFlags{provider: "claude", profile: "worker", forwarded: []string{"--permission-mode", "auto", "--model", "m", "--", "op"}}
	f.identity.Key = restartTestPrincipal

	got := restartArgvPosture(f, "", postureUnattended, "")
	want := []string{"--provider", "claude", "--profile", "worker", "--", "--model", "m", "--permission-mode", "dontAsk", "--resume", restartTestPrincipal, "--", "op"}
	if !slices.Equal(got, want) {
		t.Errorf("unattended argv = %q, want %q", got, want)
	}
	f.forwarded = []string{"--permission-mode", "dontAsk", "--model", "m"}
	if got := restartArgvPosture(f, "", postureUnattended, ""); countOf(got, "dontAsk") != 1 {
		t.Errorf("unattended over a spliced launch: %q, want exactly one dontAsk", got)
	}

	f.forwarded = []string{"--permission-mode=dontAsk", "--inherit-permission-mode", "dontAsk", "--permission-mode", "auto", "--model", "m", "--", "--permission-mode", "dontAsk"}
	got = restartArgvPosture(f, "", postureAttended, "")
	want = []string{"--provider", "claude", "--profile", "worker", "--", "--permission-mode", "auto", "--model", "m", "--resume", restartTestPrincipal, "--", "--permission-mode", "dontAsk"}
	if !slices.Equal(got, want) {
		t.Errorf("attended argv = %q, want %q", got, want)
	}
	if keep := restartArgvPosture(f, "", postureKeep, ""); !slices.Equal(keep, restartArgv(f, "")) {
		t.Errorf("keep = %q, want restartArgv's %q", keep, restartArgv(f, ""))
	}

	f.forwarded = nil
	if got := restartArgvPosture(f, restartTestPrincipal, postureUnattended, ""); !slices.Contains(got, unattendedCarrierFlag) {
		t.Errorf("attached unattended relaunch lacks the carrier: %q", got)
	}
	t.Setenv(unattendedEnv, "1")
	if got := restartArgvPosture(f, restartTestPrincipal, postureAttended, ""); slices.Contains(got, unattendedCarrierFlag) {
		t.Errorf("attached attended relaunch carries the posture: %q", got)
	}
	if got := restartArgvPosture(f, restartTestPrincipal, postureKeep, ""); !slices.Contains(got, unattendedCarrierFlag) {
		t.Errorf("attached keep relaunch of an unattended launch lacks the carrier: %q", got)
	}
}

// spinclass#476: an interactive `claude --resume` with no mode flag runs in
// the transcript's last mode, so an attended claude relaunch whose launch was
// in dontAsk must name a mode: the verdict's, or default from an older
// spinclass that sends none. A mode the launch kept is the one it runs in,
// and keep, unattended and other providers are not given one.
func TestRestartArgvAttendedNamesAMode(t *testing.T) {
	t.Setenv(unattendedEnv, "")
	f := parsedFlags{provider: "claude", forwarded: []string{"--permission-mode", "dontAsk", "--model", "m", "--", "op"}}
	f.identity.Key = restartTestPrincipal

	got := restartArgvPosture(f, "", postureAttended, "")
	want := []string{"--provider", "claude", "--", "--model", "m", "--permission-mode", "default", "--resume", restartTestPrincipal, "--", "op"}
	if !slices.Equal(got, want) {
		t.Errorf("attended over a dontAsk launch, no verdict mode = %q, want %q", got, want)
	}
	for _, mode := range []string{"auto", "acceptEdits", "default"} {
		got := restartArgvPosture(f, "", postureAttended, mode)
		want := []string{"--provider", "claude", "--", "--model", "m", "--permission-mode", mode, "--resume", restartTestPrincipal, "--", "op"}
		if !slices.Equal(got, want) {
			t.Errorf("attended over a dontAsk launch, verdict mode %s = %q, want %q", mode, got, want)
		}
	}
	f.forwarded = []string{"--model", "m"}
	if got := restartArgvPosture(f, "", postureAttended, ""); countOf(got, "default") != 1 || countOf(got, "--permission-mode") != 1 {
		t.Errorf("attended over a launch with no mode = %q, want exactly one --permission-mode default", got)
	}
	f.forwarded = []string{"--permission-mode", "acceptEdits", "--model", "m"}
	if got := restartArgvPosture(f, "", postureAttended, "auto"); countOf(got, "--permission-mode") != 1 || !slices.Contains(got, "acceptEdits") {
		t.Errorf("attended over an acceptEdits launch, verdict auto = %q, want acceptEdits kept and no other mode", got)
	}
	f.forwarded = []string{"--model", "m"}
	if got := restartArgvPosture(f, "", postureKeep, "auto"); slices.Contains(got, "--permission-mode") {
		t.Errorf("keep = %q, want no mode added", got)
	}
	if got := restartArgvPosture(f, "", postureUnattended, "auto"); slices.Contains(got, "auto") || countOf(got, "dontAsk") != 1 {
		t.Errorf("unattended with a verdict mode = %q, want dontAsk only", got)
	}
	f.provider = "codex"
	if got := restartArgvPosture(f, "", postureAttended, "auto"); slices.Contains(got, "--permission-mode") {
		t.Errorf("attended codex relaunch = %q, want no claude mode flag", got)
	}
}

// The verdict's optional mode parses through; an older spinclass's verdict
// without one leaves it empty.
func TestParsePreflightVerdictMode(t *testing.T) {
	v, err := parsePreflightVerdict([]byte(`{"verdict":"restart","posture":"attended","mode":"auto"}`))
	if err != nil || v.Mode != "auto" || v.Posture != postureAttended {
		t.Errorf("mode auto: %+v, %v", v, err)
	}
	if v, err := parsePreflightVerdict([]byte(`{"verdict":"restart","posture":"attended"}`)); err != nil || v.Mode != "" {
		t.Errorf("no mode: %+v, %v", v, err)
	}
}

// A verdict naming a mode no attended start gets is dropped before the stop
// and recorded as a posture refusal; the provider keeps running.
func TestRestartRefusedByAnUnknownModeLeavesTheProviderRunning(t *testing.T) {
	for _, mode := range []string{"dontAsk", "bypassPermissions", "plan", "bogus"} {
		pendingRelaunch.Store(nil)
		stops := 0
		var recorded []sessionstatus.RestartRefusal
		fr := frontendRestarter{
			principal: restartTestPrincipal, argv: []string{"--provider", "claude"},
			stop:        func() { stops++ },
			refusal:     func(r sessionstatus.RestartRefusal) { recorded = append(recorded, r) },
			postureArgv: func(p, m string) []string { return []string{"--provider", "claude", "--mode-applied", m} },
			logPath:     filepath.Join(t.TempDir(), "l.log"),
		}
		providerRunning.Store(true)
		fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", Posture: postureAttended, Mode: mode})
		providerRunning.Store(false)
		if stops != 0 || pendingRelaunch.Load() != nil {
			t.Fatalf("mode %s: a refused mode must stop nothing", mode)
		}
		if len(recorded) != 1 || recorded[0].Kind != sessionstatus.RefusalPosture || !strings.Contains(recorded[0].Text, mode) {
			t.Fatalf("mode %s: recorded %+v; want one posture refusal naming the mode", mode, recorded)
		}
	}
	pendingRelaunch.Store(nil)
}

// unattended is refused for what the marker cannot put in dontAsk; a forwarded
// non-dontAsk mode is rewritten away, so it does not refuse. attended and keep
// are never refused here.
func TestRelaunchPostureError(t *testing.T) {
	ok := parsedFlags{provider: "claude", forwarded: []string{"--permission-mode", "auto", "--model", "m"}}
	if err := relaunchPostureError(ok, postureUnattended); err != nil {
		t.Errorf("claude with a forwarded auto mode: %v", err)
	}
	for name, f := range map[string]parsedFlags{
		"codex":    {provider: "codex"},
		"naked":    {provider: "claude", naked: true},
		"settings": {provider: "claude", forwarded: []string{"--settings", "s.json"}},
		"skip":     {provider: "claude", forwarded: []string{"--dangerously-skip-permissions"}},
	} {
		if err := relaunchPostureError(f, postureUnattended); err == nil || !strings.Contains(err.Error(), unattendedEnv) {
			t.Errorf("%s: err = %v, want the marker's refusal", name, err)
		}
		if err := relaunchPostureError(f, postureAttended); err != nil {
			t.Errorf("%s attended: %v", name, err)
		}
		if err := relaunchPostureError(f, postureKeep); err != nil {
			t.Errorf("%s keep: %v", name, err)
		}
	}
}

// The verb passes --posture through and the frontend queues it; a bad value
// is a usage error.
func TestRestartVerbPosture(t *testing.T) {
	runtime := shortSocketDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	q := &restartQueue{}
	var got controlMessage
	startTestControl(t, runtime, func(m controlMessage) controlReply {
		got = m
		return restartControlHandler("claude", "", q)(m)
	})
	code, stdout, stderr := runRestartVerbForTest(t, "--session", restartTestPrincipal, "--posture", postureAttended)
	if code != 0 || got.Posture != postureAttended || !strings.Contains(stdout, "posture attended") {
		t.Fatalf("code %d, message %+v, stdout %q, stderr %q", code, got, stdout, stderr)
	}
	if req, ok := q.take(); !ok || req.Posture != postureAttended {
		t.Fatalf("queued %+v, %v; want the posture on the request", req, ok)
	}
	if code, _, stderr := runRestartVerbForTest(t, "--session", restartTestPrincipal, "--posture", "auto"); code != 2 || !strings.Contains(stderr, "posture") {
		t.Errorf("bad posture: code %d, stderr %q; want a usage error", code, stderr)
	}
	// "keep" and the empty string both spell the empty posture: a hand-off
	// that cannot drop the flag pair passes one of them.
	for _, spelled := range []string{"keep", ""} {
		code, stdout, stderr := runRestartVerbForTest(t, "--session", restartTestPrincipal, "--posture", spelled)
		if code != 0 || got.Posture != postureKeep || strings.Contains(stdout, "posture") {
			t.Errorf("--posture %q: code %d, message %+v, stdout %q, stderr %q; want keep", spelled, code, got, stdout, stderr)
		}
		if req, ok := q.take(); !ok || req.Posture != postureKeep {
			t.Errorf("--posture %q queued %+v, %v", spelled, req, ok)
		}
	}
}

// A request the posture gate refuses is not queued; attended and keep are not
// gated at request time.
func TestRestartVerbRefusedByThePostureGateQueuesNothing(t *testing.T) {
	runtime := shortSocketDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	q := &restartQueue{}
	var asked []string
	trig := &restartTriggers{posture: func(p string) error {
		asked = append(asked, p)
		if p == postureUnattended {
			return errors.New(unattendedEnv + "=1 puts claude in dontAsk, but the provider args pass --settings, which could set another mode; refusing")
		}
		return nil
	}}
	startTestControl(t, runtime, restartControlHandlerTriggers("claude", "", q, trig))

	code, _, stderr := runRestartVerbForTest(t, "--session", restartTestPrincipal, "--posture", postureUnattended)
	if code == 0 || !strings.Contains(stderr, "posture unattended") || !strings.Contains(stderr, "--settings") {
		t.Fatalf("code %d, stderr %q; want the posture refusal", code, stderr)
	}
	if _, ok := q.take(); ok {
		t.Fatal("a refused request must not be queued")
	}
	if code, _, stderr := runRestartVerbForTest(t, "--session", restartTestPrincipal, "--posture", postureAttended); code != 0 {
		t.Fatalf("attended: code %d, %q", code, stderr)
	}
	if req, ok := q.take(); !ok || req.Posture != postureAttended {
		t.Fatalf("attended queued %+v, %v", req, ok)
	}
	if code, _, stderr := runRestartVerbForTest(t, "--session", restartTestPrincipal); code != 0 {
		t.Fatalf("keep: code %d, %q", code, stderr)
	}
	if !slices.Equal(asked, []string{postureUnattended, postureAttended}) {
		t.Errorf("gate asked for %q; want unattended and attended, never keep", asked)
	}
}

// The plan carries the verdict's posture and argv, never the request's, and
// the notice names it; a verdict without one keeps everything.
func TestFrontendRestarterAppliesTheVerdictsPosture(t *testing.T) {
	pendingRelaunch.Store(nil)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	graceDone := make(chan struct{}, 3)
	t.Cleanup(func() { <-graceDone; <-graceDone; <-graceDone })
	fr := frontendRestarter{
		principal: restartTestPrincipal, argv: []string{"--provider", "claude"},
		postureArgv: func(p, m string) []string { return []string{"--provider", "claude", "--posture-applied", p, m} },
		stop:        func() {}, logPath: filepath.Join(t.TempDir(), "l.log"), stopGrace: time.Millisecond,
		kill:         func() error { return nil },
		graceChecked: func(bool) { graceDone <- struct{}{} },
	}
	providerRunning.Store(true)
	fr.restart(restartRequest{When: "idle", Posture: postureAttended}, preflightVerdict{Verdict: "restart", Reason: "r", Posture: postureUnattended})
	p := pendingRelaunch.Load()
	if p == nil || p.posture != postureUnattended || !slices.Contains(p.argv, "--posture-applied") || !strings.Contains(p.reason, "posture unattended") {
		t.Fatalf("plan = %+v; want the verdict's posture applied", p)
	}

	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", Reason: "r", Posture: postureAttended, Mode: "auto"})
	if p := pendingRelaunch.Load(); p == nil || p.posture != postureAttended || !slices.Contains(p.argv, "auto") {
		t.Fatalf("plan = %+v; want the verdict's mode passed to the argv", p)
	}

	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	fr.restart(restartRequest{When: "idle", Posture: postureAttended}, preflightVerdict{Verdict: "restart", Reason: "r"})
	if p := pendingRelaunch.Load(); p == nil || p.posture != postureKeep || !slices.Equal(p.argv, fr.argv) || strings.Contains(p.reason, "posture") {
		t.Fatalf("plan = %+v; want the launch's posture kept", p)
	}
}

// A relaunch that could not take the verdict's posture is dropped before the
// stop, recorded with its own kind; the provider keeps running.
func TestRestartRefusedByThePostureCheckLeavesTheProviderRunning(t *testing.T) {
	pendingRelaunch.Store(nil)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	stops := 0
	var recorded []sessionstatus.RestartRefusal
	fr := frontendRestarter{
		principal: restartTestPrincipal, argv: []string{"--provider", "claude"},
		stop:    func() { stops++ },
		refusal: func(r sessionstatus.RestartRefusal) { recorded = append(recorded, r) },
		postureCheck: func(p string) error {
			if p == postureUnattended {
				return errors.New("the provider args pass --settings")
			}
			return nil
		},
		logPath: filepath.Join(t.TempDir(), "l.log"),
	}
	providerRunning.Store(true)
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", Posture: postureUnattended})
	if stops != 0 || pendingRelaunch.Load() != nil {
		t.Fatal("a refused posture must stop nothing")
	}
	if len(recorded) != 1 || recorded[0].Kind != sessionstatus.RefusalPosture || !strings.Contains(recorded[0].Text, "--settings") {
		t.Fatalf("recorded %+v; want one posture refusal naming the cause", recorded)
	}
}
