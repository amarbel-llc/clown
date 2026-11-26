package agentscope

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

func TestRedactSetenvKeepsKeysDropsValues(t *testing.T) {
	got := RedactSetenv([]string{"systemd-run", "--setenv=NIX_CONFIG=access-tokens = github.com=secret", "--setenv=A=b=c", "--property=X=y"})
	want := []string{"systemd-run", "--setenv=NIX_CONFIG=<redacted>", "--setenv=A=<redacted>", "--property=X=y"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, a := range got {
		if strings.Contains(a, "secret") {
			t.Errorf("value leaked: %q", a)
		}
	}
}

// Every spelling of an assignment systemd-run accepts is redacted, so a later
// change to another spelling cannot reopen the leak.
func TestRedactSetenvCoversEverySpelling(t *testing.T) {
	in := []string{
		"systemd-run",
		"--setenv", "TWO=secret-two",
		"-E", "SHORT=secret-short",
		"-E=SHORTEQ=secret-shorteq",
		"--setenv=ONE=secret-one",
		"--property=Environment=A=secret-a B=secret-b",
		"-p", "Environment=C=secret-c",
		"--property=UnsetEnvironment=KEEP_THIS_KEY",
		"--", "claude",
	}
	orig := slices.Clone(in)
	got := RedactSetenv(in)
	if !slices.Equal(in, orig) {
		t.Errorf("the input was modified: %v", in)
	}
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "secret") {
		t.Errorf("a value leaked: %s", joined)
	}
	for _, want := range []string{"TWO=<redacted>", "SHORT=<redacted>", "SHORTEQ=<redacted>", "ONE=<redacted>", "Environment=<redacted>", "KEEP_THIS_KEY", "claude"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
}

func TestLoggedArgvReducesTheProviderTail(t *testing.T) {
	argv := []string{"systemd-run", "--setenv=A=1", "--", "/shim", "--", "/bin/claude", "--settings", `{"env":{"X":"secret-settings"}}`}
	got := LoggedArgv(argv, 3)
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "secret-settings") || strings.Contains(joined, "--settings") {
		t.Errorf("the provider's arguments reached the log: %s", joined)
	}
	if !strings.Contains(joined, "/bin/claude <2 more args>") || !strings.Contains(joined, "--setenv=A=<redacted>") {
		t.Errorf("got %s", joined)
	}
	// A count that does not fit leaves the whole line redacted, not cut.
	if got := LoggedArgv(argv, 0); !strings.Contains(strings.Join(got, " "), "/bin/claude") {
		t.Errorf("providerArgs 0 must keep every element: %v", got)
	}
}

// The full unit's own argv carries credentials when the model proxy is off
// (ANTHROPIC_ and CLAUDE_CODE_ pass through), so the start log must not show
// them: redact what SystemdRunArgs really renders.
func TestRedactSetenvHidesAFullUnitsCredentials(t *testing.T) {
	l := testLaunch()
	l.FrontendEnv = append(l.FrontendEnv, "ANTHROPIC_API_KEY=sk-secret-1", "CLAUDE_CODE_OAUTH_TOKEN=tok-secret-2")
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.Join(args, " ")
	if !strings.Contains(raw, "sk-secret-1") || !strings.Contains(raw, "tok-secret-2") {
		t.Fatalf("premise: the proxy-off unit carries the credentials in its argv: %v", args)
	}
	logged := strings.Join(RedactSetenv(args), " ")
	if strings.Contains(logged, "secret") {
		t.Errorf("a credential reached the logged argv: %s", logged)
	}
	if !strings.Contains(logged, "--setenv=ANTHROPIC_API_KEY=<redacted>") {
		t.Errorf("the key should still be named: %s", logged)
	}
}

// Run itself must call the redaction: reverting its log line would leave every
// test above green, so this one drives Run with a fake systemd-run and reads
// what it logs.
func TestRunLogsNoEnvironmentValuesAndNoProviderTail(t *testing.T) {
	l := testLaunch()
	l.FrontendEnv = append(l.FrontendEnv, "ANTHROPIC_API_KEY=sk-secret-1", "CLAUDE_CODE_OAUTH_TOKEN=tok-secret-2")
	l.Argv = []string{"/nix/store/cl/bin/claude", "--settings", `{"env":{"X":"settings-secret-3"}}`}
	systemctl, _ := fakeSystemctl(t)
	tools := Tools{SystemdRun: fakeSystemdRun(t), Systemctl: systemctl, show: func(string) (Status, error) {
		return Status{ActiveState: "inactive"}, nil
	}}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	_, _ = Run(l, tools, logger)
	out := buf.String()
	if !strings.Contains(out, "starting unit") {
		t.Fatalf("Run logged no start line: %q", out)
	}
	for _, leaked := range []string{"sk-secret-1", "tok-secret-2", "settings-secret-3"} {
		if strings.Contains(out, leaked) {
			t.Errorf("%q reached Run's log: %s", leaked, out)
		}
	}
	if !strings.Contains(out, "ANTHROPIC_API_KEY=<redacted>") {
		t.Errorf("the key should be logged, its value not: %s", out)
	}
}
