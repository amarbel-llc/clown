package agentscope

import (
	"slices"
	"strings"
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

// clown#333: the broker socket is bound into the unit as a single inode and
// relayed from its own in-unit port; a launch without one adds neither.
func TestLaunchBindsAndRelaysTheBrokerSocket(t *testing.T) {
	l := testLaunch()
	l.Broker = Relay{Listen: "127.0.0.1:19001", Socket: "/run/user/1001/clown/serve/p1/broker.sock"}
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--property=BindPaths=" + l.Broker.Socket, "--relay", "127.0.0.1:19001=" + l.Broker.Socket} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
	none, err := testLaunch().SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range none {
		if strings.Contains(a, "broker.sock") || strings.Contains(a, ":19001=") {
			t.Errorf("a launch without a broker mentions it: %q", a)
		}
	}
	if BrokerPort == ServePortBase {
		t.Error("the broker relay shares the serve relay's port")
	}
}
