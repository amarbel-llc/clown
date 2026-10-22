package agentscope

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func validSpec() Spec {
	return Spec{
		Version:      1,
		Worktree:     "/home/op/repos/x/.worktrees/w",
		OperatorHome: "/home/op",
		UnitNameHint: "spinclass-agent-x-w",
		Sockets:      []Socket{{Path: "/home/op/.local/state/ssh/agent.sock", Purpose: "piggy-agent"}},
		DenyPaths:    []string{"/run/user/1001/bus"},
		Env:          map[string]string{"SPINCLASS_SESSION": "x/w"},
	}
}

func TestValidateRefusesExposingTheOperatorHome(t *testing.T) {
	for _, bad := range []string{"/home/op", "/home", "/"} {
		s := validSpec()
		s.BindsRW = []string{bad}
		if err := s.Validate(); err == nil {
			t.Errorf("binds_rw %q: want refusal", bad)
		}
	}
	s := validSpec()
	s.Worktree = "/home/op"
	if err := s.Validate(); err == nil {
		t.Error("worktree == operator_home: want refusal")
	}
}

func TestValidateRefusesUnbindablePathsAndScrubbedEnv(t *testing.T) {
	cases := map[string]func(*Spec){
		"relative worktree":  func(s *Spec) { s.Worktree = "repos/x" },
		"colon in bind":      func(s *Spec) { s.BindsRO = []string{"/nix/a:b"} },
		"space in socket":    func(s *Spec) { s.Sockets = []Socket{{Path: "/run/a b"}} },
		"unclean path":       func(s *Spec) { s.BindsRO = []string{"/nix/../etc"} },
		"spec sets DBUS":     func(s *Spec) { s.Env = map[string]string{"DBUS_SESSION_BUS_ADDRESS": "x"} },
		"spec sets HOME":     func(s *Spec) { s.Env = map[string]string{"HOME": "/home/op"} },
		"unknown version":    func(s *Spec) { s.Version = 2 },
		"relative result":    func(s *Spec) { s.ResultPath = "agent-unit.json" },
		"result in worktree": func(s *Spec) { s.ResultPath = s.Worktree + "/.spinclass/agent-unit.json" },
		"relative deny path": func(s *Spec) { s.DenyPaths = []string{"bus"} },
	}
	for name, mutate := range cases {
		s := validSpec()
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: want refusal", name)
		}
	}
	if err := validSpec().Validate(); err != nil {
		t.Fatalf("valid spec refused: %v", err)
	}
}

func TestLoadSpecRefusesUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spec.json")
	raw, _ := json.Marshal(map[string]any{"version": 1, "worktree": "/w", "operator_home": "/home/op", "network": "host"})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSpec(path); err == nil || !strings.Contains(err.Error(), "network") {
		t.Fatalf("want unknown-field refusal naming it, got %v", err)
	}
}

func TestUnitNameSanitizesAndIsUnique(t *testing.T) {
	a, b := UnitName("spinclass-agent-clown/cool fir"), UnitName("spinclass-agent-clown/cool fir")
	if a == b {
		t.Fatalf("two names collided: %s", a)
	}
	if !strings.HasPrefix(a, "spinclass-agent-clown-cool-fir-") || !strings.HasSuffix(a, ".service") {
		t.Fatalf("unexpected name %q", a)
	}
	if n := UnitName(""); !strings.HasPrefix(n, "clown-agent-") {
		t.Fatalf("empty hint: %q", n)
	}
}

func testLaunch() Launch {
	return Launch{
		Spec:      validSpec(),
		Unit:      "u-1.service",
		WorkDir:   "/home/op/repos/x/.worktrees/w/sub",
		AgentHome: "/home/op/repos/x/.worktrees/w/.tmp/agent-home",
		FrontendEnv: []string{
			"PATH=/bin", "TERM=xterm", "DISPLAY=:0", "WAYLAND_DISPLAY=wayland-1",
			"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1001/bus", "HOME=/home/op",
			"CLOWN_SESSION_ID=k", "SECRET_TOKEN=s", "LC_ALL=C", "ANTHROPIC_BASE_URL=u",
			"SSH_AUTH_SOCK=/home/op/.local/state/ssh/agent.sock", "TMPDIR=/tmp",
		},
		ManagerEnvKeys: []string{"PATH", "DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR", "GTK_PATH"},
		Shim:           []string{"/nix/store/c/bin/clown", "agent-exec"},
		Argv:           []string{"/nix/store/cl/bin/claude", "--session-id", "$x"},
		EgressSocket:   "/run/user/1001/clown/agent/u-1/egress.sock",
		Forwards:       []Relay{{Listen: "127.0.0.1:5222", Socket: "/run/user/1001/clown/agent/u-1/fwd-5222.sock"}},
	}
}

func TestSystemdRunArgsRefusesAUnitWithoutItsProxy(t *testing.T) {
	l := testLaunch()
	l.EgressSocket = ""
	if _, err := l.SystemdRunArgs(); err == nil {
		t.Fatal("want refusal")
	}
}

func TestSystemdRunArgsCarryTheRequiredSandbox(t *testing.T) {
	args, err := testLaunch().SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--user", "--wait", "--pipe", "--collect", "--expand-environment=no", "--slice=clown-agent.slice", "--unit=u-1.service",
		"--property=PrivateUsers=self", "--property=PrivatePIDs=yes", "--property=ProtectHome=tmpfs",
		"--property=TemporaryFileSystem=/dev/pts", "--property=PrivateTmp=yes", "--property=ProtectSystem=strict",
		"--property=NoNewPrivileges=yes", "--property=WorkingDirectory=/home/op/repos/x/.worktrees/w/sub",
		"--property=BindPaths=/home/op/repos/x/.worktrees/w",
		"--property=BindPaths=/home/op/.local/state/ssh/agent.sock",
		"--property=InaccessiblePaths=-/run/user/1001/bus",
		"--property=PrivateNetwork=yes",
		"--property=BindPaths=/run/user/1001/clown/agent/u-1/egress.sock",
		"--property=BindPaths=/run/user/1001/clown/agent/u-1/fwd-5222.sock",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
	sep := slices.Index(args, "--")
	if sep < 0 || !slices.Equal(args[sep+1:], []string{
		"/nix/store/c/bin/clown", "agent-exec",
		"--relay", "127.0.0.1:3128=/run/user/1001/clown/agent/u-1/egress.sock",
		"--relay", "127.0.0.1:5222=/run/user/1001/clown/agent/u-1/fwd-5222.sock", "--",
		"/nix/store/cl/bin/claude", "--session-id", "$x",
	}) {
		t.Fatalf("tail after -- = %v", args[sep+1:])
	}
}

func TestEnvironmentAllowlistsAndUnsetsManagerKeys(t *testing.T) {
	set, unset := testLaunch().environment()
	env := map[string]string{}
	for _, kv := range set {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	for k, want := range map[string]string{
		"PATH": "/bin", "TERM": "xterm", "LC_ALL": "C", "ANTHROPIC_BASE_URL": "u",
		"SPINCLASS_SESSION": "x/w", "HOME": "/home/op/repos/x/.worktrees/w/.tmp/agent-home",
		"SSH_AUTH_SOCK": "/home/op/.local/state/ssh/agent.sock",
		"HTTPS_PROXY":   "http://127.0.0.1:3128", "https_proxy": "http://127.0.0.1:3128",
		"XDG_STATE_HOME":  "/home/op/.local/state",
		"GIT_SSH_COMMAND": `ssh -F none -o ProxyCommand="/nix/store/c/bin/clown egress-connect /run/user/1001/clown/agent/u-1/egress.sock %h %p"`,
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if env["TMPDIR"] != "/home/op/repos/x/.worktrees/w/.tmp" {
		t.Errorf("TMPDIR outside the worktree not redirected: %q", env["TMPDIR"])
	}
	for _, k := range []string{"DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS", "CLOWN_SESSION_ID", "SECRET_TOKEN"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s leaked into the unit", k)
		}
	}
	if !slices.Equal(unset, []string{"DBUS_SESSION_BUS_ADDRESS", "GTK_PATH", "XDG_RUNTIME_DIR"}) {
		t.Errorf("unset = %v", unset)
	}
}

// CLOWN_UNIT is the in-unit marker spinclass's hook reads to tell it is
// confined (FDR 0035): the unit's own name, present only inside a unit, and not
// settable by an operator export, the extra env or the spec.
func TestClownUnitNamesTheUnitAndCannotBeSetFromOutside(t *testing.T) {
	l := testLaunch()
	l.FrontendEnv = append(l.FrontendEnv, "CLOWN_UNIT=operator-export.service")
	l.ExtraEnv = []string{"CLOWN_UNIT=extra.service"}
	set, _ := l.environment()
	var got []string
	for _, kv := range set {
		if strings.HasPrefix(kv, "CLOWN_UNIT=") {
			got = append(got, kv)
		}
	}
	if !slices.Equal(got, []string{"CLOWN_UNIT=u-1.service"}) {
		t.Errorf("CLOWN_UNIT entries = %v, want exactly the launch's unit name", got)
	}
	s := validSpec()
	s.Env = map[string]string{"CLOWN_UNIT": "spec.service"}
	if err := s.Validate(); err == nil {
		t.Error("spec env setting CLOWN_UNIT: want refusal")
	}
}

// The unit's CLOWN_BIN is the binary the frontend runs the unit with (the
// shim), never an inherited one: round R saw in-unit producers on an older
// clown than the unit's own because the frontend env carried a stray CLOWN_BIN.
func TestClownBinIsTheShimWhateverWasInherited(t *testing.T) {
	for name, mutate := range map[string]func(*Launch){
		"none inherited": func(*Launch) {},
		"inherited a different clown": func(l *Launch) {
			l.FrontendEnv = append(l.FrontendEnv, "CLOWN_BIN=/nix/store/old/bin/clown")
		},
		"extra env and spec set one": func(l *Launch) {
			l.FrontendEnv = append(l.FrontendEnv, "CLOWN_BIN=/nix/store/old/bin/clown")
			l.ExtraEnv = []string{"CLOWN_BIN=/nix/store/extra/bin/clown"}
			l.Spec.Env = map[string]string{"CLOWN_BIN": "/nix/store/spec/bin/clown"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			l := testLaunch()
			mutate(&l)
			set, _ := l.environment()
			var got []string
			for _, kv := range set {
				if strings.HasPrefix(kv, "CLOWN_BIN=") {
					got = append(got, kv)
				}
			}
			if !slices.Equal(got, []string{"CLOWN_BIN=" + l.Shim[0]}) {
				t.Errorf("CLOWN_BIN entries = %v, want exactly the shim %s", got, l.Shim[0])
			}
		})
	}
}

// clown#272: NIX_CONFIG carries access-tokens on the dev hosts. No nix
// configuration key may reach the unit, whatever path it comes by.
func TestNixConfigurationNeverReachesTheUnit(t *testing.T) {
	l := testLaunch()
	l.FrontendEnv = append(l.FrontendEnv,
		"NIX_CONFIG=experimental-features = nix-command flakes\naccess-tokens = github.com=ghp_secret",
		"NIX_USER_CONF_FILES=/home/op/.config/nix/nix.conf",
		"NIX_CONF_DIR=/home/op/.config/nix",
		"NIX_PATH=nixpkgs=/nix/store/p",
	)
	l.ExtraEnv = []string{"NIX_CONFIG=access-tokens = github.com=ghp_secret"}
	set, _ := l.environment()
	for _, kv := range set {
		if strings.Contains(kv, "access-tokens") || strings.Contains(kv, "ghp_secret") {
			t.Errorf("token-bearing entry reached the unit: %q", kv)
		}
		for _, k := range []string{"NIX_CONFIG=", "NIX_USER_CONF_FILES=", "NIX_CONF_DIR="} {
			if strings.HasPrefix(kv, k) {
				t.Errorf("%s passed into the unit: %q", strings.TrimSuffix(k, "="), kv)
			}
		}
	}
	if !slices.Contains(set, "NIX_PATH=nixpkgs=/nix/store/p") {
		t.Errorf("NIX_PATH no longer passed: %v", set)
	}
	s := validSpec()
	s.Env = map[string]string{"NIX_CONFIG": "access-tokens = github.com=x"}
	if err := s.Validate(); err == nil {
		t.Error("spec env setting NIX_CONFIG: want refusal")
	}
}

func TestValidateRefusesAWritableBindOverTheOperatorsClaudeCredential(t *testing.T) {
	for _, bad := range []string{"/home/op/.claude", "/home/op/.claude/.credentials.json"} {
		s := validSpec()
		s.BindsRW = []string{bad}
		if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "credential") {
			t.Errorf("binds_rw %q: want a refusal naming the credential, got %v", bad, err)
		}
	}
	s := validSpec()
	s.BindsRW = []string{"/home/op/.claude/projects/x"}
	if err := s.Validate(); err != nil {
		t.Errorf("a transcript dir bind was refused: %v", err)
	}
}

func TestValidateRefusesBindingTheOperatorsNixConfig(t *testing.T) {
	for _, bad := range []string{"/home/op/.config", "/home/op/.config/nix", "/home/op/.config/nix/nix.conf"} {
		s := validSpec()
		s.BindsRO = []string{bad}
		if err := s.Validate(); err == nil {
			t.Errorf("binds_ro %q: want refusal", bad)
		}
	}
	s := validSpec()
	s.BindsRO = []string{"/home/op/.config/moxy"}
	if err := s.Validate(); err != nil {
		t.Errorf("unrelated ~/.config bind refused: %v", err)
	}
}

func TestSSHAuthSockOnlyWhenTheSocketIsBound(t *testing.T) {
	l := testLaunch()
	l.Spec.Sockets = nil
	set, _ := l.environment()
	for _, kv := range set {
		if strings.HasPrefix(kv, "SSH_AUTH_SOCK=") {
			t.Fatalf("SSH_AUTH_SOCK passed without the socket bound: %s", kv)
		}
	}
}

func TestSystemdRunArgsRefusesWorkDirOutsideWorktree(t *testing.T) {
	l := testLaunch()
	l.WorkDir = "/home/op"
	if _, err := l.SystemdRunArgs(); err == nil {
		t.Fatal("want refusal")
	}
}

// fakeProc writes /proc/<pid>/{cgroup,status} for a process under root.
func fakeProc(t *testing.T, root string, pid int, cgroup, nspid string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup"), []byte("0::"+cgroup+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte("Name:\tclaude\nNSpid:\t"+nspid+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	setCmdline(t, root, pid, "claude", "-p", "hi")
}

// setCmdline writes /proc/<pid>/cmdline under root.
func setCmdline(t *testing.T, root string, pid int, argv ...string) {
	t.Helper()
	raw := strings.Join(argv, "\x00") + "\x00"
	if err := os.WriteFile(filepath.Join(root, strconv.Itoa(pid), "cmdline"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWaitActiveWaitsForTheProviderExec: while pid 1 is still the exec shim
// (its relays and in-unit servers starting, any of which may refuse), the
// launch has not succeeded; it has once the shim exec'd the provider.
func TestWaitActiveWaitsForTheProviderExec(t *testing.T) {
	const unit = "u-1.service"
	cg := "/user.slice/user-1001.slice/user@1001.service/clown.slice/" + Slice + "/" + unit
	root := t.TempDir()
	fakeProc(t, root, 100, cg, "100 1")
	setCmdline(t, root, 100, "/nix/store/x-clown/bin/clown", ShimVerb, "--relay", "a=b", "--", "claude")
	polls := 0
	tools := Tools{procRoot: root, show: func(string) (Status, error) {
		polls++
		if polls == 3 {
			setCmdline(t, root, 100, "claude", "-p", "hi")
		}
		return Status{ActiveState: "active", ControlGroup: cg, MainPID: 100}, nil
	}}
	if _, err := tools.waitActive(context.Background(), unit); err != nil {
		t.Fatal(err)
	}
	if polls < 3 {
		t.Fatalf("reported started after %d polls, while pid 1 was still the shim", polls)
	}

	setCmdline(t, root, 100, "/nix/store/x-clown/bin/clown", ShimVerb, "--", "claude")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := (Tools{procRoot: root, show: func(string) (Status, error) {
		return Status{ActiveState: "active", ControlGroup: cg, MainPID: 100}, nil
	}}).waitActive(ctx, unit); err == nil {
		t.Fatal("a unit whose pid 1 never left the shim was reported started")
	}
}

// TestProviderStartedWhenTheInitRenamedItself: as pid 1 the shim keeps its
// argv and runs the provider as a child; its InitComm name is the signal.
func TestProviderStartedWhenTheInitRenamedItself(t *testing.T) {
	root := t.TempDir()
	fakeProc(t, root, 100, "/x", "100 1")
	setCmdline(t, root, 100, "/nix/store/x-clown/bin/clown", ShimVerb, "--", "claude")
	tools := Tools{procRoot: root}
	if tools.providerStarted(100) {
		t.Fatal("the shim was reported started before it renamed itself")
	}
	if err := os.WriteFile(filepath.Join(root, "100", "comm"), []byte(InitComm+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !tools.providerStarted(100) {
		t.Fatal("the shim renamed itself but was not reported started")
	}
}

// TestWaitActiveReportsAFailedUnitAsEnded: a provider that exits nonzero
// between two polls leaves the unit failed; Run classifies it by exit code,
// so waitActive must not call it a start failure.
func TestWaitActiveReportsAFailedUnitAsEnded(t *testing.T) {
	failed := func(result string) Tools {
		return Tools{procRoot: t.TempDir(), show: func(string) (Status, error) {
			return Status{ActiveState: "failed", SubState: "failed", Result: result}, nil
		}}
	}
	for _, r := range []string{"exit-code", "signal"} {
		if _, err := failed(r).waitActive(context.Background(), "u-1.service"); !errors.Is(err, errUnitEnded) {
			t.Errorf("result %s: err = %v, want errUnitEnded", r, err)
		}
	}
	// A unit that never ran its process (a bad exec, sandbox setup) is a
	// start failure, not an exit.
	for _, r := range []string{"exec", "resources", "protocol"} {
		_, err := failed(r).waitActive(context.Background(), "u-1.service")
		if err == nil || errors.Is(err, errUnitEnded) || !strings.Contains(err.Error(), "failed to start (result "+r+")") {
			t.Errorf("result %s: err = %v, want a start failure naming it", r, err)
		}
	}
}

func TestShimRefusedCodes(t *testing.T) {
	for code, want := range map[int]bool{0: false, 1: false, 2: true, 126: true, 127: true, 130: false} {
		if ShimRefused(code) != want {
			t.Errorf("ShimRefused(%d) = %v, want %v", code, !want, want)
		}
	}
}

// clown#273: right after a PrivatePIDs unit turns active, systemd names the
// intermediate process (gone, or outside the namespace) as MainPID; the
// record must name the namespace init that stays in the unit's cgroup.
func TestWaitActiveRecordsTheMainPIDThatStaysInTheUnitCgroup(t *testing.T) {
	const unit = "u-1.service"
	cg := "/user.slice/user-1001.slice/user@1001.service/clown.slice/" + Slice + "/" + unit
	root := t.TempDir()
	fakeProc(t, root, 98, cg, "98")                    // intermediate, still alive
	fakeProc(t, root, 99, "/user.slice/other", "99 1") // a pidns init elsewhere
	fakeProc(t, root, 100, cg, "100 1")                // the lasting main process
	seq := []int{97, 98, 99, 100}                      // 97: already exited, no /proc entry
	calls := 0
	tools := Tools{procRoot: root, show: func(string) (Status, error) {
		pid := seq[min(calls, len(seq)-1)]
		calls++
		return Status{ActiveState: "active", ControlGroup: cg, MainPID: pid}, nil
	}}
	st, err := tools.waitActive(context.Background(), unit)
	if err != nil {
		t.Fatal(err)
	}
	if st.MainPID != 100 {
		t.Fatalf("recorded MainPID %d, want 100", st.MainPID)
	}
	got, err := procCgroupAt(root, st.MainPID)
	if err != nil || got != cg {
		t.Fatalf("recorded pid's cgroup = %q (%v), want the unit's %q", got, err, cg)
	}
}

func TestWaitActiveGivesUpWhenNoMainPIDSettles(t *testing.T) {
	const unit = "u-1.service"
	root := t.TempDir()
	tools := Tools{procRoot: root, show: func(string) (Status, error) {
		return Status{ActiveState: "active", ControlGroup: "/x/" + Slice + "/" + unit, MainPID: 97}, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := tools.waitActive(ctx, unit); err == nil {
		t.Fatal("want an error when the main pid never settles")
	}
}

func TestWriteResultIsAtomicJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".spinclass", "agent-unit.json")
	if err := WriteResult(path, Result{Unit: "u.service", Cgroup: "/c", UID: 1001, Pty: "/dev/pts/3", MainPID: 7}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r Result
	if err := json.Unmarshal(raw, &r); err != nil || r.Version != 1 || r.Unit != "u.service" || r.MainPID != 7 {
		t.Fatalf("result = %+v (%v)", r, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}
