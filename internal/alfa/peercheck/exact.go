package peercheck

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// soPeerPIDFD is SO_PEERPIDFD (Linux 6.5+): a pidfd for the peer, taken when
// the connection was made, so the pid cannot be recycled under us.
const soPeerPIDFD = 77

// CheckCgroupExact reports whether cg IS the unit's control group (want, the
// ControlGroup property read once from the manager): strict equality, not "this
// group and below". Passing a slice or a delegated group therefore admits
// nobody who is merely beneath it, and a look-alike path under a delegated
// subtree (unlike CheckUnitCgroup's suffix match) cannot satisfy it.
//
// Why strict is safe for a clown agent unit: seen on flic (just explore
// explore-unit-cgroup-probe), every process of a confined unit, including a
// setsid child, lives in the unit's own cgroup, the unit's tree has no
// sub-cgroup, and the unit could not make one by the two routes tried (seen on
// flic, 2026-11-26, one run each: a direct mkdir is refused, cgroupfs being
// read-only in the unit, ProtectControlGroups=yes; and in a new user+mount
// namespace, with and without a cgroup namespace, mounting a fresh cgroup2 is
// refused, "permission denied" / "must be superuser", so no sub-cgroup was ever
// made and no process moved; the reason for that refusal was not established, so
// it is one host's evidence, not a proof for every kernel). If a unit is ever
// given Delegate=, its processes may live below, and this function must be
// relaxed on purpose, not by accident.
func CheckCgroupExact(cg, want string) error {
	if want == "" || !strings.HasPrefix(want, "/") || strings.HasSuffix(want, "/") {
		return fmt.Errorf("the unit's control group %q is not an absolute path", want)
	}
	if cg == want {
		return nil
	}
	return fmt.Errorf("caller is in %q, not in %q", cg, want)
}

// CheckPeerExact admits the peer of c only if it runs in exactly the control
// group want. The peer is taken as a pidfd (SO_PEERPIDFD), its pid is read from
// that pidfd, and the pidfd is confirmed still live after the cgroup is read,
// so a recycled pid is not mistaken for the peer. A kernel without
// SO_PEERPIDFD fails closed.
//
// What the guarantee rests on (round 1, M1), which this function cannot check:
// the peer cannot move itself into want's cgroup or out of it, so it cannot
// write a cgroup.procs (a confined unit has cgroupfs read-only,
// ProtectControlGroups=yes in agentscope's properties()), and it cannot ask
// the user manager to move a process (seen: a confined unit has no
// /run/user/<uid>/systemd and `systemctl --user` fails to connect). A caller
// whose peers can do either gets no identity from this check.
func CheckPeerExact(c net.Conn, want string) error {
	return checkPeerExactWith(c, want, realPeerOps)
}

// peerOps are the four kernel interactions CheckPeerExact makes, separated so
// a test can pin each safety property by removing it (round 1, A-M5): that no
// pidfd means refused (never a fallback to SO_PEERCRED), that the cgroup read is
// of the pid the pidfd names, that liveness is confirmed AFTER that read, and
// that the pidfd is closed on every path.
type peerOps struct {
	pidfdOf  func(c *net.UnixConn) (int, error) // SO_PEERPIDFD
	pidOf    func(pidfd int) (int, error)       // the pid the pidfd names
	cgroupOf func(pid int) (string, error)      // /proc/<pid>/cgroup
	alive    func(pidfd int) error              // the pidfd still names a live process
	close    func(pidfd int)
}

var realPeerOps = peerOps{
	pidfdOf: func(uc *net.UnixConn) (int, error) {
		raw, err := uc.SyscallConn()
		if err != nil {
			return -1, err
		}
		pidfd := -1
		var gerr error
		if err := raw.Control(func(fd uintptr) {
			pidfd, gerr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, soPeerPIDFD)
		}); err != nil {
			return -1, err
		}
		return pidfd, gerr
	},
	pidOf:    func(pidfd int) (int, error) { return pidfdPID("/proc", pidfd) },
	cgroupOf: ProcCgroup,
	alive:    func(pidfd int) error { return unix.PidfdSendSignal(pidfd, 0, nil, 0) },
	close:    func(pidfd int) { _ = unix.Close(pidfd) },
}

func checkPeerExactWith(c net.Conn, want string, ops peerOps) error {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return errors.New("not a unix connection")
	}
	pidfd, err := ops.pidfdOf(uc)
	if err != nil {
		return fmt.Errorf("peer pidfd: %w (needs Linux 6.5+; failing closed)", err)
	}
	defer ops.close(pidfd)
	pid, err := ops.pidOf(pidfd)
	if err != nil {
		return err
	}
	cg, err := ops.cgroupOf(pid)
	if err != nil {
		return err
	}
	if err := ops.alive(pidfd); err != nil {
		return fmt.Errorf("the peer (pid %d) is gone: %w", pid, err)
	}
	return CheckCgroupExact(cg, want)
}

// pidfdPID reads the pid a pidfd names, in this process's pid namespace, from
// procRoot/self/fdinfo/<fd>.
func pidfdPID(procRoot string, fd int) (int, error) {
	b, err := os.ReadFile(procRoot + "/self/fdinfo/" + strconv.Itoa(fd))
	if err != nil {
		return 0, err
	}
	return parsePidfdInfo(b)
}

func parsePidfdInfo(b []byte) (int, error) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "Pid:"); ok {
			pid, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil || pid <= 0 {
				return 0, fmt.Errorf("the peer is not visible in this pid namespace (Pid: %q)", strings.TrimSpace(rest))
			}
			return pid, nil
		}
	}
	return 0, errors.New("no Pid: line in the pidfd's fdinfo")
}
