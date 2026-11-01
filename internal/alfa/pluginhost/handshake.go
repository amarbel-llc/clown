package pluginhost

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
)

const CoreProtocolVersion = 1

type Handshake struct {
	CoreVersion int
	AppVersion  int
	NetworkType string
	Address     string
	Protocol    string
}

func ParseHandshake(line string) (Handshake, error) {
	line = strings.TrimSpace(line)
	parts := strings.Split(line, "|")
	if len(parts) < 5 {
		return Handshake{}, fmt.Errorf("handshake: expected 5 pipe-delimited fields, got %d: %q", len(parts), line)
	}

	coreVer, err := strconv.Atoi(parts[0])
	if err != nil {
		return Handshake{}, fmt.Errorf("handshake: invalid core protocol version %q: %w", parts[0], err)
	}
	if coreVer != CoreProtocolVersion {
		return Handshake{}, fmt.Errorf("handshake: incompatible core protocol version %d (expected %d)", coreVer, CoreProtocolVersion)
	}

	appVer, err := strconv.Atoi(parts[1])
	if err != nil {
		return Handshake{}, fmt.Errorf("handshake: invalid app protocol version %q: %w", parts[1], err)
	}

	netType := parts[2]
	if netType != "tcp" && netType != "unix" {
		return Handshake{}, fmt.Errorf("handshake: unsupported network type %q (expected tcp or unix)", netType)
	}

	addr := parts[3]
	if addr == "" {
		return Handshake{}, fmt.Errorf("handshake: empty network address")
	}
	if netType == "unix" && !filepath.IsAbs(addr) {
		return Handshake{}, fmt.Errorf("handshake: unix socket path %q is not absolute", addr)
	}

	proto := parts[4]
	if proto != "streamable-http" && proto != "sse" {
		return Handshake{}, fmt.Errorf("handshake: unsupported protocol %q (expected streamable-http or sse)", proto)
	}

	return Handshake{
		CoreVersion: coreVer,
		AppVersion:  appVer,
		NetworkType: netType,
		Address:     addr,
		Protocol:    proto,
	}, nil
}

func (h Handshake) URL() string {
	return h.URLWithHostRewrite("")
}

// unixHost is the Host a request to a unix-socket server carries; the
// connection itself goes to the socket (Client).
const unixHost = "unix"

// BaseURL is the scheme and host plugin-host's own requests to this server
// use; paths are appended to it. A unix server's host is a placeholder: its
// Client dials the socket.
func (h Handshake) BaseURL() string {
	if h.NetworkType == "unix" {
		return "http://" + unixHost
	}
	return "http://" + h.Address
}

// Client is the HTTP client plugin-host's own requests to this server use:
// one that dials the socket for a unix server (F12, FDR 0020), the default
// client otherwise.
func (h Handshake) Client() *http.Client {
	if h.NetworkType != "unix" {
		return http.DefaultClient
	}
	sock := h.Address
	// No keep-alives: each call builds its own transport, which must not
	// leave idle connections to the bridge behind.
	return &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
}

// URLWithHostRewrite returns the same URL as URL(), but with the host
// portion of the address replaced when hostOverride is non-empty. The
// port and path components are preserved. An empty hostOverride is a
// no-op (equivalent to URL()).
//
// Use this when the URL is being written into something an out-of-process
// consumer will dial — e.g. a compiled plugin manifest that
// claude-code reads from inside a container that can't resolve the
// loopback the plugin server bound to. Plugin-host itself should
// keep using URL() / Address for its own dials (healthchecks and
// shutdown) because *those* run as the same process / network
// namespace as the bind.
func (h Handshake) URLWithHostRewrite(hostOverride string) string {
	path := "/mcp"
	if h.Protocol == "sse" {
		path = "/sse"
	}
	if h.NetworkType == "unix" {
		return h.BaseURL() + path
	}
	addr := h.Address
	if hostOverride != "" {
		if _, port, err := net.SplitHostPort(h.Address); err == nil {
			addr = net.JoinHostPort(hostOverride, port)
		}
	}
	return "http://" + addr + path
}
