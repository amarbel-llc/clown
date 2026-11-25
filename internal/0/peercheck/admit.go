package peercheck

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// maxCgroupFileBytes bounds the file the listener reads: a cgroup path is far
// shorter; a larger file is not clown's.
const maxCgroupFileBytes = 4096

// ReadUnitCgroupFile reads the file clown writes with the unit's exact cgroup
// (FDR 0048: <runtime>/clown/launch/<key>/bridge.cgroup), the listener's side of
// the seam. It is read FRESH on every call (a listener calls it per connection),
// and fails closed: a missing, empty, unreadable, over-long or non-regular file
// (a symlink, a directory, a socket) is an error, as is a value that is not an
// absolute path or holds a newline (one trailing newline is tolerated) or a NUL.
// The error is what a listener logs; the answer to the peer is always
// `ERR refused` (bridgewire.RefusedUnadmitted), never a silent close.
func ReadUnitCgroupFile(path string) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("the unit's cgroup is not recorded yet (%s: %w)", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxCgroupFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxCgroupFileBytes {
		return "", errors.New("the cgroup file is over-long")
	}
	v := strings.TrimSuffix(string(b), "\n")
	if v == "" || !strings.HasPrefix(v, "/") || strings.ContainsAny(v, "\n\x00") {
		return "", fmt.Errorf("the cgroup file holds %q, not an absolute cgroup path", v)
	}
	return v, nil
}

// AdmitCgroup is the listener's admission decision for a peer whose cgroup is
// peerCgroup: it reads the file now and admits only on strict equality
// (CheckCgroupExact). nil admits; every error refuses.
func AdmitCgroup(cgroupFile, peerCgroup string) error {
	want, err := ReadUnitCgroupFile(cgroupFile)
	if err != nil {
		return err
	}
	return CheckCgroupExact(peerCgroup, want)
}
