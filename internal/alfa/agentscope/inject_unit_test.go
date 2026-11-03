package agentscope

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// proxyLaunch is testLaunch with the model injection proxy on and real-looking
// (fake) credentials in every place the old pass-through let one in.
func proxyLaunch() Launch {
	l := testLaunch()
	l.EgressSocket = "/run/user/1001/clown/agent/u-1/egress.sock"
	l.Model = Relay{Listen: ModelAddr(), Socket: "/run/user/1001/clown/agent/u-1/model.sock"}
	l.ModelCredential = ModelCredentialProxy
	l.FrontendEnv = append(l.FrontendEnv,
		"ANTHROPIC_AUTH_TOKEN=clown-test-bearer-auth",
		"CLAUDE_CODE_OAUTH_TOKEN=clown-test-bearer-oauth",
		"CLAUDE_CODE_OAUTH_REFRESH_TOKEN=clown-test-bearer-refresh",
		"ANTHROPIC_API_KEY=clown-test-bearer-key",
		"ANTHROPIC_MODEL=some-model",
	)
	l.ExtraEnv = append(l.ExtraEnv, "ANTHROPIC_API_KEY=clown-test-bearer-extra")
	l.Spec.Env = map[string]string{"ANTHROPIC_AUTH_TOKEN": "clown-test-bearer-spec", "ANTHROPIC_BASE_URL": "https://evil.test"}
	return l
}

func TestModelProxyUnitEnvironmentCarriesOnlyThePlaceholder(t *testing.T) {
	l := proxyLaunch()
	set, _ := l.environment()
	env := unitEnv(l)
	if got, want := env["ANTHROPIC_BASE_URL"], "http://"+ModelAddr(); got != want {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want %q (the spec and the frontend may not override it)", got, want)
	}
	if got := env["CLAUDE_CODE_OAUTH_TOKEN"]; got != ModelPlaceholder {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN = %q, want the placeholder", got)
	}
	if v, ok := env["ANTHROPIC_API_KEY"]; !ok || v != "" {
		t.Errorf("ANTHROPIC_API_KEY = %q (present: %v), want present and empty", v, ok)
	}
	for _, k := range []string{"ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_REFRESH_TOKEN"} {
		if v, ok := env[k]; ok {
			t.Errorf("%s = %q reached the unit", k, v)
		}
	}
	if env["ANTHROPIC_MODEL"] != "some-model" {
		t.Errorf("other ANTHROPIC_ keys must still pass: ANTHROPIC_MODEL = %q", env["ANTHROPIC_MODEL"])
	}
	for _, kv := range set {
		if strings.Contains(kv, "clown-test-bearer") || strings.Contains(kv, "evil.test") {
			t.Errorf("a credential or override survived: %q", kv)
		}
	}
	if slices.Index(set, "ANTHROPIC_API_KEY=") < 0 {
		t.Errorf("the empty ANTHROPIC_API_KEY entry is missing from %v", set)
	}
	// Loopback stays out of the proxy relay's way.
	if !strings.Contains(env["NO_PROXY"], "127.0.0.1") {
		t.Errorf("NO_PROXY = %q must keep loopback direct", env["NO_PROXY"])
	}
}

func TestWithoutTheProxyTheCredentialEnvironmentIsAsBefore(t *testing.T) {
	l := proxyLaunch()
	l.Model, l.ModelCredential = Relay{}, ModelCredentialLoginFile
	env := unitEnv(l)
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "clown-test-bearer-oauth" || env["ANTHROPIC_AUTH_TOKEN"] != "clown-test-bearer-spec" {
		t.Errorf("fallback launches keep the pass-through (FDR 0020): %v", env)
	}
	if strings.Contains(env["ANTHROPIC_BASE_URL"], ModelAddr()) {
		t.Errorf("no proxy, but ANTHROPIC_BASE_URL = %q", env["ANTHROPIC_BASE_URL"])
	}
}

func TestModelProxySocketIsBoundAndRelayed(t *testing.T) {
	l := proxyLaunch()
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "--property=BindPaths="+l.Model.Socket) {
		t.Errorf("the model socket is not bound in: %v", args)
	}
	if j := slices.Index(args, ModelAddr()+"="+l.Model.Socket); j < 1 || args[j-1] != "--relay" {
		t.Errorf("no --relay for the model socket in %v", args)
	}
	if ModelPort == ServePortBase || ModelPort == BrokerPort || ModelPort >= InUnitPortBase || ModelPort == RelayPort {
		t.Errorf("ModelPort %d collides with another reserved port", ModelPort)
	}
}

func TestModelCredentialIsReportedInTheResultAndCapabilities(t *testing.T) {
	if !slices.Contains(Capabilities, "model-inject-proxy") {
		t.Errorf("Capabilities %v lack model-inject-proxy", Capabilities)
	}
	for _, mode := range []string{ModelCredentialProxy, ModelCredentialLoginFile} {
		l := testLaunch()
		l.ModelCredential = mode
		path := filepath.Join(t.TempDir(), "r.json")
		if err := WriteResult(path, l.baseResult()); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(path)
		var got struct {
			ModelCredential string `json:"model_credential"`
			Version         int    `json:"version"`
		}
		if err := json.Unmarshal(raw, &got); err != nil || got.ModelCredential != mode || got.Version != SpecVersion {
			t.Errorf("result %s: %+v, %v; want model_credential %q at spec version %d", raw, got, err, mode, SpecVersion)
		}
	}
	// A refusal records which path the launch was on, too.
	l := testLaunch()
	l.Spec.ResultPath = filepath.Join(t.TempDir(), "fail.json")
	l.ModelCredential = ModelCredentialLoginFile
	_ = l.Fail(os.ErrInvalid)
	raw, _ := os.ReadFile(l.Spec.ResultPath)
	if !strings.Contains(string(raw), `"model_credential": "login-file"`) {
		t.Errorf("failure result lacks the credential path: %s", raw)
	}
}

func TestWithoutModelAPIHostsDropsTheAPIAndLoginHostsOnly(t *testing.T) {
	got := WithoutModelAPIHosts(DefaultEgressAllow)
	for _, dropped := range []string{"api.anthropic.com:443", "console.anthropic.com:443", "platform.claude.com:443", "claude.ai:443"} {
		if slices.Contains(got, dropped) {
			t.Errorf("%s survived", dropped)
		}
	}
	for _, kept := range []string{"mcp-proxy.anthropic.com:443", "github.com:443", "cache.nixos.org:443"} {
		if !slices.Contains(got, kept) {
			t.Errorf("%s was dropped", kept)
		}
	}
	if !slices.Contains(DefaultEgressAllow, "api.anthropic.com:443") {
		t.Error("the shared default list was mutated")
	}
}
