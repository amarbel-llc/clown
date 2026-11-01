package main

import (
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"code.linenisgreat.com/clown/internal/0/peercheck"
	"code.linenisgreat.com/clown/internal/alfa/agentscope"
	"code.linenisgreat.com/clown/internal/alfa/pluginhost"
)

// policyBrokerEnv is the one variable that names the session's policy-broker
// socket to the agent and to moxy (clown#333, spinclass#438 option i). Unset
// or empty means no broker. Unconfined its value is the socket's absolute
// path; confined it is the in-unit relay's loopback address (host:port),
// because the unit's private network namespace cannot see the path.
const policyBrokerEnv = "CLOWN_POLICY_BROKER"

// policyBroker is a session's broker socket: clown names it, `spinclass serve`
// (the Server) creates and listens on it. Zero: the session has no broker.
type policyBroker struct {
	Server string // "<plugin>/<server>" of the serve that listens
	Socket string
}

// routePolicyBroker names the session's policy-broker socket and passes it to
// the spinclass serve bridge's environment under policyBrokerEnv, so serve can
// listen there. It creates the session's 0700 directory for serve to bind in.
// It returns a copy of discovered with only that server's Env changed; a
// session without a spinclass serve returns discovered, the zero broker and
// no error, and creates nothing. Any error means "no broker": the caller logs
// it and launches without one (moxy then uses its static floor).
func routePolicyBroker(discovered []pluginhost.DiscoveredServer, bridgePath, runtimeDir, principal string) ([]pluginhost.DiscoveredServer, policyBroker, error) {
	out := slices.Clone(discovered)
	var broker policyBroker
	for i, d := range out {
		if !isServeServer(d, bridgePath) {
			continue
		}
		if broker.Socket != "" {
			return nil, policyBroker{}, fmt.Errorf("plugin %s declares more than one frontend server; one policy broker per session is supported", servePlugin)
		}
		sock, err := peercheck.BrokerSocketPath(runtimeDir, principal)
		if err != nil {
			return nil, policyBroker{}, fmt.Errorf("policy broker socket for %s: %w", d.Name(), err)
		}
		dir := filepath.Dir(sock)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, policyBroker{}, err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, policyBroker{}, err
		}
		env := maps.Clone(d.Def.Env)
		if env == nil {
			env = map[string]string{}
		}
		env[policyBrokerEnv] = sock
		out[i].Def.Env = env
		broker = policyBroker{Server: d.Name(), Socket: sock}
	}
	return out, broker, nil
}

// cleanup removes the socket serve may have left behind (a crash) and then
// the session directory, if nothing else is in it. A zero broker is a no-op.
func (b policyBroker) cleanup() {
	if b.Socket == "" {
		return
	}
	_ = os.Remove(b.Socket)
	_ = os.Remove(filepath.Dir(b.Socket))
}

// brokerSocketPresent reports whether path is a socket file. A confined unit
// binds the socket in as a single inode, and systemd refuses a bind whose
// source does not exist, so the relay is added only for a socket serve made.
func brokerSocketPresent(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

// applyPolicyBroker exports the broker to the agent once the plugin servers
// are up. started is whether the broker's serve started and the session still
// binds it (and the provider can take an environment); without it nothing is
// exported. Unconfined, the agent gets the path in bound.Env. Confined, the
// socket must exist: it is relayed into the unit (flags.agentScopeBroker) and
// the agent and every in-unit server get the relay's address instead.
func applyPolicyBroker(flags *parsedFlags, bound *Command, started bool, logger *slog.Logger) {
	b := flags.policyBroker
	if b.Socket == "" || !started {
		return
	}
	value := b.Socket
	if flags.agentScopeSpec != "" {
		if !brokerSocketPresent(b.Socket) {
			if logger != nil {
				logger.Warn("policy broker: serve created no socket; not relaying it into the unit", "socket", b.Socket)
			}
			return
		}
		flags.agentScopeBroker = agentscope.Relay{Listen: fmt.Sprintf("127.0.0.1:%d", agentscope.BrokerPort), Socket: b.Socket}
		value = flags.agentScopeBroker.Listen
		flags.agentScopeInUnit = withBrokerEnv(flags.agentScopeInUnit, value)
	}
	bound.Env = append(bound.Env, policyBrokerEnv+"="+value)
	if logger != nil {
		logger.Info("policy broker exported", "server", b.Server, "value", value)
	}
}

// withBrokerEnv returns servers with policyBrokerEnv=value added to each Env,
// on copies: the caller's maps are not touched.
func withBrokerEnv(servers []agentscope.InUnitServer, value string) []agentscope.InUnitServer {
	out := slices.Clone(servers)
	for i := range out {
		env := maps.Clone(out[i].Env)
		if env == nil {
			env = map[string]string{}
		}
		env[policyBrokerEnv] = value
		out[i].Env = env
	}
	return out
}

// brokerStarted is whether the broker's serve is among the started servers and
// the session still binds it.
func brokerStarted(b policyBroker, started []*pluginhost.ManagedServer, kept []pluginhost.DiscoveredServer) bool {
	return b.Socket != "" &&
		slices.ContainsFunc(started, func(s *pluginhost.ManagedServer) bool { return s.Name == b.Server }) &&
		slices.ContainsFunc(kept, func(d pluginhost.DiscoveredServer) bool { return d.Name() == b.Server })
}
