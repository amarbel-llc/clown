package pluginhost

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/0/disconnectlog"
)

// writeBridgePlugin writes a plugin dir named name whose clown.json declares
// the given stdio servers and one native http server.
func writeBridgePlugin(t *testing.T, name string, stdio ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(`{"name":"`+name+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	servers := ""
	for i, s := range stdio {
		if i > 0 {
			servers += ","
		}
		servers += `"` + s + `":{"command":"/bin/true","env":{"KEEP":"me"}}`
	}
	cfg := `{"version":1,"httpServers":{"native":{"command":"/bin/true"}},"stdioServers":{` + servers + `}}`
	if err := os.WriteFile(filepath.Join(dir, "clown.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func serverNamed(t *testing.T, found []DiscoveredServer, name string) DiscoveredServer {
	t.Helper()
	for _, d := range found {
		if d.ServerName == name {
			return d
		}
	}
	t.Fatalf("no discovered server %q in %v", name, found)
	return DiscoveredServer{}
}

// clown#344: Discover gives a bridge the plugin name, the record directory and
// the host log, keeps its own env, and leaves a native server and the
// diagnostics plugin's bridge alone.
func TestDiscoverGivesBridgesTheirRecordIdentity(t *testing.T) {
	const bridge = "/opt/clown/bin/clown-stdio-bridge"
	cg := writeBridgePlugin(t, "cutting-garden", "cg")
	diag := writeBridgePlugin(t, DiagnosticsPluginName, "disconnects")
	host := &Host{PluginDirs: []string{cg, diag}, BridgePath: bridge, RecordRoot: "/state/clown/disconnects", LogPath: "/logs/host.log"}
	found, err := host.Discover()
	if err != nil {
		t.Fatal(err)
	}

	env := serverNamed(t, found, "cg").Def.Env
	for k, want := range map[string]string{
		EnvBridgePlugin:    "cutting-garden",
		EnvBridgeRecordDir: "/state/clown/disconnects",
		EnvBridgeHostLog:   "/logs/host.log",
		"KEEP":             "me",
	} {
		if env[k] != want {
			t.Errorf("bridge env[%s] = %q, want %q", k, env[k], want)
		}
	}
	if env["CLOWN_BRIDGE_SERVER_NAME"] != "cg" {
		t.Errorf("the statsd server name was lost: %v", env)
	}
	if env := serverNamed(t, found, "native").Def.Env; env[EnvBridgeRecordDir] != "" {
		t.Errorf("a native http server got the record identity: %v", env)
	}
	for _, d := range found {
		if d.PluginName == DiagnosticsPluginName && d.Def.Env[EnvBridgeRecordDir] != "" {
			t.Errorf("the diagnostics plugin's server %s is recorded; it must not record itself", d.ServerName)
		}
	}

	// Without a record root nothing changes.
	plain := &Host{PluginDirs: []string{cg}, BridgePath: bridge}
	found, err = plain.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if env := serverNamed(t, found, "cg").Def.Env; env[EnvBridgeRecordDir] != "" || env[EnvBridgePlugin] != "" {
		t.Errorf("a host with no RecordRoot added the record identity: %v", env)
	}
}

func TestRecordEnvDoesNotMutateItsInput(t *testing.T) {
	in := map[string]string{"A": "1"}
	out := recordEnv(in, "p", "/root", "")
	if len(in) != 1 || in[EnvBridgePlugin] != "" {
		t.Errorf("input mutated: %v", in)
	}
	if _, ok := out[EnvBridgeHostLog]; ok {
		t.Errorf("an empty host log must not be set: %v", out)
	}
}

// The host's note before the SIGTERM reaches a recording bridge, and only one.
func TestNoteShutdownPostsToARecordingBridgeOnly(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/clown/shutdown" {
			posts.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().(*net.TCPAddr).String()

	recording := &ManagedServer{Name: "p/s", Def: ServerDef{Env: map[string]string{EnvBridgeRecordDir: "/r"}}, handshake: Handshake{NetworkType: "tcp", Address: addr}}
	recording.noteShutdown()
	if posts.Load() != 1 {
		t.Errorf("a recording bridge got %d notices, want 1", posts.Load())
	}
	plain := &ManagedServer{Name: "p/n", handshake: Handshake{NetworkType: "tcp", Address: addr}}
	plain.noteShutdown()
	if posts.Load() != 1 {
		t.Errorf("a server that does not record was sent a notice (%d)", posts.Load())
	}

	// A bridge that is gone must not hold the stop up.
	gone := &ManagedServer{Def: ServerDef{Env: map[string]string{EnvBridgeRecordDir: "/r"}}, handshake: Handshake{NetworkType: "tcp", Address: "127.0.0.1:1"}}
	start := time.Now()
	gone.noteShutdown()
	if time.Since(start) > 3*time.Second {
		t.Errorf("an unreachable bridge held the notice for %v", time.Since(start))
	}
}

func finishedServer(name, plugin string, recorded, stopping bool) *ManagedServer {
	s := &ManagedServer{Name: name, PluginName: plugin, startedAt: time.Now(), done: make(chan struct{})}
	if recorded {
		s.Def.Env = map[string]string{EnvBridgeRecordDir: "/r"}
	}
	s.stopping.Store(stopping)
	close(s.done)
	return s
}

// A started server that ends while the host is not stopping it is reported
// once (a bridge flagged as such, any other server not); one the host is
// stopping, one reserved for a server running elsewhere, one with no plugin
// name and the diagnostics plugin's own server are not (clown#241).
func TestWatchExitsReportsOnlyUnintendedDeaths(t *testing.T) {
	died := finishedServer("dies", "cutting-garden", true, false)
	stopped := finishedServer("stopped", "cutting-garden", true, true)
	plain := finishedServer("plain", "other", false, false)
	plain.tail = disconnectlog.NewRing()
	plain.tail.Add("plain went down hard")
	reserved := finishedServer("reserved", "other", true, false)
	reserved.reserved = true
	nameless := finishedServer("nameless", "", false, false)
	diag := finishedServer("disconnects", DiagnosticsPluginName, false, false)
	host := &Host{Servers: []*ManagedServer{died, stopped, plain, reserved, nameless, diag}}

	got := make(chan ExitEvent, 6)
	host.WatchExits(func(ev ExitEvent) { got <- ev })

	seen := map[string]ExitEvent{}
	for range 2 {
		select {
		case ev := <-got:
			seen[ev.Name] = ev
		case <-time.After(5 * time.Second):
			t.Fatalf("expected two deaths, got %v", seen)
		}
	}
	if ev, ok := seen["dies"]; !ok || ev.PluginName != "cutting-garden" || ev.Server != died || ev.StartedAt.IsZero() || !ev.Bridge {
		t.Errorf("bridge event = %+v", ev)
	}
	if ev, ok := seen["plain"]; !ok || ev.Bridge || len(ev.StderrTail) != 1 || ev.StderrTail[0].Line != "plain went down hard" {
		t.Errorf("non-bridge event = %+v; want it reported with its stderr tail", ev)
	}
	select {
	case ev := <-got:
		t.Errorf("unexpected third event %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}
