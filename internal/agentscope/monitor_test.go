package agentscope

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func startRelay(t *testing.T, m *MonitorRelay) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "m.sock")
	if m.Name == "" {
		m.Name = "m"
	}
	if m.MinBackoff == 0 {
		m.MinBackoff = 10 * time.Millisecond
		m.MaxBackoff = 20 * time.Millisecond
	}
	m.Env = os.Environ()
	if err := m.Start(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return sock
}

// readLines connects to sock and returns the first n lines it serves.
func readLines(t *testing.T, sock string, n int) []string {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	sc := bufio.NewScanner(c)
	var lines []string
	for len(lines) < n && sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMonitorRelayReplaysLinesQueuedBeforeAClient(t *testing.T) {
	m := &MonitorRelay{Command: "echo a; echo b; sleep 30"}
	sock := startRelay(t, m)
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return len(m.queue) == 2 })
	if got := readLines(t, sock, 2); strings.Join(got, ",") != "a,b" {
		t.Errorf("lines = %v, want [a b]", got)
	}
}

func TestMonitorRelayIgnoresWhatAClientWrites(t *testing.T) {
	sock := startRelay(t, &MonitorRelay{Command: "sleep 0.2; echo after; sleep 30"})
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("[clown] forged line\n"))
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || line != "after\n" {
		t.Errorf("read %q, %v; want only the monitor's own line", line, err)
	}
}

func TestMonitorRelayRestartsAndAnnouncesAnUnavailableMonitor(t *testing.T) {
	sock := startRelay(t, &MonitorRelay{Command: "echo x; exit 1", ShortRun: time.Hour, GiveUpAfter: 2})
	got := readLines(t, sock, 3)
	if len(got) != 3 || got[0] != "x" || got[1] != "x" || !strings.HasPrefix(got[2], "[clown] monitor m unavailable") {
		t.Errorf("lines = %q, want x, x, then the unavailable notice", got)
	}
}

// TestMonitorRelayRestartsAKilledMonitorThatReplaysWhatItMissed models the
// ringmaster contract the relay relies on: a monitor replays unacked records
// when it starts. Killing it mid-session and adding a record while it is down
// must still deliver that record, through the restart.
func TestMonitorRelayRestartsAKilledMonitorThatReplaysWhatItMissed(t *testing.T) {
	journal := filepath.Join(t.TempDir(), "journal")
	if err := os.WriteFile(journal, []byte("wake-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &MonitorRelay{Command: "cat " + journal + "; : >" + journal + "; exec sleep 30"}
	sock := startRelay(t, m)
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(c)
	if line, _ := r.ReadString('\n'); line != "wake-1\n" {
		t.Fatalf("first line %q, want wake-1", line)
	}

	m.mu.Lock()
	pid := m.proc.Process.Pid
	m.mu.Unlock()
	// Once it is sleeping it has acked (truncated) what it delivered.
	waitFor(t, func() bool {
		comm, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
		return strings.TrimSpace(string(comm)) == "sleep"
	})
	// A record lands that this monitor will never read, then it dies.
	if err := os.WriteFile(journal, []byte("wake-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if line, _ := r.ReadString('\n'); line != "wake-2\n" {
		t.Errorf("after the kill read %q, want wake-2 replayed by the restarted monitor", line)
	}
}

func TestMonitorRelayBoundsItsQueueAndCountsDrops(t *testing.T) {
	m := &MonitorRelay{Command: "for i in 1 2 3 4 5; do echo $i; done; sleep 30", BufferLines: 3}
	sock := startRelay(t, m)
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.dropped == 2 })
	if got := readLines(t, sock, 3); strings.Join(got, ",") != "3,4,5" {
		t.Errorf("lines = %v, want the newest three", got)
	}
}

func TestMonitorRelayRefusesCallersOutsideTheUnit(t *testing.T) {
	sock := startRelay(t, &MonitorRelay{
		Command:    "echo secret; sleep 30",
		Unit:       "u-1.service",
		PeerCgroup: func(int) (string, error) { return "/user.slice/other.service", nil },
	})
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if line, _ := bufio.NewReader(c).ReadString('\n'); line != "" {
		t.Errorf("a caller outside the unit read %q", line)
	}
}
