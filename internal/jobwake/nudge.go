package jobwake

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// bindNudge binds the channel's unixgram nudge socket, removing a stale socket
// file at the path first (RFC-0009 §9). The runtime dir is created mode 0700.
func bindNudge(channelID string) (*net.UnixConn, error) {
	p := SocketPath(channelID)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(p) // clear stale socket before bind (RFC-0009 §9)
	addr := &net.UnixAddr{Name: p, Net: "unixgram"}
	return net.ListenUnixgram("unixgram", addr)
}

// sendNudge sends a single best-effort datagram "<v>|<job>|<type>\n" to the
// channel socket. All errors are ignored: a missing socket (no monitor running)
// is the common case, and correctness never depends on the nudge (RFC-0009 §6).
func sendNudge(channelID, jobID, eventType string) {
	p := SocketPath(channelID)
	raddr := &net.UnixAddr{Name: p, Net: "unixgram"}
	conn, err := net.DialUnix("unixgram", nil, raddr)
	if err != nil {
		return // best-effort; common when no monitor is running (RFC-0009 §6)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(fmt.Sprintf("%d|%s|%s\n", SchemaVersion, jobID, eventType)))
}

// removeSocket unlinks the channel's nudge socket file (monitor shutdown).
func removeSocket(channelID string) error {
	return os.Remove(SocketPath(channelID))
}
