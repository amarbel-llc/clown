package agentscope

import (
	"bytes"
	"errors"
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

// upstreamTarget is the name the tests' proxies resolve to a documentation
// address (192.0.2.1) and dial to a loopback echo server, so a tunnel can be
// tested without a loopback destination, which the proxy refuses.
const upstreamTarget = "upstream.test:443"

// proxyFor serves a proxy whose resolver and dialler send upstreamTarget to
// echo; every other name resolves through resolved, or not at all.
func proxyFor(t *testing.T, allow Allowlist, cgroup, echo string, resolved map[string][]net.IP) string {
	t.Helper()
	p := &EgressProxy{
		Allow:      allow,
		Unit:       "u-1.service",
		PeerCgroup: func(int) (string, error) { return cgroup, nil },
		LookupIP: func(host string) ([]net.IP, error) {
			if host == "upstream.test" {
				return []net.IP{net.ParseIP("192.0.2.1")}, nil
			}
			if ips, ok := resolved[host]; ok {
				return ips, nil
			}
			return nil, errors.New("no such host")
		},
		Dial: func(addr string) (net.Conn, error) {
			if addr == "192.0.2.1:443" {
				return net.Dial("tcp", echo)
			}
			return nil, errors.New("test dialler reaches only upstream.test, not " + addr)
		},
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
	sock := proxyFor(t, Allowlist{upstreamTarget}, unitCgroup, echoServer(t), nil)
	var out bytes.Buffer
	if err := Connect(sock, upstreamTarget, strings.NewReader("ping\n"), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "ping\n" {
		t.Fatalf("tunnel echoed %q", out.String())
	}
}

// TestEgressRefusesLoopbackDestinations: an allowed loopback destination,
// literal or by a name that resolves there, is still refused, and the
// refusal names the destination and the reason.
func TestEgressRefusesLoopbackDestinations(t *testing.T) {
	echo := echoServer(t)
	_, port, _ := net.SplitHostPort(echo)
	sneaky := "sneaky.test:" + port
	for _, target := range []string{
		echo,                                   // 127.0.0.1
		net.JoinHostPort("127.8.9.10", port),   // anywhere in 127.0.0.0/8
		net.JoinHostPort("::1", port),          // IPv6 loopback
		net.JoinHostPort("localhost", port),    // the name
		net.JoinHostPort("db.localhost", port), // a name under .localhost
		net.JoinHostPort("0.0.0.0", port),      // unspecified reaches the loopback
		net.JoinHostPort("::ffff:127.0.0.1", port),
		sneaky, // resolves to 127.0.0.1
	} {
		sock := proxyFor(t, Allowlist{target}, unitCgroup, echo,
			map[string][]net.IP{"sneaky.test": {net.ParseIP("192.0.2.7"), net.ParseIP("127.0.0.1")}})
		err := Connect(sock, target, strings.NewReader("x"), io.Discard)
		if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "loopback") ||
			!strings.Contains(err.Error(), target) {
			t.Errorf("CONNECT %s: want a 403 naming it and the loopback, got %v", target, err)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "LOCALHOST.": true, "x.localhost": true, "127.0.0.1": true, "127.255.0.1": true,
		"::1": true, "[::1]": true, "0.0.0.0": true, "::": true,
		"api.anthropic.com": false, "localhost.evil.com": false, "128.0.0.1": false, "192.0.2.1": false,
	} {
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// TestDefaultEgressAllowNamesNoLoopback: the default list stays clear of the
// loopback refusal.
func TestDefaultEgressAllowNamesNoLoopback(t *testing.T) {
	for _, e := range DefaultEgressAllow {
		host, _, err := net.SplitHostPort(e)
		if err != nil || IsLoopbackHost(host) {
			t.Errorf("default egress entry %q is malformed or loopback", e)
		}
	}
}

func TestEgressRefusesDestinationsOffTheAllowlist(t *testing.T) {
	sock := proxyFor(t, Allowlist{"api.anthropic.com:443"}, unitCgroup, echoServer(t), nil)
	err := Connect(sock, upstreamTarget, strings.NewReader(""), io.Discard)
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
	echo := echoServer(t)
	for _, cg := range []string{
		"/user.slice/user-1001.slice/session-3.scope",
		"/user.slice/user-1001.slice/user@1001.service/clown.slice/clown-agent.slice/other.service",
	} {
		sock := proxyFor(t, Allowlist{upstreamTarget}, cg, echo, nil)
		if err := Connect(sock, upstreamTarget, strings.NewReader("x"), io.Discard); err == nil {
			t.Errorf("caller in %q was served", cg)
		}
	}
}
