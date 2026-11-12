package pluginhost

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const testBridgeToken = "tok-0123456789abcdef"

// With a token the host hands it to every bridge it desugars (the diagnostics
// plugin's included) and to nothing else; without one the env is untouched.
func TestDiscoverGivesEveryBridgeTheTokenOnlyWhenSet(t *testing.T) {
	const bridge = "/opt/clown/bin/clown-stdio-bridge"
	cg := writeBridgePlugin(t, "cutting-garden", "cg")
	diag := writeBridgePlugin(t, DiagnosticsPluginName, "disconnects")

	on := &Host{PluginDirs: []string{cg, diag}, BridgePath: bridge, BridgeToken: testBridgeToken}
	found, err := on.Discover()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cg", "disconnects"} {
		if got := serverNamed(t, found, name).Def.Env[EnvBridgeToken]; got != testBridgeToken {
			t.Errorf("bridge %s env token = %q, want the token", name, got)
		}
	}
	if got := serverNamed(t, found, "cg").Def.Env["KEEP"]; got != "me" {
		t.Errorf("the bridge's own env was lost: KEEP = %q", got)
	}
	if _, ok := serverNamed(t, found, "native").Def.Env[EnvBridgeToken]; ok {
		t.Error("a native http server was given the bridge token")
	}

	off := &Host{PluginDirs: []string{cg, diag}, BridgePath: bridge}
	found, err = off.Discover()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range found {
		if _, ok := d.Def.Env[EnvBridgeToken]; ok {
			t.Errorf("%s got a token with the opt-in off", d.Name())
		}
	}
}

func TestWithBridgeTokenDoesNotMutateItsInput(t *testing.T) {
	in := map[string]string{"A": "1"}
	out := withBridgeToken(in, "x")
	if _, ok := in[EnvBridgeToken]; ok || len(in) != 1 {
		t.Errorf("input mutated: %v", in)
	}
	if out[EnvBridgeToken] != "x" || out["A"] != "1" {
		t.Errorf("out = %v", out)
	}
}

// The manifest entry for a bridge with a token names it as an Authorization
// header; one without carries no headers key at all.
func TestManifestEntryCarriesTheTokenHeaderOnlyWhenTheBridgeHasOne(t *testing.T) {
	h := &Host{}
	guarded := &ManagedServer{Name: "p/s", Def: ServerDef{Env: map[string]string{EnvBridgeToken: testBridgeToken}}, handshake: Handshake{NetworkType: "tcp", Address: "127.0.0.1:9", Protocol: "streamable-http"}}
	e := h.ServerEntry(guarded)
	if e.Headers["Authorization"] != "Bearer "+testBridgeToken || len(e.Headers) != 1 {
		t.Errorf("headers = %v, want the bearer token", e.Headers)
	}
	if e.Type != "http" || !strings.HasPrefix(e.URL, "http://127.0.0.1:9/") {
		t.Errorf("entry = %+v", e)
	}

	open := &ManagedServer{Name: "p/n", handshake: guarded.handshake}
	raw, err := json.Marshal(h.ServerEntry(open))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "headers") {
		t.Errorf("an entry with no token marshals with headers: %s", raw)
	}

	// A server reserved for the agent unit runs a bridge started with the same
	// env, so its entry carries the header too; one with no token does not.
	reserved := &ManagedServer{Name: "p/r", reserved: true, Def: guarded.Def, handshake: guarded.handshake}
	if got := h.ServerEntry(reserved).Headers["Authorization"]; got != "Bearer "+testBridgeToken {
		t.Errorf("a reserved bridge's entry header = %q, want the bearer token", got)
	}
	reservedOpen := &ManagedServer{Name: "p/ro", reserved: true, handshake: guarded.handshake}
	if got := h.ServerEntry(reservedOpen).Headers; got != nil {
		t.Errorf("a reserved server with no token got headers %v", got)
	}
}

// bridgeLike answers 401 to any request without the bearer token, as the
// bridge does, and counts what it saw.
func bridgeLike(t *testing.T, token string, ok, refused *atomic.Int32) (addr string) {
	t.Helper()
	mux := http.NewServeMux()
	handler := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
				refused.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			ok.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}
	}
	mux.HandleFunc("/clown/shutdown", handler(""))
	mux.HandleFunc("/clown/system-prompt", handler("a fragment"))
	mux.HandleFunc("/mcp", handler(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"one","description":"d"}]}}`))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).String()
}

// With the token set, the host's own calls (shutdown notice, system-prompt
// fetch, tool catalog) all pass a guarded server, and a request without the
// header is refused; with no token the host sends no Authorization at all.
func TestHostControlCallsSendTheTokenAndAPlainRequestIsRefused(t *testing.T) {
	var ok, refused atomic.Int32
	addr := bridgeLike(t, testBridgeToken, &ok, &refused)
	srv := &ManagedServer{
		Name: "p/s",
		Def: ServerDef{
			SystemPromptPath: BridgeSystemPromptPath,
			Env:              map[string]string{EnvBridgeRecordDir: "/r", EnvBridgeToken: testBridgeToken},
		},
		handshake: Handshake{NetworkType: "tcp", Address: addr, Protocol: "streamable-http"},
	}
	h := &Host{Servers: []*ManagedServer{srv}}

	srv.noteShutdown()
	if frags := h.FetchPromptFragments(context.Background()); len(frags) != 1 || frags[0] != "a fragment" {
		t.Errorf("prompt fragments = %v, want the one fragment", frags)
	}
	if tools, got := h.FetchToolCatalog(context.Background(), srv); !got || len(tools) != 1 || tools[0].Name != "one" {
		t.Errorf("tool catalog = %v, %v", tools, got)
	}
	if refused.Load() != 0 {
		t.Errorf("the host's own calls were refused %d times", refused.Load())
	}
	if ok.Load() != 4 { // shutdown, system-prompt, initialize, tools/list
		t.Errorf("guarded server saw %d accepted calls, want 4", ok.Load())
	}

	// A caller that does not have the token is refused.
	resp, err := http.Post("http://"+addr+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a request without the header got %d, want 401", resp.StatusCode)
	}
	if refused.Load() != 1 {
		t.Errorf("refused = %d, want 1", refused.Load())
	}
}

func TestHostControlCallsSendNoAuthorizationWithoutAToken(t *testing.T) {
	var seen atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/clown/shutdown", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			seen.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	srv := &ManagedServer{
		Name:      "p/s",
		Def:       ServerDef{Env: map[string]string{EnvBridgeRecordDir: "/r"}},
		handshake: Handshake{NetworkType: "tcp", Address: ts.Listener.Addr().(*net.TCPAddr).String()},
	}
	srv.noteShutdown()
	if seen.Load() != 0 {
		t.Error("a server with no token was sent an Authorization header")
	}
	if srv.Client() != http.DefaultClient {
		t.Error("a server with no token should use the plain handshake client")
	}
}

// A compiled manifest that carries a credential is owner-only; one that does
// not keeps its mode.
func TestCompiledManifestWithHeadersIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "plugin.json")
	if err := os.WriteFile(src, []byte(`{"name":"p"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mode := func(servers map[string]MCPServerEntry) os.FileMode {
		dst := filepath.Join(dir, "out.json")
		_ = os.Remove(dst)
		if err := writeCompiledManifest(src, dst, CompileInputs{Servers: servers}); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(dst)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Mode().Perm()
	}
	if got := mode(map[string]MCPServerEntry{"s": {Type: "http", URL: "http://x/mcp", Headers: map[string]string{"Authorization": "Bearer t"}}}); got != 0o600 {
		t.Errorf("manifest with headers mode = %o, want 600", got)
	}
	// Without headers the file is written as before (0644 less the umask), so
	// only the credential-bearing one is tightened.
	if got := mode(map[string]MCPServerEntry{"s": {Type: "http", URL: "http://x/mcp"}}); got&^0o644 != 0 {
		t.Errorf("manifest without headers mode = %o, want no bit beyond 644", got)
	}
}
