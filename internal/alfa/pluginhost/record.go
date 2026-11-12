package pluginhost

import (
	"context"
	"net/http"
	"strings"
	"time"

	"code.linenisgreat.com/clown/internal/0/disconnectlog"
)

// The disconnect record (clown#344, docs/plans/2026-11-06-clown-344-
// disconnect-record.md). A bridged server's clown-stdio-bridge writes a record
// of what it sees of its child; the host writes one for the bridge itself
// dying, which the bridge cannot. These are the host's two halves of it: the
// identity it gives a bridge, and the watch on the bridge process.

// Environment keys the host sets on a bridge it starts, read by the bridge
// (cmd/clown-stdio-bridge/recorder.go).
const (
	EnvBridgePlugin    = "CLOWN_BRIDGE_PLUGIN_NAME"
	EnvBridgeRecordDir = "CLOWN_BRIDGE_RECORD_DIR"
	EnvBridgeHostLog   = "CLOWN_BRIDGE_HOST_LOG"
)

// DiagnosticsPluginName is the built-in plugin that serves the records
// (cmd/clown). Its own server is never recorded, so a failure of the thing
// that reports failures cannot recurse.
const DiagnosticsPluginName = "clown-builtin-diagnostics"

// recordEnv returns env with the bridge's record identity added, copying env so
// a map shared by a parsed clown.json is never mutated.
func recordEnv(env map[string]string, plugin, recordRoot, hostLog string) map[string]string {
	out := make(map[string]string, len(env)+3)
	for k, v := range env {
		out[k] = v
	}
	out[EnvBridgePlugin] = plugin
	out[EnvBridgeRecordDir] = recordRoot
	if hostLog != "" {
		out[EnvBridgeHostLog] = hostLog
	}
	return out
}

// recorded reports whether this server is a bridge the host gave a record
// directory.
func (s *ManagedServer) recorded() bool {
	return s.Def.Env[EnvBridgeRecordDir] != ""
}

// StartedAt is when the server's process was started (zero before Start).
func (s *ManagedServer) StartedAt() time.Time { return s.startedAt }

// noteShutdown tells a recording bridge that the SIGTERM about to follow is the
// host stopping it on purpose, so it writes no record for it. Best effort and
// bounded: a bridge that does not answer is stopped anyway, and at worst writes
// one spurious record for the signal.
func (s *ManagedServer) noteShutdown() {
	if !s.recorded() || s.handshake.Address == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.handshake.BaseURL()+"/clown/shutdown", nil)
	if err != nil {
		return
	}
	resp, err := s.Client().Do(req)
	if err != nil {
		s.logger().Debug("shutdown notice not delivered", "err", err)
		return
	}
	resp.Body.Close()
}

// ExitEvent is a started server that died while the session ran.
type ExitEvent struct {
	Server     *ManagedServer
	PluginName string
	Name       string
	ServerName string
	PID        int
	StartedAt  time.Time
	Code       int
	Signal     string
	// Bridge is true when the server is a recording bridge (which may have
	// written its own record); false for any other started server.
	Bridge bool
	// StderrTail is the scrubbed tail of the process's stderr.
	StderrTail []disconnectlog.StderrLine
}

// WatchExits calls onExit, once and from its own goroutine, for each started
// server that ends without the host having begun to stop it: it crashed, was
// killed, or exited on a fault of its own. A server the host stops (Stop, so
// Shutdown) is not reported. Servers that were reserved for a server running
// elsewhere, that have no plugin name (the aggregator), or that are the
// diagnostics plugin itself (a failure of the thing that reports failures must
// not recurse) are not watched. It returns at once.
func (h *Host) WatchExits(onExit func(ExitEvent)) {
	for _, srv := range h.Servers {
		if srv.Reserved() || srv.Done() == nil || srv.PluginName == "" || srv.PluginName == DiagnosticsPluginName {
			continue
		}
		go func(s *ManagedServer) {
			<-s.Done()
			if s.stopping.Load() {
				return
			}
			ev := ExitEvent{Server: s, PluginName: s.PluginName, Name: s.Name, ServerName: strings.TrimPrefix(s.Name, s.PluginName+"/"), StartedAt: s.startedAt, Bridge: s.recorded(), StderrTail: s.tail.Snapshot()}
			if s.cmd != nil && s.cmd.Process != nil {
				ev.PID = s.cmd.Process.Pid
			}
			if s.waitState != nil {
				ev.Code = s.waitState.ExitCode()
				ev.Signal = exitSignal(s.waitState)
			}
			onExit(ev)
		}(srv)
	}
}
