// Package peercheck authenticates the peers of a unix socket by the cgroup
// of the process on the other end: it admits only callers that run in one
// clown agent unit (or, for the serve socket, the frontend clown itself). It
// is the part of agentscope a second consumer needs to guard a socket of its
// own, split out so the rest of agentscope is not part of that surface.
package peercheck

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Slice is the systemd slice every agent unit runs in. Its cgroup prefix,
// .../clown-agent.slice/<unit>, is what CheckUnitCgroup matches.
const Slice = "clown-agent.slice"

// PeerPID is a unix connection's peer pid from SO_PEERCRED. The kernel
// translates the pid into the reader's pid namespace, so a caller inside a
// unit's private pid namespace still resolves here.
func PeerPID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return int(cred.Pid), nil
}

// CheckUnitCgroup reports whether pid runs in the agent unit's cgroup:
// .../clown-agent.slice/<unitName>. read resolves a pid's cgroup (nil:
// ProcCgroup).
func CheckUnitCgroup(pid int, unitName string, read func(pid int) (string, error)) error {
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

// ListenUnix creates a unix socket at path (0600), replacing any file there.
func ListenUnix(path string) (net.Listener, error) {
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
func ProcCgroup(pid int) (string, error) { return ProcCgroupAt("/proc", pid) }

// ProcCgroupAt is ProcCgroup against the proc tree at procRoot.
func ProcCgroupAt(procRoot string, pid int) (string, error) {
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cgroup"))
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
