package bridgewire

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
)

// The bridge.cgroup file (FDR 0048): the unit's exact cgroup, written by clown
// and read by the serve's listener on every connection. The writer and the
// reader live here, side by side, so that what one produces the other accepts
// and neither can drift (joint round, C-L1).

// MaxCgroupFileBytes bounds the file a reader accepts: a cgroup path is far
// shorter; a larger file is not clown's.
const MaxCgroupFileBytes = 4096

// validCgroupValue says whether v is usable as the unit's cgroup: an absolute
// path with no trailing slash and no control character or DEL anywhere.
func validCgroupValue(v string) bool {
	if !strings.HasPrefix(v, "/") || strings.HasSuffix(v, "/") {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] == 0x7f {
			return false
		}
	}
	return true
}

// WriteCgroupFile records the unit's exact cgroup for the serve at path (the
// bridge.cgroup file, CgroupFileName): 0600, a regular file, exactly the path
// with no trailing newline, written to path+".tmp" and renamed over so a reader
// sees the old value or the new, never a part, and a symlink at path is
// replaced rather than followed. It refuses a value that is not an absolute
// path with no trailing slash and no control character. This is the writer
// clown uses at launch (cmd/clown's writeCgroup adds the check that the value
// is the unit's own); ReadCgroupFile is the reader. Exported so an end-to-end
// test can stand in for clown's half of the seam.
func WriteCgroupFile(path, cgroup string) error {
	if path == "" {
		return errors.New("bridge: no cgroup file path")
	}
	if !validCgroupValue(cgroup) {
		return fmt.Errorf("bridge: unusable cgroup %q", cgroup)
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(cgroup)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ReadCgroupFile reads the file WriteCgroupFile writes, the listener's side of
// the seam. It is read FRESH on every call and fails closed: a missing, empty,
// unreadable, over-long or non-regular file (a symlink, a directory, a socket),
// a file owned by another user or writable by group or others, or a value that
// is not an absolute path with no trailing slash and no control character (one
// trailing newline is tolerated) is an error. The error is what a listener
// logs; the answer to the peer is always `ERR refused` (RefusedUnadmitted),
// never a silent close.
func ReadCgroupFile(path string) (string, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
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
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return "", fmt.Errorf("%s is owned by uid %d, not by this user", path, st.Uid)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("%s is writable by group or others (mode %04o)", path, fi.Mode().Perm())
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxCgroupFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > MaxCgroupFileBytes {
		return "", errors.New("the cgroup file is over-long")
	}
	v := strings.TrimSuffix(string(b), "\n")
	if v == "" || !validCgroupValue(v) {
		return "", fmt.Errorf("the cgroup file holds %q, not an absolute cgroup path", v)
	}
	return v, nil
}
