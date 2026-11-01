package peercheck

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ServeSocketPath is the per-session serve socket for server:
// <runtimeDir>/clown/serve/<principal>/<server>.sock. The directory is the
// session's own, so two sessions never share a socket. It refuses a principal
// or server that is not one path segment, and a path over the unix socket
// limit.
func ServeSocketPath(runtimeDir, principal, server string) (string, error) {
	return sessionSocketPath(runtimeDir, principal, server+".sock")
}

// BrokerPort is the in-unit loopback port the policy-broker socket is relayed
// from (clown#333): beside ServePortBase, which carries the one serve socket a
// session has.
const BrokerPort = ServePortBase + 1

// BrokerSocketPath is the per-session policy-broker socket (clown#333):
// <runtimeDir>/clown/serve/<principal>/broker.sock, in the session's own
// directory beside its serve socket. Clown only names it; `spinclass serve`
// creates it. The same limits as ServeSocketPath apply.
func BrokerSocketPath(runtimeDir, principal string) (string, error) {
	return sessionSocketPath(runtimeDir, principal, "broker.sock")
}

func sessionSocketPath(runtimeDir, principal, file string) (string, error) {
	if !filepath.IsAbs(runtimeDir) {
		return "", errors.New("XDG_RUNTIME_DIR is unset; nowhere to put the session socket")
	}
	for _, seg := range []string{principal, file} {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, "/\x00") {
			return "", fmt.Errorf("%q is not a single path segment", seg)
		}
	}
	p := filepath.Join(runtimeDir, "clown", "serve", principal, file)
	if len(p) >= 108 {
		return "", fmt.Errorf("session socket path %q exceeds the unix socket limit", p)
	}
	return p, nil
}

// ListenServeSocket creates the serve socket at path (its directory 0700,
// the socket 0600) and returns a listener whose Accept yields only callers
// that run in unit's cgroup, or that are the frontend itself: the pid
// frontendPID returns at that accept (the bridge's current parent clown,
// which health-checks the server and fetches its prompt fragment; nil for
// none). Every other caller is closed and logged. peerCgroup resolves a pid's
// cgroup (nil: ProcCgroup); logf may be nil.
//
// A socket at path that still answers belongs to a live bridge (the same
// principal launched again, e.g. a resume or a pinned CLOWN_SESSION_ID): it
// is refused, not unlinked. Only a stale socket file is replaced. Between the
// probe and the replacement another bridge could still bind; two launches of
// one principal racing on the same instant are not guarded against.
func ListenServeSocket(path, unit string, frontendPID func() int, peerCgroup func(int) (string, error), logf func(format string, args ...any)) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("serve socket %s is live: another bridge serves this session's principal; refusing to take it over", path)
	}
	ln, err := ListenUnix(path)
	if err != nil {
		return nil, err
	}
	return &serveListener{Listener: ln, unit: unit, frontendPID: frontendPID, peerCgroup: peerCgroup, logf: logf}, nil
}

type serveListener struct {
	net.Listener
	unit        string
	frontendPID func() int
	peerCgroup  func(int) (string, error)
	logf        func(format string, args ...any)
}

// ServeConn is a connection the serve socket accepted, with its peer class:
// Frontend is true for the frontend clown itself, false for a caller in the
// unit. The bridge serves the control routes only to the frontend
// (clown#301).
type ServeConn struct {
	net.Conn
	Frontend bool
}

func (l *serveListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		frontend, err := authorizeServePeer(c, l.unit, l.frontendPID, l.peerCgroup)
		if err != nil {
			if l.logf != nil {
				l.logf("serve socket: refused caller: %v", err)
			}
			c.Close()
			continue
		}
		return &ServeConn{Conn: c, Frontend: frontend}, nil
	}
}

// authorizeServePeer admits the frontend's current pid (frontend true), else
// only callers in the unit's cgroup. It fails closed: a peer whose pid cannot
// be read is refused. The error names the caller's pid and cgroup, or says
// the cgroup is unknown.
func authorizeServePeer(c net.Conn, unitName string, frontendPID func() int, read func(int) (string, error)) (frontend bool, err error) {
	pid, err := PeerPID(c)
	if err != nil {
		return false, fmt.Errorf("caller's pid unreadable, cgroup unknown: %w", err)
	}
	if frontendPID != nil {
		if fp := frontendPID(); fp > 1 && pid == fp {
			return true, nil
		}
	}
	return false, CheckUnitCgroup(pid, unitName, read)
}
