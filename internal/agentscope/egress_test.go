package agentscope

import (
	"bytes"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllowlistMatchesHostPortAndWildcardSubdomains(t *testing.T) {
	a := Allowlist{"api.anthropic.com:443", "*.githubusercontent.com:443", "github.com:22"}
	for hp, want := range map[string]bool{
		"api.anthropic.com:443":             true,
		"API.Anthropic.com.:443":            true,
		"api.anthropic.com:80":              false,
		"objects.githubusercontent.com:443": true,
		"githubusercontent.com:443":         false,
		"github.com:22":                     true,
		"github.com:443":                    false,
		"evil.com:443":                      false,
		"api.anthropic.com.evil.com:443":    false,
		"not-a-hostport":                    false,
	} {
		if got := a.Allows(hp); got != want {
			t.Errorf("Allows(%q) = %v, want %v", hp, got, want)
		}
	}
}

// echoServer accepts one connection on loopback and echoes it back.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

func proxyFor(t *testing.T, allow Allowlist, cgroup string) string {
	t.Helper()
	p := &EgressProxy{
		Allow:      allow,
		Unit:       "u-1.service",
		PeerCgroup: func(int) (string, error) { return cgroup, nil },
	}
	sock := filepath.Join(t.TempDir(), "egress.sock")
	if err := p.Listen(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return sock
}

const unitCgroup = "/user.slice/user-1001.slice/user@1001.service/clown.slice/clown-agent.slice/u-1.service"

func TestEgressTunnelsAnAllowedDestinationForTheUnit(t *testing.T) {
	target := echoServer(t)
	sock := proxyFor(t, Allowlist{target}, unitCgroup)
	var out bytes.Buffer
	if err := Connect(sock, target, strings.NewReader("ping\n"), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "ping\n" {
		t.Fatalf("tunnel echoed %q", out.String())
	}
}

func TestEgressRefusesDestinationsOffTheAllowlist(t *testing.T) {
	target := echoServer(t)
	sock := proxyFor(t, Allowlist{"api.anthropic.com:443"}, unitCgroup)
	err := Connect(sock, target, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("want 403 refusal, got %v", err)
	}
}

func forwardRoundTrip(t *testing.T, cgroup string) string {
	t.Helper()
	f := &PortForward{Target: echoServer(t), Unit: "u-1.service", PeerCgroup: func(int) (string, error) { return cgroup, nil }}
	sock := filepath.Join(t.TempDir(), "fwd.sock")
	if err := f.Listen(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("ping\n"))
	_ = c.(*net.UnixConn).CloseWrite()
	got, _ := io.ReadAll(c)
	return string(got)
}

func TestPortForwardServesOnlyTheUnit(t *testing.T) {
	if got := forwardRoundTrip(t, unitCgroup); got != "ping\n" {
		t.Fatalf("unit caller got %q", got)
	}
	if got := forwardRoundTrip(t, "/user.slice/user-1001.slice/session-3.scope"); got != "" {
		t.Fatalf("caller outside the unit got %q", got)
	}
}

func TestParseRelay(t *testing.T) {
	r, err := ParseRelay("127.0.0.1:5222=/run/x/fwd.sock")
	if err != nil || r.Listen != "127.0.0.1:5222" || r.Socket != "/run/x/fwd.sock" || r.String() != "127.0.0.1:5222=/run/x/fwd.sock" {
		t.Fatalf("ParseRelay = %+v, %v", r, err)
	}
	if _, err := ParseRelay("127.0.0.1:5222"); err == nil {
		t.Fatal("want error for a relay without a socket")
	}
}

func TestEgressRefusesCallersOutsideTheUnit(t *testing.T) {
	target := echoServer(t)
	for _, cg := range []string{
		"/user.slice/user-1001.slice/session-3.scope",
		"/user.slice/user-1001.slice/user@1001.service/clown.slice/clown-agent.slice/other.service",
	} {
		sock := proxyFor(t, Allowlist{target}, cg)
		if err := Connect(sock, target, strings.NewReader("x"), io.Discard); err == nil {
			t.Errorf("caller in %q was served", cg)
		}
	}
}
