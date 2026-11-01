package toolcost

import (
	"os"
	"strings"
	"testing"
)

// The context-floor guard: the first response of a session carries nothing but
// the static prefix (system prompt, tool definitions), so its total input is
// the floor. When the floor is over FloorPct of the window, auto-compact can
// never get below it and loops; the run emits a gauge always and a counter plus
// a warning when it is over.

func appendFixture(t *testing.T, path string, f *fixture) {
	t.Helper()
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.WriteString(f.String()); err != nil {
		t.Fatal(err)
	}
}

func floorCfg() Config { return Config{Host: "h", Repo: "r", Window: 200000, FloorPct: 60} }

func TestFirstResponseFloorOverTheThresholdIsFlagged(t *testing.T) {
	var f fixture
	f.prompt("hi")
	f.assistant("m1", u{in: 10, cc5m: 130000}, "end_turn", text("ok")) // 130010 > 120000
	f.assistant("m2", u{in: 5, cr: 150000}, "end_turn", text("again")) // a later, bigger context
	p := writeFixture(t, &f)

	_, ms := mustRun(t, p, State{}, func() Config { c := floorCfg(); c.Final = true; return c }())
	if got, n := sum(ms, "clown.context_floor.h.r.opus-5-5"); n != 1 || got != 130010 {
		t.Errorf("floor gauge = %d (x%d), want 130010 once, from the FIRST response only", got, n)
	}
	if got, _ := sum(ms, "clown.context_floor_over.h.r.opus-5-5"); got != 1 {
		t.Errorf("floor_over counter = %d, want 1", got)
	}
	msg, over := FloorWarning(ms, floorCfg())
	if !over {
		t.Fatal("FloorWarning must report over")
	}
	for _, want := range []string{"130010", "200000", "60%", "auto-compact"} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning %q missing %q", msg, want)
		}
	}
}

func TestFloorUnderTheThresholdIsOnlyAGauge(t *testing.T) {
	var f fixture
	f.prompt("hi")
	f.assistant("m1", u{in: 10, cc5m: 40000}, "end_turn", text("ok"))
	p := writeFixture(t, &f)

	_, ms := mustRun(t, p, State{}, func() Config { c := floorCfg(); c.Final = true; return c }())
	if got, n := sum(ms, "clown.context_floor.h.r.opus-5-5"); n != 1 || got != 40010 {
		t.Errorf("floor gauge = %d (x%d), want 40010", got, n)
	}
	if _, n := sum(ms, "clown.context_floor_over.h.r.opus-5-5"); n != 0 {
		t.Error("a floor under the threshold must not emit the over counter")
	}
	if _, over := FloorWarning(ms, floorCfg()); over {
		t.Error("no warning under the threshold")
	}
}

// The same 175,868-token floor that is over 60% of a 200K window is fine in a 1M
// window; an unknown window judges nothing (no assumed 200K); and the operator's
// off switch silences the judgement while the gauge stays.
func TestFloorIsJudgedAgainstTheModelsWindow(t *testing.T) {
	var f fixture
	f.prompt("hi")
	f.assistant("m1", u{in: 8, cc5m: 175860}, "end_turn", text("ok")) // 175868
	p := writeFixture(t, &f)

	cases := []struct {
		name     string
		window   int64
		off      bool
		wantOver bool
	}{
		{"200K window", 200000, false, true},
		{"1M window", 1000000, false, false},
		{"unknown window", 0, false, false},
		{"200K window, switched off", 200000, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := floorCfg()
			cfg.Final, cfg.Window, cfg.FloorOff = true, c.window, c.off
			_, ms := mustRun(t, p, State{}, cfg)
			if got, n := sum(ms, "clown.context_floor.h.r.opus-5-5"); n != 1 || got != 175868 {
				t.Errorf("floor gauge = %d (x%d), want 175868 once whatever the window", got, n)
			}
			got, _ := sum(ms, "clown.context_floor_over.h.r.opus-5-5")
			if (got == 1) != c.wantOver {
				t.Errorf("floor_over = %d, want over=%v", got, c.wantOver)
			}
			msg, over := FloorWarning(ms, cfg)
			if over != c.wantOver {
				t.Errorf("FloorWarning over = %v (%q), want %v", over, msg, c.wantOver)
			}
		})
	}
}

// The floor is judged once per transcript: a later run over appended records
// emits no floor metric, and neither does a state saved before this feature.
func TestFloorIsCheckedOncePerTranscript(t *testing.T) {
	var f fixture
	f.prompt("hi")
	f.assistant("m1", u{in: 10, cc5m: 130000}, "end_turn", text("one"))
	f.assistant("m2", u{in: 5, cr: 131000}, "end_turn", text("two"))
	p := writeFixture(t, &f)
	c := floorCfg()

	st, first := mustRun(t, p, State{}, c)
	if _, n := sum(first, "clown.context_floor.h.r.opus-5-5"); n != 1 {
		t.Fatalf("first run floor gauges = %d, want 1", n)
	}
	var more fixture
	more.assistant("m3", u{in: 5, cr: 132000}, "end_turn", text("three"))
	appendFixture(t, p, &more)
	_, second := mustRun(t, p, st, func() Config { c := floorCfg(); c.Final = true; return c }())
	if _, n := sum(second, "clown.context_floor.h.r.opus-5-5"); n != 0 {
		t.Errorf("a later run re-emitted the floor (%d)", n)
	}

	legacy := st
	legacy.FloorDone = false // saved by a build that predates the guard
	_, third := mustRun(t, p, legacy, func() Config { c := floorCfg(); c.Final = true; return c }())
	if _, n := sum(third, "clown.context_floor.h.r.opus-5-5"); n != 0 {
		t.Errorf("a pre-guard state judged a later response as the floor (%d)", n)
	}
}
