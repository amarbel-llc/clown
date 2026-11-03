package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/alfa/agentscope"
)

// The tokens in this file are fakes. None is, or stands for, a real credential.
const fakeModelToken = "clown-test-bearer-not-real"

func writeFakeTokenFile(t *testing.T, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "anthropic-oauth")
	if err := os.WriteFile(p, []byte(fakeModelToken+"\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestModelTokenPathConventionAndOverride(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1001")
	t.Setenv("CLOWN_MODEL_TOKEN_FILE", "")
	if got, want := modelTokenPath(), "/run/user/1001/clown/credentials/anthropic-oauth"; got != want {
		t.Errorf("modelTokenPath() = %q, want %q", got, want)
	}
	t.Setenv("CLOWN_MODEL_TOKEN_FILE", "/elsewhere/token")
	if got := modelTokenPath(); got != "/elsewhere/token" {
		t.Errorf("override: modelTokenPath() = %q", got)
	}
}

func TestChooseModelCredential(t *testing.T) {
	good := writeFakeTokenFile(t, 0o600)
	absent := filepath.Join(t.TempDir(), "absent")
	loose := writeFakeTokenFile(t, 0o644)
	for _, tc := range []struct {
		name, path, policy string
		want               string
		wantErr            []string
		wantWarn           []string
	}{
		{name: "token and default policy", path: good, policy: "", want: agentscope.ModelCredentialProxy},
		{name: "token and proxy-or-login", path: good, policy: "proxy-or-login", want: agentscope.ModelCredentialProxy},
		{name: "token and proxy-only", path: good, policy: "proxy-only", want: agentscope.ModelCredentialProxy},
		{
			name: "no token falls back by default", path: absent, policy: "", want: agentscope.ModelCredentialLoginFile,
			wantWarn: []string{"no model credential at " + absent, "binding the operator login read-only", "fallback"},
		},
		{
			name: "a loose token falls back and says why", path: loose, policy: "proxy-or-login", want: agentscope.ModelCredentialLoginFile,
			wantWarn: []string{loose, "0644", "fallback"},
		},
		{
			name: "proxy-only refuses a missing token", path: absent, policy: "proxy-only",
			wantErr: []string{absent, "piggy secrets reconcile", "CLOWN_MODEL_CREDENTIAL"},
		},
		{name: "proxy-only refuses a loose token", path: loose, policy: "proxy-only", wantErr: []string{loose, "0644", "piggy secrets reconcile"}},
		{name: "an unknown policy is refused", path: good, policy: "sometimes", wantErr: []string{"CLOWN_MODEL_CREDENTIAL", "sometimes", "proxy-only"}},
	} {
		var warn bytes.Buffer
		got, err := chooseModelCredential(tc.path, tc.policy, &warn)
		if len(tc.wantErr) > 0 {
			if err == nil {
				t.Errorf("%s: want a refusal, got %q", tc.name, got)
				continue
			}
			for _, w := range tc.wantErr {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("%s: error %q lacks %q", tc.name, err, w)
				}
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.name, got, err, tc.want)
		}
		if len(tc.wantWarn) == 0 && warn.Len() != 0 {
			t.Errorf("%s: unexpected stderr %q", tc.name, warn.String())
		}
		if n := strings.Count(warn.String(), "\n"); len(tc.wantWarn) > 0 && n != 1 {
			t.Errorf("%s: want exactly one stderr line, got %d: %q", tc.name, n, warn.String())
		}
		for _, w := range tc.wantWarn {
			if !strings.Contains(warn.String(), w) {
				t.Errorf("%s: stderr %q lacks %q", tc.name, warn.String(), w)
			}
		}
		if strings.Contains(warn.String(), fakeModelToken) {
			t.Errorf("%s: stderr leaks the token", tc.name)
		}
	}
}

func TestStageAgentHomeForLoginBind(t *testing.T) {
	opHome, agentHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	creds := filepath.Join(opHome, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(creds), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(creds, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(agentHome, ".claude", ".credentials.json")
	spec := agentscope.Spec{OperatorHome: opHome}

	p, err := stageAgentHomeFor(spec, agentHome, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range append(slices.Clone(p.rw), p.ro...) {
		if strings.Contains(b, creds) {
			t.Errorf("the operator login is bound with the proxy on: %q", b)
		}
	}
	if fi, err := os.Stat(dst); err != nil || fi.Size() != 0 {
		t.Errorf("the in-unit credentials file should be the empty seed: %v %v", fi, err)
	}

	p, err = stageAgentHomeFor(spec, agentHome, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.ro, creds+":"+dst) {
		t.Errorf("fallback: read-only binds %v lack the login", p.ro)
	}
}

// modelLaunchFixture is a spec, a worktree and a fake systemctl for driving
// prepareAgentScope and startFrontendEndpoints without a unit.
type modelLaunchFixture struct {
	spec    string
	runtime string
	opHome  string
	logs    *bytes.Buffer
	logger  *slog.Logger
}

func newModelLaunchFixture(t *testing.T, egressAllow []string) modelLaunchFixture {
	t.Helper()
	root := t.TempDir()
	// A short runtime dir: the unit's sockets live under it and a unix socket
	// path is limited to 107 bytes, which a test's own temp dir can exceed.
	runtime, err := os.MkdirTemp("", "cm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtime) })
	wt, opHome := filepath.Join(root, "wt"), filepath.Join(root, "home")
	for _, d := range []string{wt, opHome, runtime} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	creds := filepath.Join(opHome, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(creds), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(creds, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	systemctl := filepath.Join(root, "systemctl")
	if err := os.WriteFile(systemctl, []byte("#!/bin/sh\necho DBUS_SESSION_BUS_ADDRESS=x\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := map[string]any{
		"version": 1, "worktree": wt, "operator_home": opHome, "unit_name_hint": "model-test",
		"result_path": filepath.Join(root, "result.json"),
	}
	if egressAllow != nil {
		spec["egress_allow"] = egressAllow
	}
	raw, _ := json.Marshal(spec)
	specPath := filepath.Join(root, "spec.json")
	if err := os.WriteFile(specPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	if err := os.Chdir(wt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("CLOWN_SYSTEMCTL", systemctl)
	t.Setenv("CLOWN_DISABLE_STATSD", "1")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("CLOWN_MODEL_CREDENTIAL", "")
	t.Setenv("CLOWN_MODEL_UPSTREAM", "")
	logs := &bytes.Buffer{}
	return modelLaunchFixture{spec: specPath, runtime: runtime, opHome: opHome, logs: logs, logger: slog.New(slog.NewTextHandler(logs, nil))}
}

func (f modelLaunchFixture) prepare(t *testing.T) (agentscope.Launch, func()) {
	t.Helper()
	launch, err := prepareAgentScope(f.spec, "", "", []string{"claude"}, nil)
	if err != nil {
		t.Fatalf("prepareAgentScope: %v", err)
	}
	stop, err := startFrontendEndpoints(&launch, nil, nil, nil, f.logger)
	if err != nil {
		t.Fatalf("startFrontendEndpoints: %v", err)
	}
	t.Cleanup(stop)
	return launch, stop
}

func unitEnvOf(t *testing.T, l agentscope.Launch) (args []string, env map[string]string) {
	t.Helper()
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	env = map[string]string{}
	for _, a := range args {
		if kv, ok := strings.CutPrefix(a, "--setenv="); ok {
			k, v, _ := strings.Cut(kv, "=")
			env[k] = v
		}
	}
	return args, env
}

func TestLaunchWithATokenFileRunsTheProxyAndBindsNoLogin(t *testing.T) {
	f := newModelLaunchFixture(t, nil)
	t.Setenv("CLOWN_MODEL_TOKEN_FILE", writeFakeTokenFile(t, 0o600))
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "clown-test-bearer-inherited")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "clown-test-bearer-gateway")
	launch, _ := f.prepare(t)

	if launch.ModelCredential != agentscope.ModelCredentialProxy {
		t.Fatalf("ModelCredential = %q", launch.ModelCredential)
	}
	if _, err := os.Stat(launch.Model.Socket); err != nil || filepath.Base(launch.Model.Socket) != "model.sock" {
		t.Fatalf("model socket %q: %v", launch.Model.Socket, err)
	}
	if launch.Model.Listen != agentscope.ModelAddr() {
		t.Errorf("model relay listens on %q", launch.Model.Listen)
	}
	args, env := unitEnvOf(t, launch)
	if env["ANTHROPIC_BASE_URL"] != "http://"+agentscope.ModelAddr() || env["CLAUDE_CODE_OAUTH_TOKEN"] != agentscope.ModelPlaceholder {
		t.Errorf("unit env base=%q token=%q", env["ANTHROPIC_BASE_URL"], env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	if v, ok := env["ANTHROPIC_API_KEY"]; !ok || v != "" {
		t.Errorf("ANTHROPIC_API_KEY = %q (present %v), want empty", v, ok)
	}
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Error("ANTHROPIC_AUTH_TOKEN reached the unit")
	}
	for _, a := range args {
		if strings.Contains(a, "clown-test-bearer") || strings.Contains(a, ".credentials.json:") || strings.Contains(a, fakeModelToken) {
			t.Errorf("the unit's argv carries a credential or the login bind: %q", a)
		}
	}
	if !slices.Contains(args, "--property=BindPaths="+launch.Model.Socket) {
		t.Errorf("model socket not bound in: %v", args)
	}
	if !strings.Contains(f.logs.String(), "egress allowlist") || strings.Contains(f.logs.String(), "api.anthropic.com") {
		t.Errorf("with the proxy on, the default egress drops the model API hosts:\n%s", f.logs.String())
	}
}

func TestLaunchWithASpecEgressListLeavesItAlone(t *testing.T) {
	f := newModelLaunchFixture(t, []string{"api.anthropic.com:443", "github.com:443"})
	t.Setenv("CLOWN_MODEL_TOKEN_FILE", writeFakeTokenFile(t, 0o600))
	f.prepare(t)
	if !strings.Contains(f.logs.String(), "source=spec") || !strings.Contains(f.logs.String(), "api.anthropic.com:443") {
		t.Errorf("the sweatfile's list is the whole list, even with the proxy on:\n%s", f.logs.String())
	}
}

func TestLaunchWithoutATokenFileFallsBackToTheLoginBind(t *testing.T) {
	f := newModelLaunchFixture(t, nil)
	t.Setenv("CLOWN_MODEL_TOKEN_FILE", filepath.Join(t.TempDir(), "absent"))
	launch, _ := f.prepare(t)

	if launch.ModelCredential != agentscope.ModelCredentialLoginFile || launch.Model.Socket != "" {
		t.Fatalf("ModelCredential %q model socket %q; want login-file and no proxy", launch.ModelCredential, launch.Model.Socket)
	}
	args, env := unitEnvOf(t, launch)
	creds := filepath.Join(f.opHome, ".claude", ".credentials.json")
	if !slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, creds+":") }) {
		t.Errorf("fallback should bind the operator login read-only: %v", args)
	}
	if strings.Contains(env["ANTHROPIC_BASE_URL"], agentscope.ModelAddr()) {
		t.Errorf("no proxy, but base URL %q", env["ANTHROPIC_BASE_URL"])
	}
	if !strings.Contains(f.logs.String(), "api.anthropic.com:443") {
		t.Errorf("the fallback keeps the default egress, API hosts included:\n%s", f.logs.String())
	}
}

func TestProxyOnlyRefusesTheLaunchBeforeTheUnitAndRecordsIt(t *testing.T) {
	f := newModelLaunchFixture(t, nil)
	absent := filepath.Join(t.TempDir(), "absent")
	t.Setenv("CLOWN_MODEL_TOKEN_FILE", absent)
	t.Setenv("CLOWN_MODEL_CREDENTIAL", "proxy-only")
	launch, err := prepareAgentScope(f.spec, "", "", []string{"claude"}, nil)
	if err == nil || !strings.Contains(err.Error(), absent) || !strings.Contains(err.Error(), "piggy secrets reconcile") {
		t.Fatalf("prepareAgentScope = %v, want a refusal naming the path and piggy secrets reconcile", err)
	}
	_ = launch.Fail(err)
	raw, rerr := os.ReadFile(filepath.Join(filepath.Dir(f.spec), "result.json"))
	if rerr != nil || !strings.Contains(string(raw), "piggy secrets reconcile") {
		t.Errorf("the result file should carry the refusal: %s %v", raw, rerr)
	}
}

// A gateway or juggler-remote profile routes the model API itself; the proxy
// would send its traffic to the wrong place, so it stays out of the way.
func TestAGatewayBaseURLKeepsTheProxyOff(t *testing.T) {
	f := newModelLaunchFixture(t, nil)
	t.Setenv("CLOWN_MODEL_TOKEN_FILE", writeFakeTokenFile(t, 0o600))
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.example/v1")
	launch, _ := f.prepare(t)
	if launch.Model.Socket != "" || launch.ModelCredential != "" {
		t.Errorf("gateway launch: ModelCredential %q model socket %q; want neither", launch.ModelCredential, launch.Model.Socket)
	}
}
