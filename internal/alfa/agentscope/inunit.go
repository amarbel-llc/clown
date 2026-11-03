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
	// Exclude are tool names the relay pushes to the server's
	// /clown/exclude-tools once it has started: a profile's saved exclusions,
	// which the frontend cannot push to a server it never started (clown#290).
	Exclude []string `json:"exclude,omitempty"`
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

// The in-unit moxy's global moxyfile (InUnitMoxyfile) is rendered from the
// ratified ceiling in ceiling.go.
