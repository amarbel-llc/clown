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
parent    = "pebble-webhook@example-host"
room      = "pebble-9f3a@rooms.xmpp.example"
model     = "openrouter/anthropic/claude-sonnet"
system    = "You file one issue per actionable item."
task      = "<transcription text>"
moxyfile  = '''
[[servers]]
name = "smith"
'''
budget_key_ref = "/run/secrets/openrouter-key"
tools = ["smith_list_repos", "smith_create_issue"]

[env]
PEBBLE_TARGET_MODE = "shadow"

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
	if b.Principal != "3f1c" || b.Parent != "pebble-webhook@example-host" || b.Model == "" {
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

func TestStopOnPassRoundTrips(t *testing.T) {
	b, err := Parse([]byte(issueFilerBrief))
	if err != nil {
		t.Fatal(err)
	}
	if b.Evaluator.StopOnPass {
		t.Fatal("stop_on_pass defaults to false")
	}
	if out, _ := b.Marshal(); strings.Contains(string(out), "stop_on_pass") {
		t.Errorf("an unset stop_on_pass is omitted:\n%s", out)
	}
	b2, err := Parse([]byte(strings.Replace(issueFilerBrief, "kind = \"jq\"\n", "kind = \"jq\"\nstop_on_pass = true\n", 1)))
	if err != nil || !b2.Evaluator.StopOnPass {
		t.Fatalf("stop_on_pass = %+v, %v", b2, err)
	}
	out, err := b2.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if b3, err := Parse(out); err != nil || !b3.Evaluator.StopOnPass {
		t.Errorf("stop_on_pass lost in Marshal:\n%s", out)
	}
}

func TestValidationErrorsNameField(t *testing.T) {
	cases := []struct {
		name, from, to, wantField string
	}{
		{"schema", "schema = 1", "schema = 2", "schema"},
		{"principal", `principal = "3f1c"`, `principal = ""`, "principal"},
		{"parent", `parent    = "pebble-webhook@example-host"`, `parent = ""`, "parent"},
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

func TestToolsAndEnv(t *testing.T) {
	b, err := Parse([]byte(issueFilerBrief))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Tools) != 2 || b.Tools[1] != "smith_create_issue" || b.Env["PEBBLE_TARGET_MODE"] != "shadow" {
		t.Errorf("tools/env = %v %v", b.Tools, b.Env)
	}
	out, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b2, err := Parse(out)
	if err != nil || b2.Env["PEBBLE_TARGET_MODE"] != "shadow" || len(b2.Tools) != 2 {
		t.Errorf("round trip: %+v %v", b2, err)
	}
	for name, src := range map[string]string{
		"no tools":    strings.Replace(issueFilerBrief, `tools = ["smith_list_repos", "smith_create_issue"]`, "", 1),
		"empty tools": strings.Replace(issueFilerBrief, `tools = ["smith_list_repos", "smith_create_issue"]`, "tools = []", 1),
		"dup tools":   strings.Replace(issueFilerBrief, `"smith_list_repos", "smith_create_issue"`, `"a", "a"`, 1),
	} {
		if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "tools") {
			t.Errorf("%s: got %v", name, err)
		}
	}
	for _, key := range []string{"CLOWN_SESSION_ID", "TROUPE_XMPP_USER", "JUGGLER_MOXY_URL"} {
		src := strings.Replace(issueFilerBrief, "PEBBLE_TARGET_MODE", key, 1)
		if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "env") {
			t.Errorf("reserved env key %s: got %v", key, err)
		}
	}
}

func TestParseTemplate(t *testing.T) {
	tmpl := issueFilerBrief
	for _, line := range []string{`principal = "3f1c"`, `parent    = "pebble-webhook@example-host"`, `room      = "pebble-9f3a@rooms.xmpp.example"`, `task      = "<transcription text>"`} {
		tmpl = strings.Replace(tmpl, line, "", 1)
	}
	b, err := ParseTemplate([]byte(tmpl))
	if err != nil {
		t.Fatalf("ParseTemplate: %v", err)
	}
	if _, err := Parse([]byte(tmpl)); err == nil {
		t.Error("Parse must stay strict on an unfilled template")
	}
	b.Principal, b.Parent, b.Room, b.Task = "p", "q", "r@x", "t"
	if err := b.Validate(); err != nil {
		t.Errorf("filled template: %v", err)
	}
	noModel := strings.Replace(tmpl, `model     = "openrouter/anthropic/claude-sonnet"`, "", 1)
	if _, err := ParseTemplate([]byte(noModel)); err == nil || !strings.Contains(err.Error(), "model") {
		t.Errorf("template without model: %v", err)
	}
}

func TestInvalidTOML(t *testing.T) {
	if _, err := Parse([]byte("schema = ")); err == nil {
		t.Error("expected error")
	}
}
