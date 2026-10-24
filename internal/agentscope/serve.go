package agentscope

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// ServePortBase is the in-unit loopback port claude's manifest names for the
// first serve socket (F12, FDR 0020): below InUnitPortBase, so it cannot
// collide with an in-unit server, a forwarded frontend port, RelayPort or
// XMPP's 5222.
const ServePortBase = 19000

// ServeSocketPath is the per-session serve socket for server:
// <runtimeDir>/clown/serve/<principal>/<server>.sock. The directory is the
// session's own, so two sessions never share a socket. It refuses a principal
// or server that is not one path segment, and a path over the unix socket
// limit.
func ServeSocketPath(runtimeDir, principal, server string) (string, error) {
	if !filepath.IsAbs(runtimeDir) {
		return "", errors.New("XDG_RUNTIME_DIR is unset; nowhere to put the serve socket")
	}
	for _, seg := range []string{principal, server} {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, "/\x00") {
			return "", fmt.Errorf("%q is not a single path segment", seg)
		}
	}
	p := filepath.Join(runtimeDir, "clown", "serve", principal, server+".sock")
	if len(p) >= 108 {
		return "", fmt.Errorf("serve socket path %q exceeds the unix socket limit", p)
	}
	return p, nil
}

// ListenServeSocket creates the serve socket at path (its directory 0700,
// the socket 0600) and returns a listener whose Accept yields only callers
// that run in unit's cgroup, or that are the frontend itself (frontendPID,
// the bridge's parent clown, which health-checks the server and fetches its
// prompt fragment; 0 for none). Every other caller is closed and logged with
// its cgroup. peerCgroup resolves a pid's cgroup (nil: ProcCgroup); logf may
// be nil.
func ListenServeSocket(path, unit string, frontendPID int, peerCgroup func(int) (string, error), logf func(format string, args ...any)) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	ln, err := listenUnix(path)
	if err != nil {
		return nil, err
	}
	return &serveListener{Listener: ln, unit: unit, frontendPID: frontendPID, peerCgroup: peerCgroup, logf: logf}, nil
}

type serveListener struct {
	net.Listener
	unit        string
	frontendPID int
	peerCgroup  func(int) (string, error)
	logf        func(format string, args ...any)
}

func (l *serveListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if err := authorizeServePeer(c, l.unit, l.frontendPID, l.peerCgroup); err != nil {
			if l.logf != nil {
				l.logf("serve socket: refused caller: %v", err)
			}
			c.Close()
			continue
		}
		return c, nil
	}
}

// authorizeServePeer admits the frontend's own pid, else only callers in the
// unit's cgroup.
func authorizeServePeer(c net.Conn, unitName string, frontendPID int, read func(int) (string, error)) error {
	pid, err := peerPID(c)
	if err != nil {
		return err
	}
	if frontendPID > 0 && pid == frontendPID {
		return nil
	}
	return checkUnitCgroup(pid, unitName, read)
}
