package main

import (
	"strings"
	"testing"
)

func TestParseOSRelease(t *testing.T) {
	const nixos = `NAME="NixOS"
ID=nixos
VERSION_ID="25.11"
PRETTY_NAME="NixOS 25.11 (Xantusia)"
`
	name, version, id := parseOSRelease(nixos)
	if name != "NixOS" || version != "25.11" || id != "nixos" {
		t.Fatalf("parseOSRelease(nixos) = (%q,%q,%q), want (NixOS,25.11,nixos)", name, version, id)
	}

	const ubuntu = `NAME="Ubuntu"
VERSION_ID="24.04"
ID=ubuntu
`
	name, version, id = parseOSRelease(ubuntu)
	if name != "Ubuntu" || version != "24.04" || id != "ubuntu" {
		t.Fatalf("parseOSRelease(ubuntu) = (%q,%q,%q), want (Ubuntu,24.04,ubuntu)", name, version, id)
	}
}

func TestHostInfoFragmentNixOS(t *testing.T) {
	h := hostInfo{hostname: "clown-dev", osName: "NixOS", osVersion: "25.11", goos: "linux", isNixOS: true}
	frag := h.fragment()
	if !strings.HasPrefix(frag, "## Host identity") {
		t.Fatalf("fragment should start with the Host identity heading; got:\n%s", frag)
	}
	for _, want := range []string{"host: clown-dev", "OS: NixOS 25.11", "NixOS-managed", "systemctl --user"} {
		if !strings.Contains(frag, want) {
			t.Errorf("fragment missing %q; got:\n%s", want, frag)
		}
	}
}

func TestHostInfoFragmentNonNixOSLinux(t *testing.T) {
	h := hostInfo{hostname: "box", osName: "Ubuntu", osVersion: "24.04", goos: "linux"}
	frag := h.fragment()
	if !strings.Contains(frag, "OS: Ubuntu 24.04") {
		t.Errorf("fragment missing Ubuntu OS line; got:\n%s", frag)
	}
	if !strings.Contains(frag, "non-NixOS Linux") {
		t.Errorf("fragment should describe non-NixOS Linux config model; got:\n%s", frag)
	}
	if strings.Contains(frag, "NixOS-managed") {
		t.Errorf("non-NixOS host must not claim NixOS-managed; got:\n%s", frag)
	}
}

func TestHostInfoFragmentEmptyWhenNothingKnown(t *testing.T) {
	if frag := (hostInfo{}).fragment(); frag != "" {
		t.Fatalf("fragment with no determinable info should be empty, got %q", frag)
	}
}

func TestHostInfoFragmentDarwinFallsBackToGOOS(t *testing.T) {
	h := hostInfo{hostname: "mac", osName: "macOS", goos: "darwin"}
	frag := h.fragment()
	if !strings.Contains(frag, "OS: macOS") || !strings.Contains(frag, "config model: macOS") {
		t.Errorf("darwin fragment should report macOS OS + config model; got:\n%s", frag)
	}
}
