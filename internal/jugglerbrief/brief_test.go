package jugglerbrief

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

const issueFilerBrief = `
schema = 1
principal = "3f1c"
parent    = "pebble-webhook@krone"
room      = "pebble-9f3a@rooms.xmpp.example"
model     = "openrouter/anthropic/claude-sonnet"
system    = "You file one issue per actionable item."
task      = "<transcription text>"
moxyfile  = '''
[[servers]]
name = "smith"
'''
budget_key_ref = "/run/secrets/openrouter-key"

[limits]
steps = 12
wall_clock = "2m"

[evaluator]
kind = "jq"
program = '''
  .cannot_complete == null
  and ([.calls[] | select(.kind == "issue" and .ok)] | length) >= 1
'''
`

func TestParseIssueFiler(t *testing.T) {
	b, err := Parse([]byte(issueFilerBrief))
	if err != nil {
		t.Fatal(err)
	}
	if b.Principal != "3f1c" || b.Parent != "pebble-webhook@krone" || b.Model == "" {
		t.Errorf("unexpected brief: %+v", b)
	}
	if !strings.Contains(b.Moxyfile, `name = "smith"`) {
		t.Errorf("moxyfile not kept raw: %q", b.Moxyfile)
	}
	if b.BudgetKeyRef != "/run/secrets/openrouter-key" {
		t.Errorf("budget_key_ref = %q", b.BudgetKeyRef)
	}
	d, err := b.Limits.WallClockDuration()
	if err != nil || d != 2*time.Minute {
		t.Errorf("wall clock = %v, %v", d, err)
	}
}

func TestDefaults(t *testing.T) {
	src := strings.Replace(issueFilerBrief, "[limits]\nsteps = 12\nwall_clock = \"2m\"\n", "", 1)
	b, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if b.Limits.Steps != 12 || b.Limits.WallClock != "2m" {
		t.Errorf("defaults not applied: %+v", b.Limits)
	}
}

func TestRoundTripDeterministic(t *testing.T) {
	src := strings.Replace(issueFilerBrief, "wall_clock = \"2m\"\n", "wall_clock = \"2m\"\n[limits.sandbox]\nprivate_tmp = true\n", 1)
	b, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	first, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b2, err := Parse(first)
	if err != nil {
		t.Fatalf("reparse: %v\n%s", err, first)
	}
	second, err := b2.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("marshal not stable:\n%s\n---\n%s", first, second)
	}
	if b2.Task != b.Task || b2.Moxyfile != b.Moxyfile || b2.Evaluator != b.Evaluator || b2.BudgetKeyRef != b.BudgetKeyRef {
		t.Errorf("round trip lost data: %+v vs %+v", b, b2)
	}
	if b2.Limits.Sandbox == nil {
		t.Errorf("reserved sandbox value not preserved")
	}
}

func TestValidationErrorsNameField(t *testing.T) {
	cases := []struct {
		name, from, to, wantField string
	}{
		{"schema", "schema = 1", "schema = 2", "schema"},
		{"principal", `principal = "3f1c"`, `principal = ""`, "principal"},
		{"parent", `parent    = "pebble-webhook@krone"`, `parent = ""`, "parent"},
		{"model", `model     = "openrouter/anthropic/claude-sonnet"`, `model = ""`, "model"},
		{"task", `task      = "<transcription text>"`, `task = ""`, "task"},
		{"steps", "steps = 12", "steps = -1", "limits.steps"},
		{"wall_clock", `wall_clock = "2m"`, `wall_clock = "soon"`, "limits.wall_clock"},
		{"wall_clock zero", `wall_clock = "2m"`, `wall_clock = "0s"`, "limits.wall_clock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(issueFilerBrief, tc.from) {
				t.Fatalf("fixture lacks %q", tc.from)
			}
			src := strings.Replace(issueFilerBrief, tc.from, tc.to, 1)
			_, err := Parse([]byte(src))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Errorf("error %q does not name %q", err, tc.wantField)
			}
		})
	}
}

func TestEmptyProgramRejected(t *testing.T) {
	b, err := Parse([]byte(issueFilerBrief))
	if err != nil {
		t.Fatal(err)
	}
	b.Evaluator.Program = "  \n"
	if err := b.Validate(); err == nil || !strings.Contains(err.Error(), "evaluator.program") {
		t.Errorf("got %v", err)
	}
}

func TestEvaluatorKinds(t *testing.T) {
	for _, kind := range []string{"predicate", "agent"} {
		src := strings.Replace(issueFilerBrief, `kind = "jq"`, `kind = "`+kind+`"`, 1)
		_, err := Parse([]byte(src))
		if err == nil || !strings.Contains(err.Error(), "evaluator.kind") || !strings.Contains(err.Error(), "not yet supported") {
			t.Errorf("kind %s: got %v", kind, err)
		}
	}
	src := strings.Replace(issueFilerBrief, `kind = "jq"`, `kind = "wasm"`, 1)
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "evaluator.kind") {
		t.Errorf("unknown kind: got %v", err)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	_, err := Parse([]byte(issueFilerBrief + "\nstepz = 3\n"))
	if err == nil || !strings.Contains(err.Error(), "stepz") {
		t.Errorf("got %v", err)
	}
}

func TestInvalidTOML(t *testing.T) {
	if _, err := Parse([]byte("schema = ")); err == nil {
		t.Error("expected error")
	}
}
