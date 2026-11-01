package agentscope

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// MonitorSpec is one plugin monitor that runs in the frontend for an agent
// unit (FDR 0020 "frontend monitors"): Command is what claude would have run
// inside the unit, Socket where the unit's `clown agent-monitor` reads its
// lines.
type MonitorSpec struct {
	Name    string
	Command string
	Socket  string
}

// MonitorRelay runs one monitor command in the frontend and serves its stdout
// lines on a unix socket bound into the agent unit. The stream is one-way:
// what a reader sends is read only to notice it has gone and is discarded, so
// every line the relay serves, including its own "[clown] monitor ...
// unavailable" notice, was written by the frontend. (Inside the unit, a
// process of the same uid can still write to the reader's stdout; the relay
// cannot speak for that.)
//
// The command starts with the first reader. Lines produced while no reader is
// connected are queued (at most BufferLines, oldest dropped and counted) and
// replayed to the next reader. A command that exits is restarted with capped
// backoff; after GiveUpAfter consecutive runs shorter than ShortRun it emits
// one unavailable notice and keeps retrying at the cap. It is killed with the
// relay (Close) and with the frontend process (Pdeathsig).
type MonitorRelay struct {
	Name    string
	Command string
	Env     []string
	// Unit is the agent unit whose processes may connect. Empty skips the
	// check; only the test harness (`clown agent-monitor-serve`) leaves it
	// empty, and the socket is 0600 either way.
	Unit       string
	Logger     *slog.Logger
	PeerCgroup func(pid int) (string, error)

	BufferLines int
	MinBackoff  time.Duration
	MaxBackoff  time.Duration
	ShortRun    time.Duration
	GiveUpAfter int

	mu      sync.Mutex
	ln      net.Listener
	clients []net.Conn
	queue   []string
	dropped int
	proc    *exec.Cmd
	started bool
	stopped bool
	wake    chan struct{}
}

func (m *MonitorRelay) defaults() {
	if m.BufferLines <= 0 {
		m.BufferLines = 1024
	}
	if m.MinBackoff <= 0 {
		m.MinBackoff = time.Second
	}
	if m.MaxBackoff <= 0 {
		m.MaxBackoff = 30 * time.Second
	}
	if m.ShortRun <= 0 {
		m.ShortRun = 10 * time.Second
	}
	if m.GiveUpAfter <= 0 {
		m.GiveUpAfter = 5
	}
}

// Start listens on path (0600). The monitor command starts with the first
// authorized reader, not before: a monitor such as ringmaster's acks what it
// prints, so lines it printed with nobody to read them would be acked and
// then exist only in this relay's memory.
func (m *MonitorRelay) Start(path string) error {
	m.defaults()
	ln, err := listenUnix(path)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.ln = ln
	m.wake = make(chan struct{})
	m.mu.Unlock()
	m.log("monitor relay started", "monitor", m.Name, "socket", path, "buffer_lines", m.BufferLines)
	go m.accept(ln)
	return nil
}

// Close stops the command (its whole process group) and the listener.
func (m *MonitorRelay) Close() error {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return nil
	}
	m.stopped = true
	close(m.wake)
	proc := m.proc
	for _, c := range m.clients {
		_ = c.Close()
	}
	m.clients = nil
	dropped, undelivered := m.dropped, len(m.queue)
	ln := m.ln
	m.mu.Unlock()
	if proc != nil && proc.Process != nil {
		_ = syscall.Kill(-proc.Process.Pid, syscall.SIGTERM)
	}
	m.log("monitor relay stopped", "monitor", m.Name, "dropped_lines", dropped, "undelivered_lines", undelivered)
	if ln != nil {
		return ln.Close()
	}
	return nil
}

func (m *MonitorRelay) accept(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		if m.Unit != "" {
			if err := authorizeUnitPeer(c, m.Unit, m.PeerCgroup); err != nil {
				m.log("monitor relay: refused caller", "monitor", m.Name, "err", err)
				_ = c.Close()
				continue
			}
		}
		m.mu.Lock()
		if m.stopped {
			m.mu.Unlock()
			_ = c.Close()
			return
		}
		ok := true
		for _, line := range m.queue {
			if !writeLine(c, line) {
				ok = false
				break
			}
		}
		if ok {
			m.queue = nil
			m.clients = append(m.clients, c)
		} else {
			_ = c.Close()
		}
		first := ok && !m.started
		if first {
			m.started = true
		}
		m.mu.Unlock()
		if ok {
			go m.untilGone(c)
		}
		if first {
			go m.run()
		}
	}
}

// untilGone drops c as soon as its reader goes away, so the next line is
// queued for a later reader instead of written into a dead socket. What the
// reader sends is read only to see EOF and is thrown away: nothing from the
// unit ever enters the stream.
func (m *MonitorRelay) untilGone(c net.Conn) {
	_, _ = io.Copy(io.Discard, c)
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, have := range m.clients {
		if have == c {
			m.clients = append(m.clients[:i], m.clients[i+1:]...)
			break
		}
	}
	_ = c.Close()
}

// emit delivers line to every connected client, or queues it when none is.
func (m *MonitorRelay) emit(line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	live := m.clients[:0]
	for _, c := range m.clients {
		if writeLine(c, line) {
			live = append(live, c)
		} else {
			_ = c.Close()
		}
	}
	m.clients = live
	if len(live) > 0 {
		return
	}
	m.queue = append(m.queue, line)
	if over := len(m.queue) - m.BufferLines; over > 0 {
		m.queue = m.queue[over:]
		m.dropped += over
		m.log("monitor relay: buffer full, dropped oldest lines", "monitor", m.Name, "dropped_total", m.dropped, "buffer_lines", m.BufferLines)
	}
}

func writeLine(c net.Conn, line string) bool {
	_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := io.WriteString(c, line+"\n")
	return err == nil
}

func (m *MonitorRelay) run() {
	// Pdeathsig is tied to the thread that forked the child: pinning this
	// goroutine keeps that thread alive for as long as the relay runs, so a
	// frontend killed without unwinding (SIGKILL, a hangup) still takes its
	// monitors with it, as claude's death takes an unconfined session's.
	runtime.LockOSThread()
	backoff := m.MinBackoff
	short := 0
	announced := false
	for {
		started := time.Now()
		err := m.runOnce()
		m.mu.Lock()
		stopped := m.stopped
		m.mu.Unlock()
		if stopped {
			return
		}
		if time.Since(started) < m.ShortRun {
			short++
		} else {
			short, backoff, announced = 0, m.MinBackoff, false
		}
		m.log("monitor relay: monitor exited, restarting", "monitor", m.Name, "err", err, "backoff", backoff, "short_runs", short)
		if short >= m.GiveUpAfter && !announced {
			m.emit(fmt.Sprintf("[clown] monitor %s unavailable: it exited %d times in a row; retrying every %s", m.Name, short, m.MaxBackoff))
			announced = true
		}
		select {
		case <-m.wake:
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, m.MaxBackoff)
	}
}

func (m *MonitorRelay) runOnce() error {
	cmd := exec.Command("sh", "-c", m.Command)
	cmd.Env = m.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = &logWriter{m: m}
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return nil
	}
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		return err
	}
	m.proc = cmd
	m.mu.Unlock()
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		m.emit(sc.Text())
	}
	if err := sc.Err(); err != nil {
		// An over-long line stops the scanner while the monitor may still be
		// writing: stop it rather than wait on a pipe nobody drains.
		m.log("monitor relay: reading the monitor failed; stopping it", "monitor", m.Name, "err", err)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	err = cmd.Wait()
	m.mu.Lock()
	m.proc = nil
	m.mu.Unlock()
	return err
}

func (m *MonitorRelay) log(msg string, args ...any) {
	if m.Logger != nil {
		m.Logger.Info(msg, args...)
	}
}

// logWriter sends a monitor's stderr to the launch log, never to the unit.
type logWriter struct{ m *MonitorRelay }

func (w *logWriter) Write(p []byte) (int, error) {
	w.m.log("monitor stderr", "monitor", w.m.Name, "text", string(p))
	return len(p), nil
}
