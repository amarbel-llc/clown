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

// CheckCgroupExact reports whether cg is exactly the unit's control group
// (want, the ControlGroup property read once from the manager) or a sub-group
// of it. Unlike CheckUnitCgroup's suffix match it cannot be satisfied by a
// look-alike path another process made under a delegated subtree.
func CheckCgroupExact(cg, want string) error {
	if want == "" || !strings.HasPrefix(want, "/") || strings.HasSuffix(want, "/") {
		return fmt.Errorf("the unit's control group %q is not an absolute path", want)
	}
	if cg == want || strings.HasPrefix(cg, want+"/") {
		return nil
	}
	return fmt.Errorf("caller is in %q, not in %q", cg, want)
}

// CheckPeerExact admits the peer of c only if it runs in the control group
// want (or below it). The peer is taken as a pidfd (SO_PEERPIDFD), its pid
// is read from that pidfd, and the pidfd is confirmed still live after the
// cgroup is read, so a recycled pid is not mistaken for the peer. A kernel
// without SO_PEERPIDFD fails closed.
func CheckPeerExact(c net.Conn, want string) error {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return errors.New("not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	pidfd := -1
	var gerr error
	if err := raw.Control(func(fd uintptr) {
		pidfd, gerr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, soPeerPIDFD)
	}); err != nil {
		return err
	}
	if gerr != nil {
		return fmt.Errorf("peer pidfd: %w (needs Linux 6.5+; failing closed)", gerr)
	}
	defer unix.Close(pidfd)
	pid, err := pidfdPID("/proc", pidfd)
	if err != nil {
		return err
	}
	cg, err := ProcCgroup(pid)
	if err != nil {
		return err
	}
	if err := unix.PidfdSendSignal(pidfd, 0, nil, 0); err != nil {
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
