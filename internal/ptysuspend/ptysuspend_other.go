//go:build !linux

// Package ptysuspend's relay is implemented only for Linux (the /dev/ptmx +
// TIOCGPTN allocation path). On other platforms Run reports unsupported so the
// package still builds; darwin support (posix_openpt/grantpt) is a follow-up.
package ptysuspend

import (
	"fmt"
	"os"
)

// Supported reports that the pty-suspend proxy is not implemented here, so
// callers fall back to a normal launch.
func Supported() bool { return false }

// Run is unsupported on non-Linux platforms.
func Run(argv []string, outer *os.File) (int, error) {
	return 1, fmt.Errorf("ptysuspend: unsupported on this platform")
}
