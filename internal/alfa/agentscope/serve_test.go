package agentscope

import (
	"slices"
	"testing"
)

// F12 (FDR 0020): the per-session serve socket. The socket's peer check is
// tested in internal/0/peercheck.

// The serve socket is bound into the unit and relayed from the port claude's
// URL names; the result reports it.
func TestLaunchBindsRelaysAndReportsTheServeSocket(t *testing.T) {
	l := testLaunch()
	l.Serve = Relay{Listen: "127.0.0.1:19000", Socket: "/run/user/1001/clown/serve/p1/spinclass.sock"}
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--property=BindPaths=" + l.Serve.Socket, "--relay", "127.0.0.1:19000=" + l.Serve.Socket} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
	if r := l.baseResult(); r.ServeSocket != l.Serve.Socket {
		t.Errorf("result serve_socket = %q, want %q", r.ServeSocket, l.Serve.Socket)
	}
	if !slices.Contains(Capabilities, "frontend-serve-socket") {
		t.Error("capability frontend-serve-socket is not advertised")
	}
	none := testLaunch()
	if r := none.baseResult(); r.ServeSocket != "" {
		t.Errorf("a launch without a serve socket reports %q", r.ServeSocket)
	}
}
