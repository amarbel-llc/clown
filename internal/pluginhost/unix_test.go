package pluginhost

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// F12 (FDR 0020): a server that announces a unix socket is health-checked
// and asked for its prompt fragment over that socket, and claude's manifest
// names the in-unit address it is relayed from instead.

func unixServer(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc(BridgeSystemPromptPath, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "fragment over unix") })
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func TestParseHandshakeAcceptsUnix(t *testing.T) {
	hs, err := ParseHandshake("1|1|unix|/run/x/s.sock|streamable-http")
	if err != nil || hs.NetworkType != "unix" || hs.Address != "/run/x/s.sock" {
		t.Fatalf("ParseHandshake = %+v, %v", hs, err)
	}
	if _, err := ParseHandshake("1|1|unix|relative.sock|streamable-http"); err == nil {
		t.Error("want refusal of a relative socket path")
	}
	if _, err := ParseHandshake("1|1|udp|127.0.0.1:1|streamable-http"); err == nil {
		t.Error("want refusal of an unknown network type")
	}
}

func TestUnixHandshakeHealthAndPromptFragment(t *testing.T) {
	hs := Handshake{CoreVersion: 1, NetworkType: "unix", Address: unixServer(t), Protocol: "streamable-http"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := WaitHealthyHandshake(ctx, hs, "/healthz", 50*time.Millisecond); err != nil {
		t.Fatalf("health over unix: %v", err)
	}
	h := &Host{}
	srv := &ManagedServer{Name: "spinclass/spinclass", Def: ServerDef{SystemPromptPath: BridgeSystemPromptPath}, handshake: hs}
	h.Servers = append(h.Servers, srv)
	if got := h.FetchPromptFragments(ctx); !slices.Equal(got, []string{"fragment over unix"}) {
		t.Errorf("fragments = %q", got)
	}
}

func TestManifestNamesTheRelayedAddress(t *testing.T) {
	h := &Host{}
	srv := &ManagedServer{Name: "spinclass/spinclass", handshake: Handshake{NetworkType: "unix", Address: "/run/x/s.sock", Protocol: "streamable-http"}}
	srv.RouteManifestThrough("127.0.0.1:19000")
	if got := h.ServerEntry(srv); got.URL != "http://127.0.0.1:19000/mcp" || got.Type != "http" {
		t.Errorf("manifest entry = %+v", got)
	}
	tcp := &ManagedServer{handshake: Handshake{NetworkType: "tcp", Address: "127.0.0.1:4242", Protocol: "streamable-http"}}
	if got := h.ServerEntry(tcp); got.URL != "http://127.0.0.1:4242/mcp" {
		t.Errorf("tcp manifest entry changed: %+v", got)
	}
}
