package jugglereval

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

const issueFilerLedger = `{
  "schema": 1,
  "calls": [
    {"tool": "list_repos",   "kind": null,    "ok": true,  "uris": []},
    {"tool": "create_issue", "kind": "issue", "ok": true,
     "uris": ["https://code.example/clown/issues/301"]}
  ],
  "end": {"reason": "end_turn"},
  "cannot_complete": null,
  "steps": 3,
  "elapsed_ms": 18340
}`

const issueFilerProgram = `
  .cannot_complete == null
  and ([.calls[] | select(.kind == "issue" and .ok)] | length) >= 1
`

const noteFilerProgram = `
  .cannot_complete == null
  and ([.calls[] | select(.kind == "note" and .ok)] | length) >= 1
`

const routeProgram = `.cannot_complete == null and ([.calls[] | select(.kind == "route" and .ok)] | length) == 1`

func eval(t *testing.T, program, ledger string) (bool, error) {
	t.Helper()
	return Evaluate(context.Background(), KindJQ, program, json.RawMessage(ledger))
}

func mustVerdict(t *testing.T, want bool, program, ledger string) {
	t.Helper()
	got, err := eval(t, program, ledger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("verdict = %v, want %v", got, want)
	}
}

func TestIssueFiler(t *testing.T) {
	mustVerdict(t, true, issueFilerProgram, issueFilerLedger)
	zero := `{"schema":1,"calls":[{"tool":"list_repos","kind":null,"ok":true,"uris":[]}],"cannot_complete":null}`
	mustVerdict(t, false, issueFilerProgram, zero)
	failedCreate := `{"schema":1,"calls":[{"tool":"create_issue","kind":"issue","ok":false,"uris":[]}],"cannot_complete":null}`
	mustVerdict(t, false, issueFilerProgram, failedCreate)
	cannot := `{"schema":1,"calls":[{"tool":"create_issue","kind":"issue","ok":true,"uris":["u"]}],"cannot_complete":{"reason":"no repo matches"}}`
	mustVerdict(t, false, issueFilerProgram, cannot)
	mustVerdict(t, false, issueFilerProgram, `{"schema":1,"calls":[],"cannot_complete":null}`)
}

func TestNoteFiler(t *testing.T) {
	ok := `{"schema":1,"calls":[{"tool":"write_note","kind":"note","ok":true,"uris":["n1"]}],"cannot_complete":null}`
	mustVerdict(t, true, noteFilerProgram, ok)
	mustVerdict(t, false, noteFilerProgram, issueFilerLedger)
	cannot := `{"schema":1,"calls":[{"tool":"write_note","kind":"note","ok":true,"uris":["n1"]}],"cannot_complete":{"reason":"x"}}`
	mustVerdict(t, false, noteFilerProgram, cannot)
}

func TestRouteCount(t *testing.T) {
	one := `{"schema":1,"calls":[{"tool":"route","kind":"route","ok":true}],"cannot_complete":null}`
	mustVerdict(t, true, routeProgram, one)
	mustVerdict(t, false, routeProgram, `{"schema":1,"calls":[],"cannot_complete":null}`)
	failed := `{"schema":1,"calls":[{"tool":"route","kind":"route","ok":false}],"cannot_complete":null}`
	mustVerdict(t, false, routeProgram, failed)
	two := `{"schema":1,"calls":[{"tool":"route","kind":"route","ok":true},{"tool":"route","kind":"route","ok":true}],"cannot_complete":null}`
	mustVerdict(t, false, routeProgram, two)
	cannot := `{"schema":1,"calls":[{"tool":"route","kind":"route","ok":true}],"cannot_complete":{"reason":"x"}}`
	mustVerdict(t, false, routeProgram, cannot)
}

func TestInputIsLedger(t *testing.T) {
	mustVerdict(t, true, `.steps == 3 and .schema == 1`, issueFilerLedger)
}

func TestOutputMustBeExactlyOneBoolean(t *testing.T) {
	cases := map[string]string{
		"zero outputs":  `empty`,
		"two outputs":   `true, true`,
		"non-boolean":   `1`,
		"null":          `null`,
		"string":        `"true"`,
		"runtime err":   `error("boom")`,
		"type error":    `.steps | ascii_downcase`,
		"bool then err": `true, error("late")`,
	}
	for name, program := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := eval(t, program, issueFilerLedger)
			if err == nil {
				t.Fatalf("expected error, got verdict %v", got)
			}
			if got {
				t.Error("verdict must be false on error")
			}
		})
	}
}

func TestNoEnvironment(t *testing.T) {
	t.Setenv("JUGGLEREVAL_SECRET", "leak")
	mustVerdict(t, true, `$ENV.JUGGLEREVAL_SECRET == null and (env | length) == 0`, issueFilerLedger)
}

func TestNoInputOrInputs(t *testing.T) {
	for _, program := range []string{`input | true`, `[inputs] | length == 0`, `input == 1`} {
		if got, err := eval(t, program, issueFilerLedger); err == nil || got {
			t.Errorf("program %q: got (%v, %v), want error", program, got, err)
		}
	}
}

func TestNoModules(t *testing.T) {
	for _, program := range []string{`import "a" as a; true`, `include "a"; true`} {
		if got, err := eval(t, program, issueFilerLedger); err == nil || got {
			t.Errorf("program %q: got (%v, %v), want error", program, got, err)
		}
	}
}

func TestWallClockCutsOffLoops(t *testing.T) {
	for _, program := range []string{`def f: f; f`, `last(repeat(.)) | true`} {
		t.Run(program, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			start := time.Now()
			got, err := Evaluate(ctx, KindJQ, program, json.RawMessage(issueFilerLedger))
			if err == nil || got {
				t.Fatalf("got (%v, %v), want error", got, err)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("took %v, context did not cut it off", elapsed)
			}
		})
	}
}

func TestDefaultTimeoutApplied(t *testing.T) {
	start := time.Now()
	got, err := Evaluate(context.Background(), KindJQ, `def f: f; f`, json.RawMessage(issueFilerLedger))
	if err == nil || got {
		t.Fatalf("got (%v, %v), want error", got, err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("default timeout not applied, took %v", elapsed)
	}
}

func TestUnsupportedKind(t *testing.T) {
	for _, kind := range []string{"predicate", "agent", ""} {
		if got, err := Evaluate(context.Background(), kind, `true`, json.RawMessage(`{}`)); err == nil || got {
			t.Errorf("kind %q: got (%v, %v), want error", kind, got, err)
		}
	}
}

func TestBadInputs(t *testing.T) {
	if _, err := eval(t, `true`, `{not json`); err == nil {
		t.Error("bad ledger: expected error")
	}
	if _, err := eval(t, `true`, `{} {}`); err == nil {
		t.Error("two ledger documents: expected error")
	}
	if _, err := eval(t, `.[`, `{}`); err == nil {
		t.Error("bad program: expected error")
	}
}
