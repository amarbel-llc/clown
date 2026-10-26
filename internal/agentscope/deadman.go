package agentscope

import (
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// DeadManGrace is how long the shim waits, after the frontend died, between
// SIGTERM and SIGKILL to everything in the unit (clown#319). Long enough for
// claude to flush its transcript; short against the unit's 7-day backstop.
const DeadManGrace = 10 * time.Second

// DeadMan is the frontend's end of the dead-man socket (clown#319): a unix
// socket bound into the agent unit, to which the shim connects once, before
// it starts anything else. The frontend holds that one connection open and
// never writes to it; when the frontend dies, however it dies (SIGKILL
// included: the kernel closes its fds), the shim reads EOF and stops the
// unit. Only EOF matters: the frontend discards whatever arrives, and once
// the shim's connection is held the listener is closed, so nothing in the
// unit can connect again.
type DeadMan struct {
	Unit       string
	Logger     *slog.Logger
	PeerCgroup func(pid int) (string, error)

	mu   sync.Mutex
	ln   net.Listener
	held net.Conn
}

// Listen creates the socket at path (0600) and waits for the shim.
func (d *DeadMan) Listen(path string) error {
	ln, err := listenUnix(path)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.ln = ln
	d.mu.Unlock()
	go d.accept(ln)
	return nil
}

func (d *DeadMan) accept(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		if err := authorizeUnitPeer(c, d.Unit, d.PeerCgroup); err != nil {
			if d.Logger != nil {
				d.Logger.Warn("dead-man: refused caller", "err", err)
			}
			c.Close()
			continue
		}
		d.mu.Lock()
		d.held = c
		d.mu.Unlock()
		_ = ln.Close()
		// Read only to drain; content is never acted on.
		_, _ = io.Copy(io.Discard, c)
		return
	}
}

// Close closes the listener and the held connection: the shim then stops the
// unit. The frontend calls it only when the unit is done or being stopped.
func (d *DeadMan) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ln != nil {
		_ = d.ln.Close()
	}
	if d.held != nil {
		return d.held.Close()
	}
	return nil
}

// WatchFrontend is the shim's end: it connects to the dead-man socket and
// returns a channel closed when the connection ends (EOF or an error), which
// is the frontend's death. A frontend that cannot be reached is an error: the
// shim refuses to start a provider nothing would stop.
func WatchFrontend(path string) (<-chan struct{}, error) {
	c, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		return nil, err
	}
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		defer c.Close()
		_, _ = io.Copy(io.Discard, c)
	}()
	return gone, nil
}
