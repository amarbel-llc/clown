package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const restartTestPrincipal = "9eff073f-a924-48c2-b076-8b02d38f1938"

func TestControlSocketPath(t *testing.T) {
	if got, want := controlSocketPath("/run/user/1001", "abc"), "/run/user/1001/clown/control/abc.sock"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func runRestartVerbForTest(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = runRestartVerb(args, &out, &errb)
	return code, out.String(), errb.String()
}

func startTestControl(t *testing.T, runtime string, handle func(controlMessage) controlReply) *controlListener {
	t.Helper()
	l, err := startControlListener(controlSocketPath(runtime, restartTestPrincipal), handle)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	return l
}

// spinclass#419: no frontend is listening for the key, so spinclass must see
// a non-zero exit and this exact message (it clears the request and shows it).
func TestRestartVerbWithoutALiveFrontendFails(t *testing.T) {
	runtime := shortSocketDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtime)

	code, _, stderr := runRestartVerbForTest(t, "--session", restartTestPrincipal, "--when", "idle", "--reason", "new tools")
	if code == 0 {
		t.Fatal("no live frontend must exit non-zero")
	}
	if want := "no live clown frontend for " + restartTestPrincipal + "\n"; stderr != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}

	// A stale socket file left by a dead frontend is no frontend either.
	stale := controlSocketPath(runtime, restartTestPrincipal)
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	code, _, stderr = runRestartVerbForTest(t, "--session", restartTestPrincipal)
	if code == 0 || stderr != "no live clown frontend for "+restartTestPrincipal+"\n" {
		t.Fatalf("stale socket: code %d, stderr %q", code, stderr)
	}
}

// An armed frontend queues {when, reason} and the verb returns 0 at once.
func TestRestartVerbQueuesOnAnArmedFrontend(t *testing.T) {
	runtime := shortSocketDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	q := &restartQueue{}
	startTestControl(t, runtime, restartControlHandler("claude", "", q))

	code, stdout, stderr := runRestartVerbForTest(t, "--session", restartTestPrincipal, "--when", "idle", "--reason", "pick up new tools")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "queued") {
		t.Errorf("stdout = %q, want a queued confirmation", stdout)
	}
	got, ok := q.take()
	if !ok || got != (restartRequest{When: "idle", Reason: "pick up new tools"}) {
		t.Fatalf("queued = %+v, %v", got, ok)
	}
	if _, ok := q.take(); ok {
		t.Fatal("take must empty the queue")
	}

	// --when defaults to idle.
	if code, _, stderr := runRestartVerbForTest(t, "--session", restartTestPrincipal); code != 0 {
		t.Fatalf("default when: code %d, %q", code, stderr)
	}
	if got, _ := q.take(); got.When != "idle" {
		t.Fatalf("default when = %q, want idle", got.When)
	}
}

// A frontend whose provider has no idle trigger refuses with the exact message.
func TestRestartVerbOnAnUnarmedProviderFails(t *testing.T) {
	runtime := shortSocketDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	q := &restartQueue{}
	startTestControl(t, runtime, restartControlHandler("opencode", "provider is not claude", q))

	code, _, stderr := runRestartVerbForTest(t, "--session", restartTestPrincipal, "--reason", "x")
	if code == 0 {
		t.Fatal("an unarmed provider must exit non-zero")
	}
	if want := "restart needs an idle trigger; opencode has none\n"; stderr != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
	if _, ok := q.take(); ok {
		t.Fatal("an unarmed frontend must queue nothing")
	}
}

// claude whose self-close is off for another reason says why.
func TestRestartVerbOnAnUnarmedClaudeNamesTheGate(t *testing.T) {
	runtime := shortSocketDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	startTestControl(t, runtime, restartControlHandler("claude", "CLOWN_DISABLE_SELF_CLOSE=1", &restartQueue{}))
	code, _, stderr := runRestartVerbForTest(t, "--session", restartTestPrincipal)
	if code == 0 || !strings.Contains(stderr, "restart needs an idle trigger") || !strings.Contains(stderr, "CLOWN_DISABLE_SELF_CLOSE=1") {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
}

func TestRestartVerbRejectsBadArguments(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortSocketDir(t))
	for _, args := range [][]string{
		nil,
		{"--when", "idle"},
		{"--session", ""},
		{"--session", "a/b"},
		{"--session", ".."},
		{"--session", restartTestPrincipal, "--when", "now"},
		{"--session", restartTestPrincipal, "--when", "later"},
		{"--session", restartTestPrincipal, "--bogus"},
		{"--session", restartTestPrincipal, "--reason"},
	} {
		if code, _, stderr := runRestartVerbForTest(t, args...); code == 0 || stderr == "" {
			t.Errorf("runRestartVerb(%q) = %d, stderr %q; want a usage error", args, code, stderr)
		}
	}
}

// The control socket follows the idle socket's hygiene: 0600 in a 0700 dir,
// removed on close.
func TestControlSocketModes(t *testing.T) {
	runtime := shortSocketDir(t)
	path := controlSocketPath(runtime, restartTestPrincipal)
	l, err := startControlListener(path, restartControlHandler("claude", "", &restartQueue{}))
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket: %v %v, want a 0600 socket", fi, err)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("dir: %v %v, want 0700", di, err)
	}
	l.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket must be removed on close, stat err = %v", err)
	}
}

// A second frontend for the same principal must not take over a live control
// socket (the first would silently lose its restarts).
func TestControlSocketRefusesALiveSocket(t *testing.T) {
	runtime := shortSocketDir(t)
	startTestControl(t, runtime, restartControlHandler("claude", "", &restartQueue{}))
	if _, err := startControlListener(controlSocketPath(runtime, restartTestPrincipal), restartControlHandler("claude", "", &restartQueue{})); err == nil || !strings.Contains(err.Error(), "live") {
		t.Fatalf("err = %v, want a live-socket refusal", err)
	}
}

func TestRestartUnarmedReason(t *testing.T) {
	f := parsedFlags{provider: "opencode"}
	f.identity.Key = "k"
	if got := restartUnarmedReason(f, true); got != "" {
		t.Fatalf("armed: %q", got)
	}
	if got := restartUnarmedReason(f, false); got != "provider is not claude" {
		t.Fatalf("opencode: %q", got)
	}
	bin, _ := fakeSpinclass(t, t.TempDir(), "0")
	t.Setenv("SPINCLASS_SESSION_ID", "repo/b")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1")
	t.Setenv("PATH", filepath.Dir(bin))
	t.Setenv("CLOWN_DISABLE_SELF_CLOSE", "")
	f.provider = "claude"
	if got := restartUnarmedReason(f, false); !strings.Contains(got, "idle listener") {
		t.Fatalf("a passing gate with no listener: %q", got)
	}
}

// A newer request replaces an older one: one restart, the latest reason.
func TestRestartQueueKeepsTheLatestRequest(t *testing.T) {
	q := &restartQueue{}
	q.put(restartRequest{When: "idle", Reason: "first"})
	q.put(restartRequest{When: "idle", Reason: "second"})
	got, ok := q.take()
	if !ok || got.Reason != "second" {
		t.Fatalf("take = %+v, %v", got, ok)
	}
	if _, ok := q.take(); ok {
		t.Fatal("one restart, not two")
	}
}
