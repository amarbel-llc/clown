package jugglertools

import (
	"fmt"
	"strconv"
	"strings"
)

// The moxy upstream announces itself with the clown plugin handshake line,
// specified in clown-plugin-protocol(7):
//
//	CORE-VERSION|APP-VERSION|NETWORK-TYPE|NETWORK-ADDR|PROTOCOL
//
// This is juggler's own parser of that wire format, a copy of the subset of
// clown's internal/pluginhost it needs, so juggler's packages import nothing
// from clown (clown#258). The spec, not the Go package, is what the two
// share.

// HandshakeCoreProtocolVersion is the only core protocol version accepted.
const HandshakeCoreProtocolVersion = 1

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
	if coreVer != HandshakeCoreProtocolVersion {
		return Handshake{}, fmt.Errorf("handshake: incompatible core protocol version %d (expected %d)", coreVer, HandshakeCoreProtocolVersion)
	}

	appVer, err := strconv.Atoi(parts[1])
	if err != nil {
		return Handshake{}, fmt.Errorf("handshake: invalid app protocol version %q: %w", parts[1], err)
	}

	netType := parts[2]
	if netType != "tcp" {
		return Handshake{}, fmt.Errorf("handshake: unsupported network type %q (expected tcp)", netType)
	}

	addr := parts[3]
	if addr == "" {
		return Handshake{}, fmt.Errorf("handshake: empty network address")
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

// URL is the MCP endpoint the handshake announces.
func (h Handshake) URL() string {
	if h.Protocol == "sse" {
		return "http://" + h.Address + "/sse"
	}
	return "http://" + h.Address + "/mcp"
}
