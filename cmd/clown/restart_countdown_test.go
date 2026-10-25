package main

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakePosh struct {
	probeCode int
	probeOut  string
	modalCode int
	calls     [][]string
}

func (p *fakePosh) run(argv []string) ([]byte, int, error) {
	p.calls = append(p.calls, argv)
	if slices.Contains(argv, "--probe") {
		return []byte(p.probeOut), p.probeCode, nil
	}
	return nil, p.modalCode, nil
}

type countdownCase struct {
	posh        *fakePosh // nil: posh is not on PATH
	session     string    // POSH_SESSION
	interactive bool
}

func runCountdown(t *testing.T, c countdownCase) (proceed bool, slept time.Duration, notice string, posh *fakePosh) {
	t.Helper()
	var out bytes.Buffer
	cd := restartCountdown{
		poshSession: c.session,
		lookPath: func(name string) (string, error) {
			if c.posh == nil {
				return "", errors.New("not found")
			}
			return "/bin/" + name, nil
		},
		run: func(argv []string) ([]byte, int, error) {
			if c.posh == nil {
				t.Fatalf("ran %q without posh", argv)
			}
			return c.posh.run(argv)
		},
		interactive: c.interactive,
		notify:      &out,
		sleep:       func(d time.Duration) { slept += d },
	}
	proceed, _ = cd.proceed("pick up new tools")
	return proceed, slept, out.String(), c.posh
}

const probeOK = `{"op":"probe","modal":"ok","active":null,"capable":1,"attached":1}`

// A detached session gets no countdown: it restarts at once.
func TestCountdownDetachedRestartsAtOnce(t *testing.T) {
	proceed, slept, notice, _ := runCountdown(t, countdownCase{})
	if !proceed || slept != 0 || notice != "" {
		t.Fatalf("proceed %v, slept %v, notice %q", proceed, slept, notice)
	}
}

// An attached session that is not posh-wrapped gets the inline 30 s notice.
func TestCountdownInlineFallbackForAnAttachedTerminal(t *testing.T) {
	proceed, slept, notice, _ := runCountdown(t, countdownCase{interactive: true})
	if !proceed || slept != 30*time.Second {
		t.Fatalf("proceed %v, slept %v", proceed, slept)
	}
	if !strings.Contains(notice, "restart pending") || !strings.Contains(notice, "30s") || !strings.Contains(notice, "pick up new tools") {
		t.Fatalf("notice = %q", notice)
	}
}

// posh with the modal: its exit code decides, 0 proceeds and 10 cancels; no
// inline countdown on top.
func TestCountdownPoshModal(t *testing.T) {
	posh := &fakePosh{probeOut: probeOK}
	proceed, slept, notice, _ := runCountdown(t, countdownCase{posh: posh, session: "sess", interactive: true})
	if !proceed || slept != 0 || notice != "" {
		t.Fatalf("modal 0: proceed %v, slept %v, notice %q", proceed, slept, notice)
	}
	if len(posh.calls) != 2 {
		t.Fatalf("calls = %q, want probe then modal", posh.calls)
	}
	if want := []string{"/bin/posh", "modal", "--probe", "sess"}; !slices.Equal(posh.calls[0], want) {
		t.Fatalf("probe = %q, want %q", posh.calls[0], want)
	}
	modal := posh.calls[1]
	if modal[1] != "modal" || modal[2] != "sess" || !slices.Contains(modal, "--timeout") || modal[slices.Index(modal, "--timeout")+1] != "30" {
		t.Fatalf("modal = %q", modal)
	}

	cancelled := &fakePosh{probeOut: probeOK, modalCode: 10}
	if proceed, slept, _, _ := runCountdown(t, countdownCase{posh: cancelled, session: "sess", interactive: true}); proceed || slept != 0 {
		t.Fatalf("modal 10: proceed %v, slept %v; want cancelled", proceed, slept)
	}
}

// A posh without the modal (an old daemon, a refused probe), a busy or invalid
// modal, or a session posh is not on PATH for, all fall back to the inline
// countdown on an attached terminal.
func TestCountdownFallsBackWithoutAWorkingModal(t *testing.T) {
	for name, c := range map[string]countdownCase{
		"probe fails":      {posh: &fakePosh{probeCode: 1}, session: "sess", interactive: true},
		"probe not modal":  {posh: &fakePosh{probeOut: `{"op":"probe"}`}, session: "sess", interactive: true},
		"modal busy":       {posh: &fakePosh{probeOut: probeOK, modalCode: 11}, session: "sess", interactive: true},
		"modal invalid":    {posh: &fakePosh{probeOut: probeOK, modalCode: 12}, session: "sess", interactive: true},
		"no posh on PATH":  {session: "sess", interactive: true},
		"not posh-wrapped": {posh: &fakePosh{probeOut: probeOK}, interactive: true},
	} {
		t.Run(name, func(t *testing.T) {
			proceed, slept, notice, posh := runCountdown(t, c)
			if !proceed || slept != 30*time.Second || !strings.Contains(notice, "restart pending") {
				t.Fatalf("proceed %v, slept %v, notice %q", proceed, slept, notice)
			}
			if c.session == "" && posh != nil && len(posh.calls) != 0 {
				t.Fatalf("a session without POSH_SESSION must not call posh: %q", posh.calls)
			}
		})
	}
}

// A cancelled countdown leaves the provider running and records no relaunch.
func TestFrontendRestarterHonoursACancelledCountdown(t *testing.T) {
	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	stops := 0
	fr := frontendRestarter{
		principal: restartTestPrincipal,
		stop:      func() { stops++ },
		countdown: func(string) (bool, string) { return false, "cancelled" },
		logPath:   t.TempDir() + "/l.log",
	}
	fr.restart(restartRequest{When: "idle", Reason: "r"}, preflightVerdict{Verdict: "restart"})
	if stops != 0 || pendingRelaunch.Load() != nil {
		t.Fatalf("a cancelled countdown stopped the provider (%d) or recorded a plan", stops)
	}
}
