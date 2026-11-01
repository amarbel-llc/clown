package main

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"code.linenisgreat.com/clown/internal/alfa/agentscope"
	"code.linenisgreat.com/clown/internal/alfa/pluginhost"
)

// shortRuntimeDir is a runtime dir short enough for a unix socket path
// beneath it (t.TempDir carries the test name and can exceed the limit).
func shortRuntimeDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "b")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func brokerDiscovered(bridge string) []pluginhost.DiscoveredServer {
	bridged := func(plugin, server, scope string, env map[string]string) pluginhost.DiscoveredServer {
		return pluginhost.DiscoveredServer{PluginName: plugin, ServerName: server, Def: pluginhost.ServerDef{
			Command: bridge, Args: []string{"--command", "/bin/" + server, "--", "serve"}, Scope: scope, Env: env,
		}}
	}
	return []pluginhost.DiscoveredServer{
		bridged("spinclass", "spinclass", pluginhost.ScopeFrontend, map[string]string{"KEEP": "1"}),
		bridged("clown-builtin-jobs", "ringmaster", pluginhost.ScopeFrontend, nil),
		bridged("moxy", "moxy", "", nil),
	}
}

// clown#333: the spinclass serve bridge is told, in its environment, where to
// listen; no other server is, and the caller's slice and env map are left alone.
func TestRoutePolicyBrokerNamesTheSocketForTheSpinclassServeOnly(t *testing.T) {
	const bridge = "/nix/store/b/bin/clown-stdio-bridge"
	runtime := shortRuntimeDir(t)
	discovered := brokerDiscovered(bridge)

	got, broker, err := routePolicyBroker(discovered, bridge, runtime, "p1")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(runtime, "clown", "serve", "p1", "broker.sock")
	if broker.Socket != want || broker.Server != "spinclass/spinclass" {
		t.Errorf("broker = %+v, want spinclass/spinclass at %s", broker, want)
	}
	if got[0].Def.Env[policyBrokerEnv] != want || got[0].Def.Env["KEEP"] != "1" {
		t.Errorf("spinclass env = %v", got[0].Def.Env)
	}
	for _, d := range got[1:] {
		if _, ok := d.Def.Env[policyBrokerEnv]; ok {
			t.Errorf("%s was told the broker", d.Name())
		}
	}
	if _, ok := discovered[0].Def.Env[policyBrokerEnv]; ok {
		t.Error("the caller's env map was mutated")
	}
	fi, err := os.Stat(filepath.Dir(want))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("session dir: %v, %v; want 0700 so serve can create the socket in it", fi, err)
	}
}

// A session without spinclass (plain clown, juggler) names and creates nothing.
func TestRoutePolicyBrokerWithoutSpinclassNamesNothing(t *testing.T) {
	const bridge = "/b"
	runtime := shortRuntimeDir(t)
	d := []pluginhost.DiscoveredServer{{PluginName: "moxy", ServerName: "moxy", Def: pluginhost.ServerDef{Command: bridge}}}
	got, broker, err := routePolicyBroker(d, bridge, runtime, "p1")
	if err != nil || broker != (policyBroker{}) || !slices.Equal(got[0].Def.Args, d[0].Def.Args) {
		t.Errorf("got %+v, %+v, %v", got, broker, err)
	}
	if _, err := os.Stat(filepath.Join(runtime, "clown")); err == nil {
		t.Error("a directory was created for a session with no spinclass")
	}
}

// With nowhere to put the socket the broker is skipped, not fatal: moxy then
// uses its static floor.
func TestRoutePolicyBrokerWithoutARuntimeDirSkipsTheBroker(t *testing.T) {
	const bridge = "/b"
	got, broker, err := routePolicyBroker(brokerDiscovered(bridge), bridge, "", "p1")
	if err == nil || broker != (policyBroker{}) {
		t.Errorf("err %v, broker %+v; want an error and no broker", err, broker)
	}
	if got != nil {
		t.Error("a failed route returned servers")
	}
}

// Cleanup removes the socket serve left behind and then the directory.
func TestPolicyBrokerCleanupRemovesTheSocketAndTheDir(t *testing.T) {
	const bridge = "/b"
	runtime := shortRuntimeDir(t)
	_, broker, err := routePolicyBroker(brokerDiscovered(bridge), bridge, runtime, "p1")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", broker.Socket)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	broker.cleanup()
	if _, err := os.Stat(filepath.Dir(broker.Socket)); err == nil {
		t.Error("the session dir survived the cleanup")
	}
	policyBroker{}.cleanup() // a zero broker is a no-op
}

// Confined, the relay is bound only when serve actually created the socket.
func TestPolicyBrokerSocketPresent(t *testing.T) {
	runtime := shortRuntimeDir(t)
	sock := filepath.Join(runtime, "s.sock")
	if brokerSocketPresent(sock) {
		t.Error("a missing socket reported present")
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if !brokerSocketPresent(sock) {
		t.Error("a listening socket reported missing")
	}
	plain := filepath.Join(runtime, "f")
	if err := os.WriteFile(plain, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if brokerSocketPresent(plain) {
		t.Error("a regular file reported as a socket")
	}
}

func TestApplyPolicyBrokerUnconfinedExportsThePath(t *testing.T) {
	flags := parsedFlags{policyBroker: policyBroker{Server: "spinclass/spinclass", Socket: "/run/user/1/clown/serve/p1/broker.sock"}}
	var bound Command
	applyPolicyBroker(&flags, &bound, true, nil)
	if !slices.Equal(bound.Env, []string{"CLOWN_POLICY_BROKER=/run/user/1/clown/serve/p1/broker.sock"}) {
		t.Errorf("env = %q", bound.Env)
	}
}

// Nothing is exported for a session with no broker, or when the caller says
// the broker's serve is not up.
func TestApplyPolicyBrokerExportsNothingWithoutAStartedBroker(t *testing.T) {
	var bound Command
	applyPolicyBroker(&parsedFlags{}, &bound, true, nil)
	applyPolicyBroker(&parsedFlags{policyBroker: policyBroker{Server: "spinclass/spinclass", Socket: "/s"}}, &bound, false, nil)
	if len(bound.Env) != 0 {
		t.Errorf("env = %q", bound.Env)
	}
}

// The broker is exported only when its serve started and the session still
// binds it (a cheap-context selection may drop it).
func TestBrokerStartedNeedsTheServerStartedAndKept(t *testing.T) {
	b := policyBroker{Server: "spinclass/spinclass", Socket: "/s"}
	srv := []*pluginhost.ManagedServer{{Name: "spinclass/spinclass"}}
	kept := []pluginhost.DiscoveredServer{{PluginName: "spinclass", ServerName: "spinclass"}}
	if !brokerStarted(b, srv, kept) {
		t.Error("started and kept: not started")
	}
	if brokerStarted(b, nil, kept) {
		t.Error("not started: reported started")
	}
	if brokerStarted(b, srv, nil) {
		t.Error("dropped by the selection: reported started")
	}
	if brokerStarted(policyBroker{}, srv, kept) {
		t.Error("no broker: reported started")
	}
}

// Confined: the agent and every in-unit server get the in-unit relay address,
// the relay row is recorded, and the host socket path is not exported.
func TestApplyPolicyBrokerConfinedExportsTheRelayAddress(t *testing.T) {
	runtime := shortRuntimeDir(t)
	sock := filepath.Join(runtime, "broker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	inUnit := []agentscope.InUnitServer{{Name: "moxy/moxy", Env: map[string]string{"CLOWN_BIN": "/c"}}, {Name: "x/x"}}
	flags := parsedFlags{
		agentScopeSpec:   "spec.json",
		policyBroker:     policyBroker{Server: "spinclass/spinclass", Socket: sock},
		agentScopeInUnit: inUnit,
	}
	var bound Command
	applyPolicyBroker(&flags, &bound, true, nil)
	const addr = "127.0.0.1:19001"
	if !slices.Equal(bound.Env, []string{"CLOWN_POLICY_BROKER=" + addr}) {
		t.Errorf("agent env = %q", bound.Env)
	}
	if flags.agentScopeBroker != (agentscope.Relay{Listen: addr, Socket: sock}) {
		t.Errorf("relay = %+v", flags.agentScopeBroker)
	}
	for _, s := range flags.agentScopeInUnit {
		if s.Env[policyBrokerEnv] != addr {
			t.Errorf("%s env = %v", s.Name, s.Env)
		}
	}
	if flags.agentScopeInUnit[0].Env["CLOWN_BIN"] != "/c" {
		t.Error("the server's own env was lost")
	}
	if _, ok := inUnit[0].Env[policyBrokerEnv]; ok {
		t.Error("the caller's in-unit env map was mutated")
	}
}

// Confined and serve never created the socket (an older spinclass): nothing is
// bound, relayed or exported, so the unit launches as it did before.
func TestApplyPolicyBrokerConfinedSkipsAMissingSocket(t *testing.T) {
	flags := parsedFlags{
		agentScopeSpec: "spec.json",
		policyBroker:   policyBroker{Server: "spinclass/spinclass", Socket: filepath.Join(shortRuntimeDir(t), "none.sock")},
	}
	var bound Command
	applyPolicyBroker(&flags, &bound, true, nil)
	if len(bound.Env) != 0 || flags.agentScopeBroker != (agentscope.Relay{}) {
		t.Errorf("env %q relay %+v", bound.Env, flags.agentScopeBroker)
	}
}
