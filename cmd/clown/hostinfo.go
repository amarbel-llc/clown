package main

import (
	"os"
	"runtime"
	"strings"
)

// hostInfo is the best-effort host identity clown folds into the system-prompt
// append (clown#152). It is distinct from the kernel "OS Version" line claude
// already injects in its Environment section (e.g. "Linux 6.12.76"): that is the
// kernel, not the distro or the machine, and it does not say whether the host is
// NixOS-managed — which materially changes where system config and services live.
type hostInfo struct {
	hostname  string
	osName    string // os-release NAME on linux, "macOS" on darwin
	osVersion string // os-release VERSION_ID (may be empty)
	goos      string // runtime.GOOS
	isNixOS   bool
}

// hostIdentityFragment gathers the host identity and renders the system-prompt
// fragment, or "" when nothing useful is determinable. Every probe is
// best-effort: a failed hostname or an absent /etc/os-release simply omits that
// line rather than failing the launch (clown#152).
func hostIdentityFragment() string {
	return gatherHostInfo().fragment()
}

// gatherHostInfo probes the running host. Cheap and side-effect-free: one
// os.Hostname() call and, on linux, one read of /etc/os-release.
func gatherHostInfo() hostInfo {
	h := hostInfo{goos: runtime.GOOS}
	if name, err := os.Hostname(); err == nil {
		h.hostname = name
	}
	switch h.goos {
	case "linux":
		if data, err := os.ReadFile("/etc/os-release"); err == nil {
			name, version, id := parseOSRelease(string(data))
			h.osName, h.osVersion = name, version
			h.isNixOS = id == "nixos"
		}
	case "darwin":
		h.osName = "macOS"
	}
	return h
}

// parseOSRelease extracts NAME, VERSION_ID, and ID from /etc/os-release content
// (the freedesktop format: KEY=value, value optionally double-quoted). Unknown
// keys are ignored; missing keys yield empty strings.
func parseOSRelease(content string) (name, versionID, id string) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		val = strings.Trim(val, `"'`)
		switch key {
		case "NAME":
			name = val
		case "VERSION_ID":
			versionID = val
		case "ID":
			id = val
		}
	}
	return name, versionID, id
}

// fragment renders the markdown host-identity block, or "" when no line is
// determinable (so the append simply gains nothing rather than an empty heading).
func (h hostInfo) fragment() string {
	var lines []string
	if h.hostname != "" {
		lines = append(lines, "- host: "+h.hostname)
	}
	if desc := h.osDescription(); desc != "" {
		lines = append(lines, "- OS: "+desc)
	}
	if cm := h.configModel(); cm != "" {
		lines = append(lines, "- config model: "+cm)
	}
	if len(lines) == 0 {
		return ""
	}
	return "## Host identity\n\n" + strings.Join(lines, "\n")
}

// osDescription is the human OS string: "<NAME> <VERSION_ID>" when both are
// known, else the NAME alone, else the bare GOOS.
func (h hostInfo) osDescription() string {
	switch {
	case h.osName != "" && h.osVersion != "":
		return h.osName + " " + h.osVersion
	case h.osName != "":
		return h.osName
	default:
		return h.goos
	}
}

// configModel hints where system configuration and services live — the bit the
// kernel/distro lines don't convey. NixOS is called out specifically because it
// changes the whole model (system units, not home-manager / `systemctl --user`),
// which is the concrete miss clown#152 was filed against.
func (h hostInfo) configModel() string {
	switch {
	case h.isNixOS:
		return "NixOS-managed — system config lives in the NixOS system configuration; " +
			"system services are systemd *system* units, not home-manager / `systemctl --user`"
	case h.goos == "darwin":
		return "macOS"
	case h.goos == "linux":
		return "non-NixOS Linux"
	default:
		return ""
	}
}
