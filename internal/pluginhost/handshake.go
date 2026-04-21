package pluginhost

import (
	"fmt"
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

func (h Handshake) URL() string {
	path := "/mcp"
	if h.Protocol == "sse" {
		path = "/sse"
	}
	return "http://" + h.Address + path
}
