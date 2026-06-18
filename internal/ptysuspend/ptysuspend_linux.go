//go:build linux

// Package ptysuspend runs a child command on an inner pseudo-terminal while
// holding the outer (user-facing) terminal in raw mode, and intercepts the
// ctrl-z byte (0x1A) on the input stream BEFORE it reaches the child. On ctrl-z
// the proxy restores the outer terminal and raises SIGTSTP on its own process
// group, so the launching shell suspends it and the user lands back at a shell;
// `fg` resumes the proxy (re-enters raw mode and keeps relaying).
//
// This supplies the ctrl-z "escape to shell" handler that a full-screen TUI
// (e.g. claude-code) does not implement itself: such a TUI sets the terminal to
// raw mode (ISIG off), so ctrl-z is delivered to it as a byte and never becomes
// SIGTSTP. By interposing a pty and reading the user's input first, clown
// reclaims ctrl-z for job control without the child's cooperation, for ANY
// downstream provider. See clown's FDR-0017 ctrl-z recon.
package ptysuspend

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// ctrlZ is the byte a terminal in canonical mode would translate into SIGTSTP.
// In raw mode it is delivered verbatim; the proxy intercepts it here.
const ctrlZ = 0x1a

// Supported reports whether the pty-suspend proxy is implemented on this
// platform (linux). Callers gate on it so an enabled config degrades to a
// normal launch elsewhere rather than erroring.
func Supported() bool { return true }

// Run starts argv on a fresh inner pty, relays I/O to/from outer (the
// user-facing terminal, typically os.Stdin), and gives ctrl-z escape-to-shell
// semantics. It returns the child's exit code. outer MUST be an interactive
// terminal; callers should gate on that before calling.
func Run(argv []string, outer *os.File) (int, error) {
	if len(argv) == 0 {
		return 1, fmt.Errorf("ptysuspend: empty argv")
	}

	master, slave, err := openInnerPTY()
	if err != nil {
		return 1, fmt.Errorf("ptysuspend: allocate pty: %w", err)
	}
	defer master.Close()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// Setsid: the child leads its OWN session, so when the proxy raises SIGTSTP
	// on its own process group the child is unaffected and keeps running.
	// Setctty: the child adopts the inner pts (fd 0) as its controlling tty, so
	// its own raw-mode/ISIG changes land on the inner pty, not the outer one.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		slave.Close()
		return 1, fmt.Errorf("ptysuspend: start %q: %w", argv[0], err)
	}
	// The parent does not need the slave once the child holds it.
	slave.Close()

	r := &relay{outer: outer, master: master, childPid: cmd.Process.Pid}
	r.origState, err = term.MakeRaw(int(outer.Fd()))
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return 1, fmt.Errorf("ptysuspend: raw mode: %w", err)
	}
	defer term.Restore(int(outer.Fd()), r.origState)
	r.syncWinsize()

	// Propagate terminal resizes to the inner pty.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			r.syncWinsize()
		}
	}()

	// Forward a process-level SIGTERM to the child's process group (keyboard
	// ^C is a byte in raw mode and is relayed inline, so it needs no handler).
	term3 := make(chan os.Signal, 1)
	signal.Notify(term3, syscall.SIGTERM)
	defer signal.Stop(term3)
	go func() {
		for s := range term3 {
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, s.(syscall.Signal))
			}
		}
	}()

	// Child output -> user terminal.
	go func() { _, _ = io.Copy(outer, master) }()
	// User input -> child, with ctrl-z intercepted and turned into suspend.
	go relayInput(r.outer, r.master, r.suspend)

	err = cmd.Wait()
	return exitCode(err), nil
}

type relay struct {
	outer     *os.File
	master    *os.File
	origState *term.State
	childPid  int
}

// relayInput pumps in -> out, intercepting ctrl-z (0x1a): bytes before a ctrl-z
// are forwarded, the ctrl-z itself is NOT forwarded but invokes onCtrlZ
// (synchronously — onCtrlZ blocks until the session resumes), and bytes after
// resume forwarding. ctrl-z is a single byte, so no cross-read scan state is
// needed. Split out as a free function (in/out as interfaces, onCtrlZ as a
// hook) so the interception logic is unit-testable without a real terminal.
func relayInput(in io.Reader, out io.Writer, onCtrlZ func()) {
	buf := make([]byte, 4096)
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			start := 0
			for i := 0; i < len(chunk); i++ {
				if chunk[i] == ctrlZ {
					if i > start {
						_, _ = out.Write(chunk[start:i])
					}
					onCtrlZ()
					start = i + 1
				}
			}
			if start < len(chunk) {
				_, _ = out.Write(chunk[start:])
			}
		}
		if rerr != nil {
			return
		}
	}
}

// suspend restores the outer terminal, stops the proxy's own process group
// (returning the user to the launching shell), and on SIGCONT (`fg`) re-enters
// raw mode and forces the child to repaint. Mirrors charmbracelet/bubbletea's
// suspendProcess. The child, in its own session, keeps running throughout —
// while the proxy is stopped it is not draining the child's output, so a child
// that writes a lot mid-suspend pauses on a full pty buffer (no data loss; the
// kernel preserves byte order and the proxy drains it on resume). For an
// interactive TUI idle at a prompt — the usual ctrl-z moment — there is nothing
// to drain. Eliminating the pause entirely would need a tmux-style separate
// drainer process; see the package's follow-ups.
func (r *relay) suspend() {
	_ = term.Restore(int(r.outer.Fd()), r.origState)

	cont := make(chan os.Signal, 1)
	signal.Notify(cont, syscall.SIGCONT)
	defer signal.Stop(cont)

	_ = syscall.Kill(0, syscall.SIGTSTP) // stop our process group; blocks until SIGCONT
	<-cont

	if st, err := term.MakeRaw(int(r.outer.Fd())); err == nil {
		r.origState = st
	}
	r.repaintChild()
}

// syncWinsize copies the outer terminal's window size onto the inner pty
// (TIOCSWINSZ on the master propagates SIGWINCH to the child).
func (r *relay) syncWinsize() {
	ws, err := unix.IoctlGetWinsize(int(r.outer.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return
	}
	_ = unix.IoctlSetWinsize(int(r.master.Fd()), unix.TIOCSWINSZ, ws)
}

// repaintChild re-syncs the inner pty window size and forces the child (e.g.
// claude's TUI) to redraw after a resume — the screen is stale because the
// proxy drew nothing while suspended. A bare TIOCSWINSZ at the unchanged size
// can be a no-op for apps that only redraw on an actual size *change*, so we
// briefly perturb the size by one row and set it back (two resize events the
// app cannot coalesce away), then send SIGWINCH to the child's process group.
func (r *relay) repaintChild() {
	ws, err := unix.IoctlGetWinsize(int(r.outer.Fd()), unix.TIOCGWINSZ)
	if err == nil {
		if ws.Row > 1 {
			perturbed := *ws
			perturbed.Row--
			_ = unix.IoctlSetWinsize(int(r.master.Fd()), unix.TIOCSWINSZ, &perturbed)
		}
		_ = unix.IoctlSetWinsize(int(r.master.Fd()), unix.TIOCSWINSZ, ws)
	}
	if r.childPid > 0 {
		_ = syscall.Kill(-r.childPid, syscall.SIGWINCH)
	}
}

// openInnerPTY allocates a Linux pseudo-terminal pair via /dev/ptmx (no
// creack/pty dependency): unlock the master (TIOCSPTLCK=0), read the pts index
// (TIOCGPTN), and open the matching /dev/pts/<n> slave.
func openInnerPTY() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		return nil, nil, err
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		m.Close()
		return nil, nil, err
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		return nil, nil, err
	}
	return m, s, nil
}

// exitCode extracts a process exit code from exec.Cmd.Wait's error.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}
