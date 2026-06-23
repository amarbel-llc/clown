//go:build !linux

// Package ptysuspend's relay is implemented only for Linux (the /dev/ptmx +
// TIOCGPTN allocation path). On other platforms Run reports unsupported so the
// package still builds; darwin support (posix_openpt/grantpt) is a follow-up.
package ptysuspend

import (
	"fmt"
	"os"
)

// DefaultEscapeKey mirrors the linux build's constant so callers can reference
// it on any platform. ctrl-z (0x1a).
const DefaultEscapeKey = 0x1a

// Options mirrors the linux build's Options type so callers compile on every
// platform; the proxy itself is a no-op here (see Run).
type Options struct {
	// Enabled gates the whole proxy; callers also check Supported() + interactivity.
	Enabled bool
	// EscapeKey is the input byte intercepted as the escape trigger. Zero means
	// DefaultEscapeKey (ctrl-z).
	EscapeKey byte
	// EscapeArgv is the command run when the escape key is pressed. Empty means
	// the escape key is swallowed.
	EscapeArgv []string
}

// Supported reports that the pty-suspend proxy is not implemented here, so
// callers fall back to a normal launch.
func Supported() bool { return false }

// Run is unsupported on non-Linux platforms.
func Run(argv []string, outer *os.File, opts Options) (int, error) {
	return 1, fmt.Errorf("ptysuspend: unsupported on this platform")
}
