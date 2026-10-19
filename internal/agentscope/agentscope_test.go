package agentscope

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
	} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
	sep := slices.Index(args, "--")
	if sep < 0 || !slices.Equal(args[sep+1:], []string{
		"/nix/store/c/bin/clown", "agent-exec",
		"--egress-relay", "127.0.0.1:3128=/run/user/1001/clown/agent/u-1/egress.sock", "--",
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
