package agentscope

import (
	"net"

	"code.linenisgreat.com/clown/internal/0/peercheck"
)

// ServePortBase is the in-unit loopback port claude's manifest names for the
// first serve socket (F12, FDR 0020): below InUnitPortBase, so it cannot
// collide with an in-unit server, a forwarded frontend port, RelayPort or
// XMPP's 5222. The socket itself, and its peer check, live in peercheck.
const ServePortBase = 19000

// The unit-peer primitives live in peercheck, which other modules link for
// their own sockets; these keep agentscope's call sites short.

func peerPID(c net.Conn) (int, error) { return peercheck.PeerPID(c) }

func checkUnitCgroup(pid int, unitName string, read func(pid int) (string, error)) error {
	return peercheck.CheckUnitCgroup(pid, unitName, read)
}

func listenUnix(path string) (net.Listener, error) { return peercheck.ListenUnix(path) }

// ProcCgroup returns pid's cgroup v2 path from /proc/<pid>/cgroup.
func ProcCgroup(pid int) (string, error) { return peercheck.ProcCgroup(pid) }

func procCgroupAt(procRoot string, pid int) (string, error) {
	return peercheck.ProcCgroupAt(procRoot, pid)
}
