package agentscope

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// StatsdPort is where the in-unit statsd relay listens (UDP, on the unit's
// private loopback): the port clown-hook-toolcost sends to by default, so the
// hook needs no change inside the unit (clown#269).
const StatsdPort = 8125

// StatsdAddr is the in-unit statsd relay's listen address.
func StatsdAddr() string { return fmt.Sprintf("127.0.0.1:%d", StatsdPort) }

// udpPrefix marks a relay's listen address as UDP in its ADDR=SOCK form.
const udpPrefix = "udp:"

// datagramMax bounds one relayed statsd datagram; statsd senders keep theirs
// under a 1500-byte MTU, so anything larger is not a statsd packet.
const datagramMax = 65507

// StatsdRelay is the frontend end of the unit's statsd path. A private network
// namespace leaves UDP no way out and the egress proxy only does CONNECT, so
// the in-unit relay forwards each datagram over a unixgram socket bound into
// the unit. StatsdRelay reads that socket with SO_PASSCRED, drops datagrams
// whose sender is not in the unit's cgroup (the same rule as the egress proxy
// and the forwards), and sends the rest, unchanged, to Target (the frontend's
// STATSD_HOST:STATSD_PORT).
type StatsdRelay struct {
	Target     string
	Unit       string
	Logger     *slog.Logger
	PeerCgroup func(pid int) (string, error)

	mu   sync.Mutex
	conn *net.UnixConn
}

// Listen creates the unixgram socket at path (0600) and serves it until Close.
func (s *StatsdRelay) Listen(path string) error {
	up, err := net.Dial("udp", s.Target)
	if err != nil {
		return err
	}
	_ = os.Remove(path)
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		up.Close()
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		conn.Close()
		up.Close()
		return err
	}
	if err := setPassCred(conn); err != nil {
		conn.Close()
		up.Close()
		return err
	}
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
	go s.serve(conn, up)
	return nil
}

// Close stops relaying.
func (s *StatsdRelay) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

func (s *StatsdRelay) serve(conn *net.UnixConn, up net.Conn) {
	defer up.Close()
	buf := make([]byte, datagramMax)
	oob := make([]byte, unix.CmsgSpace(unix.SizeofUcred))
	for {
		n, oobn, _, _, err := conn.ReadMsgUnix(buf, oob)
		if err != nil {
			return
		}
		if err := s.authorize(oob[:oobn]); err != nil {
			if s.Logger != nil {
				s.Logger.Warn("statsd: dropped datagram", "err", err)
			}
			continue
		}
		// Fire-and-forget, like the hook's own send: statsd being down
		// must never stall the relay.
		_, _ = up.Write(buf[:n])
	}
}

// authorize checks the SCM_CREDENTIALS the kernel attached (SO_PASSCRED; the
// pid is translated into the reader's pid namespace, as with SO_PEERCRED).
func (s *StatsdRelay) authorize(oob []byte) error {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		cred, err := unix.ParseUnixCredentials(&m)
		if err != nil {
			continue
		}
		return checkUnitCgroup(int(cred.Pid), s.Unit, s.PeerCgroup)
	}
	return fmt.Errorf("datagram carries no sender credentials")
}

func setPassCred(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := raw.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1)
	}); err != nil {
		return err
	}
	return serr
}

// runUDPRelay is the in-unit end: every datagram received on r.Listen is sent,
// unchanged, to the frontend's unixgram socket r.Socket. A send that fails
// (the frontend gone, its buffer full) drops the datagram; the dial is retried
// on the next one.
func runUDPRelay(pc net.PacketConn, r Relay) error {
	var up net.Conn
	buf := make([]byte, datagramMax)
	for {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			return err
		}
		if up == nil {
			if up, err = net.Dial("unixgram", r.Socket); err != nil {
				up = nil
				continue
			}
		}
		_ = up.SetWriteDeadline(time.Now().Add(250 * time.Millisecond))
		if _, err := up.Write(buf[:n]); err != nil {
			up.Close()
			up = nil
		}
	}
}

// isUDP reports whether the relay's listen address is UDP.
func (r Relay) isUDP() bool { return strings.HasPrefix(r.Listen, udpPrefix) }

// listenAddr is the relay's listen address without its transport prefix.
func (r Relay) listenAddr() string { return strings.TrimPrefix(r.Listen, udpPrefix) }
