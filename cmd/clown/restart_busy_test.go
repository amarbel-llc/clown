package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// H2: a UserPromptSubmit hook marks the session busy without running the
// idle work; the next Stop poke marks it idle again and runs it.
func TestIdleListenerTracksTurnStarts(t *testing.T) {
	sock := filepath.Join(shortSocketDir(t), "k.sock")
	var busy atomic.Bool
	var runs atomic.Int32
	l, err := startIdleListenerTracking(sock, &busy, func() { runs.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if code := runIdleNotify([]string{"--busy", sock}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	waitFor(t, "the busy mark", busy.Load)
	pokeIdle(t, sock)
	waitFor(t, "the idle run", func() bool { return runs.Load() >= 1 })
	if busy.Load() {
		t.Fatal("a Stop poke must clear the busy mark")
	}
	// Closing ends the reader and lets the run goroutine drain every queued
	// wake: had the turn start woken the idle work too, a second run would
	// have been queued behind the first.
	l.Close()
	<-l.done
	if n := runs.Load(); n != 1 {
		t.Fatalf("runs = %d; a turn start must not run the idle work", n)
	}
}

// A turn start alone, then close: the run goroutine ends with no run.
func TestATurnStartAloneRunsNothing(t *testing.T) {
	sock := filepath.Join(shortSocketDir(t), "k.sock")
	var busy atomic.Bool
	var runs atomic.Int32
	l, err := startIdleListenerTracking(sock, &busy, func() { runs.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	runIdleNotify([]string{"--busy", sock})
	waitFor(t, "the busy mark", busy.Load)
	l.Close()
	<-l.done
	if runs.Load() != 0 {
		t.Fatal("a turn start ran the idle work")
	}
}

func TestSynthIdlePluginDirRegistersATurnStartHook(t *testing.T) {
	dir, err := synthIdlePluginDir(testStagingRoot(t), "/nix/store/c/bin/clown", "/run/user/1001/clown/idle/k.sock")
	if err != nil {
		t.Fatal(err)
	}
	hb, err := os.ReadFile(filepath.Join(dir, "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(hb, &cfg); err != nil {
		t.Fatal(err)
	}
	ups := cfg.Hooks["UserPromptSubmit"]
	if len(ups) != 1 || len(ups[0].Hooks) != 1 || ups[0].Hooks[0].Command != "/nix/store/c/bin/clown idle-notify --busy /run/user/1001/clown/idle/k.sock" {
		t.Fatalf("UserPromptSubmit hook = %+v\n%s", ups, hb)
	}
}

// A turn that starts during the inline countdown cancels the restart at once.
func TestCountdownInlineAbortsWhenATurnStarts(t *testing.T) {
	calls := 0
	var slept time.Duration
	cd := restartCountdown{
		lookPath:    func(string) (string, error) { return "", os.ErrNotExist },
		interactive: true,
		notify:      &discardWriter{},
		sleep:       func(d time.Duration) { slept += d },
		busy:        func() bool { calls++; return calls > 4 },
	}
	proceed, _ := cd.proceed("r", true)
	if proceed || slept >= 30*time.Second {
		t.Fatalf("proceed %v after %v; want a cancel as soon as the turn started", proceed, slept)
	}
}

// A modal that proceeds while a turn started is a cancel too.
func TestCountdownModalProceedDuringATurnCancels(t *testing.T) {
	posh := &fakePosh{probeOut: probeOK}
	cd := restartCountdown{
		poshSession: "sess",
		lookPath:    func(name string) (string, error) { return "/bin/" + name, nil },
		run:         posh.run,
		interactive: true,
		notify:      &discardWriter{},
		sleep:       func(time.Duration) {},
		busy:        func() bool { return true },
	}
	if proceed, _ := cd.proceed("r", true); proceed {
		t.Fatal("must not restart into a running turn")
	}
}

// L1: a modal that was raised but ended without an answer (posh killed at the
// bound, any exit but 10, 11, 12) proceeds, without a second 30 s wait.
func TestCountdownModalWithoutAnAnswerProceedsWithoutASecondWait(t *testing.T) {
	for _, code := range []int{-1, 1, 2} {
		posh := &fakePosh{probeOut: probeOK, modalCode: code}
		var slept time.Duration
		cd := restartCountdown{
			poshSession: "sess",
			lookPath:    func(name string) (string, error) { return "/bin/" + name, nil },
			run:         posh.run,
			interactive: true,
			notify:      &discardWriter{},
			sleep:       func(d time.Duration) { slept += d },
		}
		if proceed, _ := cd.proceed("r", true); !proceed || slept != 0 {
			t.Errorf("modal exit %d: proceed %v, slept %v; want proceed, no inline wait", code, proceed, slept)
		}
	}
}

// The restarter re-checks after the countdown: a turn that started is not
// SIGTERMed.
func TestRestarterSkipsTheStopWhenATurnStartedDuringTheCountdown(t *testing.T) {
	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	stops := 0
	fr := frontendRestarter{
		principal: restartTestPrincipal,
		stop:      func() { stops++ },
		countdown: func(string, bool) (bool, string) { return true, "elapsed" },
		busy:      func() bool { return true },
		logPath:   filepath.Join(t.TempDir(), "l.log"),
	}
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", Reason: "r"})
	if stops != 0 || pendingRelaunch.Load() != nil {
		t.Fatalf("stops %d; a busy session must not be stopped", stops)
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
