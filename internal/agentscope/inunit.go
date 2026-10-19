package agentscope

import (
	"encoding/json"
	"fmt"
	"os"
)

// InUnitPortBase is the first loopback port reserved, inside the unit's
// private network namespace, for agent-side plugin servers. Below the host's
// ephemeral range, so it cannot collide with a forwarded frontend port, nor
// with RelayPort or XMPP's 5222.
const InUnitPortBase = 20000

// InUnitServer is an agent-side plugin server (moxy and anything else without
// "scope":"frontend") that `clown agent-relay` starts INSIDE the unit: its
// tools then act with only what the unit can see. The frontend reserves
// Listen in the compiled manifest; the relay starts the server, waits for its
// handshake and healthcheck, and relays Listen to the address it announced.
type InUnitServer struct {
	Name           string            `json:"name"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"`
	HealthPath     string            `json:"health_path"`
	HealthInterval string            `json:"health_interval"`
	HealthTimeout  string            `json:"health_timeout"`
	Listen         string            `json:"listen"`
}

// WriteInUnitServers writes the list the relay reads (0600).
func WriteInUnitServers(path string, servers []InUnitServer) error {
	raw, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// ReadInUnitServers reads what WriteInUnitServers wrote.
func ReadInUnitServers(path string) ([]InUnitServer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var servers []InUnitServer
	if err := json.Unmarshal(raw, &servers); err != nil {
		return nil, fmt.Errorf("in-unit servers %s: %w", path, err)
	}
	return servers, nil
}

// InUnitMoxyfile is the global moxyfile of an in-unit moxy, bound read-only
// over $HOME/.config/moxy/moxyfile inside the unit. allow-tools lists
// intersect down moxy's hierarchy, so a worktree moxyfile can only narrow it.
//
// Allowed: moxins that act only on what the unit sees (files, git, nix via the
// daemon, local docs) plus moxy's meta tools. Disabled, with the reason a
// confined worker loses them (FDR 0020 "What a confined worker loses"):
// smith (writes to forges; a confined worker files through issue-filer),
// chrest (drives the operator's browser: the prompt surface), the Google
// moxins (credentials under the hidden home), freud (operator transcripts),
// stats-me (a host-loopback service the unit cannot reach).
const InUnitMoxyfile = `# Written by clown for an agent unit (clown#244, FDR 0020). Read-only inside
# the unit; allow-tools intersects down the hierarchy, so nothing below widens it.
allow-tools = ["folio", "grit", "rg", "jq", "man", "chix", "env", "batch", "async", "async-result", "async-cancel", "status", "restart"]
disable-moxins = ["smith", "chrest", "calendar", "car", "gmail", "gws", "piers", "prison", "freud", "stats-me"]
`
