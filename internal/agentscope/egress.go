package agentscope

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// RelayPort is where the in-unit relay listens, on the unit's private
// loopback; HTTPS_PROXY inside the unit names it.
const RelayPort = 3128

// DefaultEgressAllow is used when the spec names no egress_allow: the model
// API and its login hosts, the forges, and the nix caches. spinclass passes
// the sweatfile's list instead when it has one.
var DefaultEgressAllow = []string{
	"api.anthropic.com:443", "console.anthropic.com:443", "platform.claude.com:443", "claude.ai:443",
	"mcp-proxy.anthropic.com:443", // claude.ai connectors (e.g. Krone's issue-filer)
	"mcp.starbrandshoes.com:443",  // the fleet's MCP origin behind those connectors
	"code.linenisgreat.com:443", "code.linenisgreat.com:22",
	"forge.starbrandshoes.com:443", "forge.starbrandshoes.com:22",
	"github.com:443", "github.com:22", "api.github.com:443", "codeload.github.com:443",
	"*.githubusercontent.com:443",
	"cache.nixos.org:443",
}

// Allowlist matches host:port destinations; a "*." host prefix matches any
// subdomain (not the bare domain).
type Allowlist []string

// Allows reports whether host:port is on the list.
func (a Allowlist) Allows(hostport string) bool {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, entry := range a {
		eh, ep, err := net.SplitHostPort(entry)
		if err != nil || ep != port {
			continue
		}
		eh = strings.ToLower(eh)
		if eh == host || (strings.HasPrefix(eh, "*.") && strings.HasSuffix(host, eh[1:])) {
			return true
		}
	}
	return false
}

// EgressProxy is the frontend-scope HTTP CONNECT proxy for one agent unit. It
// serves a unix socket bound into the unit and accepts only callers whose
// cgroup is that unit's.
type EgressProxy struct {
	Allow Allowlist
	// Unit is the agent unit's name; callers must be in
	// .../clown-agent.slice/<Unit>.
	Unit   string
	Logger *slog.Logger
	// PeerCgroup reads a caller's cgroup from its pid; tests replace it.
	PeerCgroup func(pid int) (string, error)

	mu sync.Mutex
	ln net.Listener
}

// Listen creates the socket at path (0600) and serves it until Close.
func (p *EgressProxy) Listen(path string) error {
	ln, err := listenUnix(path)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	go p.serve(ln)
	return nil
}

// Close stops accepting; established tunnels drain on their own.
func (p *EgressProxy) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln == nil {
		return nil
	}
	return p.ln.Close()
}

func (p *EgressProxy) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go p.handle(c)
	}
}

func (p *EgressProxy) handle(c net.Conn) {
	defer c.Close()
	if err := p.authorize(c); err != nil {
		p.log("egress: refused caller", "err", err)
		return
	}
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	if req.Method != http.MethodConnect {
		p.log("egress: refused non-CONNECT request", "method", req.Method, "url", req.URL.String())
		reply(c, http.StatusMethodNotAllowed, "only CONNECT is proxied; use https")
		return
	}
	target := req.Host
	if !p.Allow.Allows(target) {
		p.log("egress: refused destination", "target", target)
		reply(c, http.StatusForbidden, "destination not on the agent scope's egress allowlist: "+target)
		return
	}
	up, err := net.DialTimeout("tcp", target, 15*time.Second)
	if err != nil {
		reply(c, http.StatusBadGateway, err.Error())
		return
	}
	defer up.Close()
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	// Bytes the client pipelined behind the CONNECT are already buffered.
	if n := br.Buffered(); n > 0 {
		buffered, _ := br.Peek(n)
		if _, err := up.Write(buffered); err != nil {
			return
		}
	}
	splice(c, up)
}

// authorize checks the caller's cgroup via SO_PEERCRED: only processes in the
// agent unit may use its egress.
func (p *EgressProxy) authorize(c net.Conn) error {
	return authorizeUnitPeer(c, p.Unit, p.PeerCgroup)
}

// authorizeUnitPeer reports whether the peer of a unix connection runs in the
// agent unit's cgroup. read resolves a pid's cgroup (nil: ProcCgroup). The
// kernel translates SO_PEERCRED's pid into the reader's pid namespace, so a
// caller inside the unit's private pid namespace still resolves here.
func authorizeUnitPeer(c net.Conn, unitName string, read func(pid int) (string, error)) error {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return errors.New("not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credErr != nil {
		return credErr
	}
	return checkUnitCgroup(int(cred.Pid), unitName, read)
}

// checkUnitCgroup reports whether pid runs in the agent unit's cgroup:
// .../clown-agent.slice/<unitName>. read resolves a pid's cgroup (nil:
// ProcCgroup).
func checkUnitCgroup(pid int, unitName string, read func(pid int) (string, error)) error {
	if read == nil {
		read = ProcCgroup
	}
	cg, err := read(pid)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(cg, "/"+Slice+"/"+unitName) {
		return fmt.Errorf("caller pid %d is in %q, not in unit %s", pid, cg, unitName)
	}
	return nil
}

// PortForward serves a unix socket bound into the agent unit and splices
// every authorized connection to one fixed TCP target in the frontend: how a
// unit with a private network namespace reaches exactly the loopback services
// clown chose (its own frontend-class MCP servers, the XMPP server), and
// nothing else on the host's loopback.
type PortForward struct {
	Target     string
	Unit       string
	Logger     *slog.Logger
	PeerCgroup func(pid int) (string, error)

	mu sync.Mutex
	ln net.Listener
}

// Listen creates the socket at path (0600) and serves it until Close.
func (f *PortForward) Listen(path string) error {
	ln, err := listenUnix(path)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.ln = ln
	f.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.handle(c)
		}
	}()
	return nil
}

// Close stops accepting; established connections drain on their own.
func (f *PortForward) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ln == nil {
		return nil
	}
	return f.ln.Close()
}

func (f *PortForward) handle(c net.Conn) {
	defer c.Close()
	if err := authorizeUnitPeer(c, f.Unit, f.PeerCgroup); err != nil {
		if f.Logger != nil {
			f.Logger.Warn("forward: refused caller", "target", f.Target, "err", err)
		}
		return
	}
	up, err := net.DialTimeout("tcp", f.Target, 15*time.Second)
	if err != nil {
		return
	}
	defer up.Close()
	splice(c, up)
}

func listenUnix(path string) (net.Listener, error) {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// ProcCgroup returns pid's cgroup v2 path from /proc/<pid>/cgroup.
func ProcCgroup(pid int) (string, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if rest, ok := bytes.CutPrefix(line, []byte("0::")); ok {
			return string(rest), nil
		}
	}
	return "", fmt.Errorf("no cgroup v2 entry for pid %d", pid)
}

func (p *EgressProxy) log(msg string, args ...any) {
	if p.Logger != nil {
		p.Logger.Warn(msg, args...)
	}
}

func reply(w io.Writer, code int, msg string) {
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, http.StatusText(code), len(msg), msg)
}

// splice copies both directions until either side closes.
func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}

// Relay is one in-unit TCP listener (e.g. 127.0.0.1:3128) whose connections
// are spliced to a frontend unix socket bound into the unit, or, for an
// in-unit server, to the TCP address it announced (Target). A UDP relay (the
// statsd relay) listens on UDP instead and sends each datagram to a frontend
// unixgram Socket.
type Relay struct {
	Listen string
	Socket string
	Target string
	UDP    bool
}

// udpPrefix marks a UDP relay's listen address in its ADDR=SOCK form.
const udpPrefix = "udp:"

func (r Relay) dial() (net.Conn, error) {
	if r.Target != "" {
		return net.Dial("tcp", r.Target)
	}
	return net.Dial("unix", r.Socket)
}

// String renders the relay as the shim's [udp:]ADDR=SOCK argument.
func (r Relay) String() string {
	if r.UDP {
		return udpPrefix + r.Listen + "=" + r.Socket
	}
	return r.Listen + "=" + r.Socket
}

// ParseRelay parses [udp:]ADDR=SOCK.
func ParseRelay(s string) (Relay, error) {
	rest, udp := strings.CutPrefix(s, udpPrefix)
	listen, sock, ok := strings.Cut(rest, "=")
	if !ok || listen == "" || sock == "" {
		return Relay{}, fmt.Errorf("relay %q is not [udp:]ADDR=SOCK", s)
	}
	return Relay{Listen: listen, Socket: sock, UDP: udp}, nil
}

// RunRelays listens on every relay's address (inside the unit) and splices
// each connection to that relay's unix socket (a UDP relay forwards each
// datagram instead). ready is called once all listeners are up; RunRelays
// returns when any listener fails.
func RunRelays(relays []Relay, ready func()) error {
	var opened []io.Closer
	defer func() {
		for _, c := range opened {
			c.Close()
		}
	}()
	var serves []func() error
	for _, r := range relays {
		r := r
		if r.UDP {
			pc, err := net.ListenPacket("udp", r.Listen)
			if err != nil {
				return err
			}
			opened = append(opened, pc)
			serves = append(serves, func() error { return runUDPRelay(pc, r) })
			continue
		}
		ln, err := net.Listen("tcp", r.Listen)
		if err != nil {
			return err
		}
		opened = append(opened, ln)
		serves = append(serves, func() error { return serveTCPRelay(ln, r) })
	}
	if ready != nil {
		ready()
	}
	errc := make(chan error, len(serves))
	for _, serve := range serves {
		go func(serve func() error) { errc <- serve() }(serve)
	}
	return <-errc
}

// serveTCPRelay splices every connection accepted on ln to r's destination.
func serveTCPRelay(ln net.Listener, r Relay) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer c.Close()
			up, err := r.dial()
			if err != nil {
				return
			}
			defer up.Close()
			splice(c, up)
		}()
	}
}

// Connect opens a tunnel to host:port through the proxy socket and splices
// it to rw (stdin/stdout for an ssh ProxyCommand).
func Connect(sock, hostport string, in io.Reader, out io.Writer) error {
	c, err := net.Dial("unix", sock)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", hostport, hostport); err != nil {
		return err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("egress refused %s: %s %s", hostport, resp.Status, strings.TrimSpace(string(body)))
	}
	// Input ending only half-closes the tunnel; the session ends when the
	// remote side does.
	go func() {
		_, _ = io.Copy(c, in)
		if uc, ok := c.(*net.UnixConn); ok {
			_ = uc.CloseWrite()
		}
	}()
	_, err = io.Copy(out, br)
	return err
}
