package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func shortSocketDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "idle")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func pokeIdle(t *testing.T, sock string) {
	t.Helper()
	c, err := net.Dial("unixgram", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
}

// clown#287: each idle poke runs the closer, runs never overlap, and pokes that
// arrive during a run collapse into exactly one more run.
func TestIdleListenerSerializesAndCoalesces(t *testing.T) {
	sock := filepath.Join(shortSocketDir(t), "run", "idle", "k.sock")
	var started, active, maxActive atomic.Int32
	release := make(chan struct{})
	l, err := startIdleListener(sock, func() {
		n := started.Add(1)
		cur := active.Add(1)
		if cur > maxActive.Load() {
			maxActive.Store(cur)
		}
		if n == 1 {
			<-release
		}
		active.Add(-1)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	pokeIdle(t, sock)
	waitFor(t, "first run to start", func() bool { return started.Load() == 1 })
	for range 5 {
		pokeIdle(t, sock)
	}
	time.Sleep(50 * time.Millisecond)
	if got := started.Load(); got != 1 {
		t.Fatalf("a run started while another was in flight: started = %d", got)
	}
	close(release)
	waitFor(t, "the coalesced rerun", func() bool { return started.Load() == 2 })
	time.Sleep(100 * time.Millisecond)
	if got := started.Load(); got != 2 {
		t.Fatalf("the five pokes must coalesce into one rerun, got %d runs", got)
	}
	if maxActive.Load() != 1 {
		t.Fatalf("runs overlapped: max concurrent = %d", maxActive.Load())
	}
}

func TestIdleListenerCloseRemovesSocket(t *testing.T) {
	sock := filepath.Join(shortSocketDir(t), "k.sock")
	l, err := startIdleListener(sock, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket should exist with mode 0600: %v %v", fi, err)
	}
	l.Close()
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket must be removed on close, stat err = %v", err)
	}
}

func TestIdleNotifyDeliversAPoke(t *testing.T) {
	sock := filepath.Join(shortSocketDir(t), "k.sock")
	var runs atomic.Int32
	l, err := startIdleListener(sock, func() { runs.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if code := runIdleNotify([]string{sock}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	waitFor(t, "the poke", func() bool { return runs.Load() == 1 })
}

// A Stop hook must never fail or stall the agent: no socket, a stale path or a
// malformed call all exit 0.
func TestIdleNotifyNeverFailsTheHook(t *testing.T) {
	for _, args := range [][]string{nil, {"a", "b"}, {filepath.Join(shortSocketDir(t), "absent.sock")}} {
		start := time.Now()
		if code := runIdleNotify(args); code != 0 {
			t.Errorf("runIdleNotify(%v) = %d, want 0", args, code)
		}
		if time.Since(start) > 2*time.Second {
			t.Errorf("runIdleNotify(%v) stalled for %v", args, time.Since(start))
		}
	}
}

func fakeSpinclass(t *testing.T, dir, exitCode string) (bin, out string) {
	t.Helper()
	out = filepath.Join(dir, "invocation")
	bin = filepath.Join(dir, "spinclass")
	script := "#!/bin/sh\nprintf '%s|%s\\n' \"$*\" \"$CLOWN_SESSION_ID\" > '" + out + "'\necho noise on stdout\necho noise on stderr >&2\nexit " + exitCode + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, out
}

// The runner invokes `spinclass close-self-run` with the principal in its
// environment, keeps its output off the terminal (claude owns the tty), and a
// refusal (non-zero exit) is not a clown error.
func TestSelfCloseRunnerRunsCloseSelfRunWithPrincipal(t *testing.T) {
	dir := t.TempDir()
	bin, out := fakeSpinclass(t, dir, "3")
	logPath := filepath.Join(dir, "state", "close-self-run.log")
	r := &selfCloseRunner{bin: bin, principal: "9eff073f-a924-48c2-b076-8b02d38f1938", logPath: logPath}
	if err := r.run(); err != nil {
		t.Fatalf("a refusal must not be an error, got %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := "close-self-run|9eff073f-a924-48c2-b076-8b02d38f1938\n"; string(got) != want {
		t.Fatalf("invocation = %q, want %q", got, want)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("output must land in the log file: %v", err)
	}
	for _, want := range []string{"noise on stdout", "noise on stderr", "exit status 3"} {
		if !strings.Contains(string(logged), want) {
			t.Errorf("log missing %q:\n%s", want, logged)
		}
	}
}

// Each session has its own socket and its listener closes over its own
// principal: a poke on one session's socket can only run THAT session's
// close-self-run, never another's, whatever the datagram says.
func TestAnotherSessionsSocketCannotTriggerThisOnesClose(t *testing.T) {
	sockDir := shortSocketDir(t)
	start := func(principal string) (sock, out string) {
		bin, out := fakeSpinclass(t, t.TempDir(), "0")
		r := &selfCloseRunner{bin: bin, principal: principal, logPath: filepath.Join(t.TempDir(), "l.log")}
		sock = idleSocketPath(sockDir, principal)
		l, err := startIdleListener(sock, func() { _ = r.run() })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(l.Close)
		return sock, out
	}
	sockA, outA := start("session-a")
	sockB, outB := start("session-b")
	if sockA == sockB {
		t.Fatal("sessions must not share a socket path")
	}

	// A datagram naming the other session changes nothing: contents are ignored.
	c, err := net.Dial("unixgram", sockB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("session-a")); err != nil {
		t.Fatal(err)
	}
	c.Close()

	waitFor(t, "session b's close-self-run", func() bool { _, err := os.Stat(outB); return err == nil })
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(outA); !os.IsNotExist(err) {
		t.Fatalf("a poke on session b's socket ran session a's close-self-run (stat err = %v)", err)
	}
	got, err := os.ReadFile(outB)
	if err != nil || !strings.HasSuffix(string(got), "|session-b\n") {
		t.Fatalf("session b's run = %q (%v), want principal session-b", got, err)
	}
}

func TestSelfCloseRunnerMissingBinaryIsAnError(t *testing.T) {
	r := &selfCloseRunner{bin: filepath.Join(t.TempDir(), "absent"), principal: "k", logPath: filepath.Join(t.TempDir(), "l.log")}
	if err := r.run(); err == nil {
		t.Fatal("want an error when the runner cannot be started")
	}
}

func TestIdleSocketPath(t *testing.T) {
	if got, want := idleSocketPath("/run/user/1001", "abc"), "/run/user/1001/clown/idle/abc.sock"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSelfCloseArmedGates(t *testing.T) {
	bin, _ := fakeSpinclass(t, t.TempDir(), "0")
	binDir := filepath.Dir(bin)
	base := func() parsedFlags {
		f := parsedFlags{provider: "claude"}
		f.identity.Key = "k-1"
		return f
	}
	arm := func(t *testing.T, f parsedFlags) (string, bool) {
		t.Helper()
		t.Setenv("SPINCLASS_SESSION_ID", "repo/branch")
		t.Setenv("XDG_RUNTIME_DIR", "/run/user/1001")
		t.Setenv("PATH", binDir)
		return selfCloseArmed(f)
	}

	t.Run("a claude launch in a spinclass session arms", func(t *testing.T) {
		got, ok := arm(t, base())
		if !ok || got != bin {
			t.Fatalf("armed = %v, bin = %q, want true, %q", ok, got, bin)
		}
	})
	for name, mutate := range map[string]func(*parsedFlags){
		"other providers": func(f *parsedFlags) { f.provider = "codex" },
		"naked":           func(f *parsedFlags) { f.naked = true },
		"launch plan":     func(f *parsedFlags) { f.printLaunchPlan = true },
		"no identity":     func(f *parsedFlags) { f.identity.Key = "" },
	} {
		t.Run(name+" do not", func(t *testing.T) {
			f := base()
			mutate(&f)
			if _, ok := arm(t, f); ok {
				t.Fatal("must not arm")
			}
		})
	}
	t.Run("outside a spinclass session", func(t *testing.T) {
		arm(t, base())
		t.Setenv("SPINCLASS_SESSION_ID", "")
		if _, ok := selfCloseArmed(base()); ok {
			t.Fatal("must not arm without a spinclass session")
		}
	})
	t.Run("without a runtime dir", func(t *testing.T) {
		arm(t, base())
		t.Setenv("XDG_RUNTIME_DIR", "")
		if _, ok := selfCloseArmed(base()); ok {
			t.Fatal("must not arm without XDG_RUNTIME_DIR")
		}
	})
	t.Run("without spinclass on PATH", func(t *testing.T) {
		arm(t, base())
		t.Setenv("PATH", t.TempDir())
		if _, ok := selfCloseArmed(base()); ok {
			t.Fatal("must not arm when spinclass cannot be found")
		}
	})
}

// A gate that fails must say which one: a silent unarmed launch is what made
// the first live check undiagnosable.
func TestSelfCloseDecisionNamesTheFailingGate(t *testing.T) {
	bin, _ := fakeSpinclass(t, t.TempDir(), "0")
	f := parsedFlags{provider: "claude"}
	f.identity.Key = "k-1"
	for _, c := range []struct {
		name, spin, runtime, path, want string
	}{
		{"no spinclass session", "", "/run/user/1", filepath.Dir(bin), "SPINCLASS_SESSION_ID"},
		{"no runtime dir", "repo/b", "", filepath.Dir(bin), "XDG_RUNTIME_DIR"},
		{"no spinclass on PATH", "repo/b", "/run/user/1", t.TempDir(), "spinclass"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("SPINCLASS_SESSION_ID", c.spin)
			t.Setenv("XDG_RUNTIME_DIR", c.runtime)
			t.Setenv("PATH", c.path)
			got, reason := selfCloseDecision(f)
			if got != "" || !strings.Contains(reason, c.want) {
				t.Fatalf("decision = %q, %q; want no binary and a reason naming %q", got, reason, c.want)
			}
		})
	}
	t.Setenv("SPINCLASS_SESSION_ID", "repo/b")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1")
	t.Setenv("PATH", filepath.Dir(bin))
	if got, reason := selfCloseDecision(f); got != bin || reason != "" {
		t.Fatalf("armed decision = %q, %q; want %q and no reason", got, reason, bin)
	}
}

// armSelfClose records its decision in close-self-run.log: armed (with the
// socket and spinclass path) or not armed (with the reason) on a claude launch.
func TestArmSelfCloseLogsItsDecision(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("SPINCLASS_SESSION_ID", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	f := parsedFlags{provider: "claude"}
	f.identity.Key = "k-1"
	dir, sock, _, closeFn := armSelfClose(f, testStagingRoot(t), nil, nil, nil)
	closeFn()
	if dir != "" || sock != "" {
		t.Fatalf("must not arm outside a spinclass session: %q %q", dir, sock)
	}
	b, err := os.ReadFile(filepath.Join(state, "clown", "close-self-run.log"))
	if err != nil {
		t.Fatalf("an unarmed claude launch must leave a log line: %v", err)
	}
	if !strings.Contains(string(b), "self-close not armed") || !strings.Contains(string(b), "SPINCLASS_SESSION_ID") {
		t.Fatalf("log = %q", b)
	}

	// Another provider is not a self-close candidate and logs nothing new.
	before := len(b)
	f.provider = "codex"
	_, _, _, closeFn = armSelfClose(f, testStagingRoot(t), nil, nil, nil)
	closeFn()
	after, _ := os.ReadFile(filepath.Join(state, "clown", "close-self-run.log"))
	if len(after) != before {
		t.Fatalf("a non-claude launch must not log, got %q", after[before:])
	}

	// Armed: the socket, hook dir and spinclass path are recorded.
	bin, _ := fakeSpinclass(t, t.TempDir(), "0")
	t.Setenv("SPINCLASS_SESSION_ID", "repo/b")
	t.Setenv("PATH", filepath.Dir(bin))
	f.provider = "claude"
	dir, sock, _, closeFn = armSelfClose(f, testStagingRoot(t), nil, nil, nil)
	defer closeFn()
	if dir == "" || sock == "" {
		t.Fatalf("expected to arm: %q %q", dir, sock)
	}
	armed, _ := os.ReadFile(filepath.Join(state, "clown", "close-self-run.log"))
	for _, want := range []string{"self-close armed", sock, bin} {
		if !strings.Contains(string(armed), want) {
			t.Errorf("log missing %q:\n%s", want, armed)
		}
	}
}

// The Stop hook must run the very binary that armed it. identity.Bin honors
// $CLOWN_BIN, which a devshell may point at an older clown that does not know
// `idle-notify` (live failure: "unknown flag idle-notify" after every turn), so
// the hook uses the arming process's own executable, never identity.Bin.
func TestArmSelfCloseHookRunsTheArmingExecutableNotCLOWN_BIN(t *testing.T) {
	bin, _ := fakeSpinclass(t, t.TempDir(), "0")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPINCLASS_SESSION_ID", "repo/b")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("PATH", filepath.Dir(bin))
	t.Setenv("CLOWN_DISABLE_SELF_CLOSE", "")
	f := parsedFlags{provider: "claude"}
	f.identity.Key = "k-1"
	f.identity.Bin = "/old/stub/clown" // what $CLOWN_BIN resolved to

	dir, sock, _, closeFn := armSelfClose(f, testStagingRoot(t), nil, nil, nil)
	defer closeFn()
	if dir == "" {
		t.Fatal("expected to arm")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	hb, err := os.ReadFile(filepath.Join(dir, "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(hb), "/old/stub/clown") {
		t.Fatalf("hook uses $CLOWN_BIN's binary: %s", hb)
	}
	if !strings.Contains(string(hb), exe+" idle-notify "+sock) {
		t.Fatalf("hook should run %q idle-notify %q:\n%s", exe, sock, hb)
	}
}

// CLOWN_DISABLE_SELF_CLOSE=1 is the off-switch: nothing is armed and no hook
// is registered.
func TestSelfCloseDisableSwitch(t *testing.T) {
	bin, _ := fakeSpinclass(t, t.TempDir(), "0")
	t.Setenv("SPINCLASS_SESSION_ID", "repo/b")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("PATH", filepath.Dir(bin))
	t.Setenv("CLOWN_DISABLE_SELF_CLOSE", "1")
	f := parsedFlags{provider: "claude"}
	f.identity.Key = "k-1"
	got, reason := selfCloseDecision(f)
	if got != "" || !strings.Contains(reason, "CLOWN_DISABLE_SELF_CLOSE") {
		t.Fatalf("decision = %q, %q", got, reason)
	}
}

// idle-notify leaves its outcome where the operator can find it (the unit's
// writable temp dir), since a hook has no other voice: both a delivered poke
// and a failed one are recorded, and neither changes the exit status.
func TestIdleNotifyRecordsItsOutcome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	logFile := filepath.Join(tmp, "clown-idle-notify.log")

	sock := filepath.Join(shortSocketDir(t), "k.sock")
	var runs atomic.Int32
	l, err := startIdleListener(sock, func() { runs.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if code := runIdleNotify([]string{sock}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if code := runIdleNotify([]string{filepath.Join(shortSocketDir(t), "absent.sock")}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("no idle-notify log: %v", err)
	}
	if !strings.Contains(string(b), "sent") || !strings.Contains(string(b), "dial failed") {
		t.Fatalf("log should record one delivery and one failure:\n%s", b)
	}
}

func TestSynthIdlePluginDirRegistersAStopHook(t *testing.T) {
	dir, err := synthIdlePluginDir(testStagingRoot(t), "/nix/store/c/bin/clown", "/run/user/1001/clown/idle/k.sock")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &m); err != nil || m.Name != "clown-selfclose" {
		t.Fatalf("manifest = %s (%v), want name clown-selfclose", b, err)
	}

	hb, err := os.ReadFile(filepath.Join(dir, "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(hb, &cfg); err != nil {
		t.Fatalf("hooks.json invalid: %v\n%s", err, hb)
	}
	if len(cfg.Hooks["Stop"]) != 1 || len(cfg.Hooks["Stop"][0].Hooks) != 1 {
		t.Fatalf("want exactly one Stop handler, got %s", hb)
	}
	h := cfg.Hooks["Stop"][0].Hooks[0]
	if h.Type != "command" || h.Command != "/nix/store/c/bin/clown idle-notify /run/user/1001/clown/idle/k.sock" || h.Timeout <= 0 {
		t.Fatalf("handler = %+v", h)
	}
}
