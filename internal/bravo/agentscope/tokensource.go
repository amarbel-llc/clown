package agentscope

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
)

// maxTokenBytes bounds a token file read: a bearer is a few hundred bytes.
const maxTokenBytes = 16 << 10

// TokenSource reads the model API bearer the InjectProxy injects, from a file
// outside every unit (clown#336): written by `piggy secrets reconcile` under
// $XDG_RUNTIME_DIR at a card-present moment. It is read on every request and
// never cached, so replacing the file rotates the credential and deleting it
// revokes at once. The value is never part of an error.
type TokenSource struct {
	Path string

	// ownerUID replaces the process uid in tests.
	ownerUID func() int
}

// Token returns the file's content with one trailing newline removed. It
// refuses a missing, empty or non-regular file, a file readable by group or
// others, and a file the frontend's uid does not own.
func (s TokenSource) Token() (string, error) {
	if s.Path == "" {
		return "", errors.New("no model credential path (XDG_RUNTIME_DIR is unset and CLOWN_MODEL_TOKEN_FILE is not)")
	}
	// O_NOFOLLOW: a symlink at the path (a planted one) is never followed to
	// some other file of the operator's.
	f, err := os.OpenFile(s.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("no model credential at %s: absent", s.Path)
		}
		if errors.Is(err, syscall.ELOOP) {
			return "", fmt.Errorf("model credential %s is a symlink; it must be a regular file", s.Path)
		}
		return "", s.readError(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", s.readError(err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("model credential %s is not a regular file", s.Path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("model credential %s has mode %04o; it must be 0600", s.Path, fi.Mode().Perm())
	}
	uid := os.Getuid
	if s.ownerUID != nil {
		uid = s.ownerUID
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != uid() {
		return "", fmt.Errorf("model credential %s is owned by uid %d, not %d", s.Path, st.Uid, uid())
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxTokenBytes+1))
	if err != nil {
		return "", s.readError(err)
	}
	if len(raw) > maxTokenBytes {
		return "", fmt.Errorf("model credential %s is larger than %d bytes", s.Path, maxTokenBytes)
	}
	tok := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if tok == "" {
		return "", fmt.Errorf("model credential %s is empty", s.Path)
	}
	return tok, nil
}

// readError names the path once; an *fs.PathError's own path is dropped.
func (s TokenSource) readError(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf("model credential %s: %w", s.Path, err)
}
