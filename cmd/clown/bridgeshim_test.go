package main

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/0/bridgewire"
)

func TestParseBridgeListenArgs(t *testing.T) {
	t.Setenv("CLOWN_BRIDGE_SOCKET", "")
	sock, eps, err := parseBridgeListenArgs([]string{"--socket", "/s", "--tcp", "127.0.0.1:3128=egress", "--unix", "/x/ssh.sock=keys"})
	if err != nil || sock != "/s" || len(eps) != 2 || eps[0].Network != "tcp" || eps[0].Service != "egress" || eps[1].Network != "unix" || eps[1].Addr != "/x/ssh.sock" {
		t.Fatalf("parsed %q %+v %v", sock, eps, err)
	}
	t.Setenv("CLOWN_BRIDGE_SOCKET", "/env.sock")
	if sock, _, err := parseBridgeListenArgs([]string{"--tcp", "127.0.0.1:1=model"}); err != nil || sock != "/env.sock" {
		t.Errorf("socket from the environment: %q, %v", sock, err)
	}
	for name, args := range map[string][]string{
		"bad endpoint":  {"--socket", "/s", "--tcp", "noequals"},
		"empty service": {"--socket", "/s", "--tcp", "a="},
		"unknown flag":  {"--socket", "/s", "--frob", "x"},
		"dangling flag": {"--socket", "/s", "--tcp"},
	} {
		if _, _, err := parseBridgeListenArgs(args); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	// Control only: no endpoint is valid.
	if sock, eps, err := parseBridgeListenArgs([]string{"--socket", "/s"}); err != nil || sock != "/s" || len(eps) != 0 {
		t.Errorf("control only: %q %v %v", sock, eps, err)
	}
	t.Setenv("CLOWN_BRIDGE_SOCKET", "")
	if _, _, err := parseBridgeListenArgs([]string{"--tcp", "127.0.0.1:1=model"}); err == nil {
		t.Error("no socket anywhere: want an error")
	}
}

func TestBridgeStdioVerb(t *testing.T) {
	t.Setenv("CLOWN_BRIDGE_SOCKET", "")
	dir, err := os.MkdirTemp("/tmp", "cbv")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "bridge.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				h, err := bridgewire.ReadHello(c)
				if err != nil || h.Service != "hook" {
					_, _ = c.Write(bridgewire.FormatErr(bridgewire.CodeUnknownService, h.Service))
					return
				}
				_, _ = c.Write(bridgewire.OK())
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	var out, errb bytes.Buffer
	if code := runBridgeStdio([]string{"--socket", sock, "--service", "hook"}, strings.NewReader(`{"tool":"Bash"}`+"\n"), &out, &errb); code != 0 || out.String() != `{"tool":"Bash"}`+"\n" {
		t.Errorf("hook stream: exit %d, out %q, err %q", code, out.String(), errb.String())
	}
	// The same through the environment's socket.
	t.Setenv("CLOWN_BRIDGE_SOCKET", sock)
	out.Reset()
	if code := runBridgeStdio([]string{"--service", "hook"}, strings.NewReader("x\n"), &out, &errb); code != 0 || out.String() != "x\n" {
		t.Errorf("env socket: exit %d, out %q", code, out.String())
	}
	// An unknown service: exit 1 and the server's refusal on stderr, nothing on stdout.
	out.Reset()
	errb.Reset()
	if code := runBridgeStdio([]string{"--service", "nope"}, strings.NewReader(""), &out, &errb); code != 1 || out.Len() != 0 || !strings.Contains(errb.String(), "unknown-service") {
		t.Errorf("unknown service: exit %d, out %q, err %q", code, out.String(), errb.String())
	}
	// No bridge at all: the fixed down text on stderr, exit 1, nothing on stdout.
	t.Setenv("CLOWN_BRIDGE_SOCKET", filepath.Join(dir, "gone.sock"))
	out.Reset()
	errb.Reset()
	if code := runBridgeStdio([]string{"--service", "hook"}, strings.NewReader(""), &out, &errb); code != 1 || out.Len() != 0 ||
		!strings.HasPrefix(errb.String(), "clown: the bridge is down (") {
		t.Errorf("no bridge: exit %d, out %q, err %q", code, out.String(), errb.String())
	}
	// Usage errors.
	if code := runBridgeStdio([]string{"--service"}, nil, &out, &errb); code != 2 {
		t.Errorf("a dangling flag: exit %d", code)
	}
	t.Setenv("CLOWN_BRIDGE_SOCKET", "")
	if code := runBridgeStdio([]string{"--service", "hook"}, nil, &out, &errb); code != 2 {
		t.Errorf("no socket: exit %d", code)
	}
}
