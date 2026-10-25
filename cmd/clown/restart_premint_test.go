package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// spinclass#419: the relaunch marker is CLOWN_RESTARTED_REASON's presence,
// whatever its value; it is consumed.
func TestConsumeRestartedNoticeReportsTheRelaunch(t *testing.T) {
	t.Setenv(restartedReasonEnv, "")
	notice, relaunched := consumeRestartedNotice()
	if !relaunched || notice == "" {
		t.Fatalf("set but empty: notice %q, relaunched %v; want a notice and true", notice, relaunched)
	}
	if _, ok := os.LookupEnv(restartedReasonEnv); ok {
		t.Fatal("CLOWN_RESTARTED_REASON was not consumed")
	}
	if notice, relaunched := consumeRestartedNotice(); relaunched || notice != "" {
		t.Fatalf("unset: notice %q, relaunched %v; want neither", notice, relaunched)
	}
}

// The order of a launch's first identity side effects, and the gate:
//   - a normal launch never peeks, then registers presence, then mints;
//   - a relaunch under --print-launch-plan does not peek either;
//   - a relaunch peeks first; closed: neither presence nor mint runs and the
//     launch stops; live: peek, presence, mint, in that order.
func TestIdentityStepsOrderAndGate(t *testing.T) {
	for _, c := range []struct {
		name             string
		relaunched, plan bool
		closed           bool
		want             []string
		goesOn           bool
	}{
		{"normal launch", false, false, false, []string{"presence", "mint"}, true},
		{"normal launch, closed would not matter", false, false, true, []string{"presence", "mint"}, true},
		{"relaunch, print-launch-plan", true, true, true, []string{"presence", "mint"}, true},
		{"relaunch, live", true, false, false, []string{"peek", "presence", "mint"}, true},
		{"relaunch, closed", true, false, true, []string{"peek"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var seq []string
			s := identitySteps(c.relaunched, c.plan,
				func() bool { seq = append(seq, "peek"); return c.closed },
				func() { seq = append(seq, "presence") },
				func() { seq = append(seq, "mint") },
			)
			if goesOn := s.run(); goesOn != c.goesOn {
				t.Fatalf("run() = %v, want %v", goesOn, c.goesOn)
			}
			if !slices.Equal(seq, c.want) {
				t.Fatalf("sequence %v, want %v", seq, c.want)
			}
		})
	}
}

// spinclass#419: the relaunch's peek. Closed: closed() true (so nothing is
// registered, minted or started). Live or unknown (a spinclass without
// --peek, garbage, none on PATH): false. Each outcome is logged.
func TestPreMintPeek(t *testing.T) {
	for _, c := range []struct {
		name, body string
		noBin      bool
		closed     bool
		log        string
	}{
		{"closed", `echo '{"verdict":"none","reason":"closing","when":"","state":"closed"}'`, false, true, "closed before the mint; not registering, minting or starting"},
		{"live", `echo '{"verdict":"none","reason":"","when":"","state":"live"}'`, false, false, "live before the mint; starting"},
		{"no --peek", `echo 'unknown flag: --peek' >&2; exit 2`, false, false, "unknown before the mint"},
		{"garbage", `echo 'not json'`, false, false, "unknown before the mint"},
		{"not on PATH", ``, true, false, "not on PATH"},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin, calls := fakePeekSpinclass(t, c.body)
			if c.noBin {
				bin = ""
			}
			logPath := filepath.Join(t.TempDir(), "close-self-run.log")
			p := preMintPeek{spinclass: bin, principal: restartTestPrincipal, environ: os.Environ(), logPath: logPath}
			if got := p.closed(); got != c.closed {
				t.Fatalf("closed() = %v, want %v", got, c.closed)
			}
			if !c.noBin {
				b, _ := os.ReadFile(calls)
				if want := "restart-preflight --peek|" + restartTestPrincipal + "\n"; string(b) != want {
					t.Fatalf("spinclass calls = %q, want %q", b, want)
				}
			}
			log, _ := os.ReadFile(logPath)
			if !strings.Contains(string(log), c.log) {
				t.Fatalf("log = %q, want %q", log, c.log)
			}
		})
	}
}

// The relaunch's peek is bounded at preMintPeekTimeout (5 s): a hung
// spinclass is unknown, and the launch goes on.
func TestPreMintPeekIsBounded(t *testing.T) {
	bin, _ := fakePeekSpinclass(t, "sleep 30")
	logPath := filepath.Join(t.TempDir(), "close-self-run.log")
	start := time.Now()
	if (preMintPeek{spinclass: bin, principal: restartTestPrincipal, environ: os.Environ(), logPath: logPath}).closed() {
		t.Fatal("a hung peek reported closed")
	}
	if took := time.Since(start); took < preMintPeekTimeout || took > preMintPeekTimeout+5*time.Second {
		t.Fatalf("took %v, want about %v", took, preMintPeekTimeout)
	}
}
