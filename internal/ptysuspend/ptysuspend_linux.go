//go:build linux

// Package ptysuspend runs a child command on an inner pseudo-terminal while
// holding the outer (user-facing) terminal in raw mode, and intercepts a chosen
// "escape" key on the input stream BEFORE it reaches the child. On the escape
// key the proxy hands the terminal to a configured escape command (a shell, or
// `sc exec <session> $SHELL`), waits for it to exit, then resumes the child.
//
// This supplies the ctrl-z "escape to shell" UX that a full-screen TUI (e.g.
// claude-code) does not implement itself: such a TUI sets the terminal to raw
// mode (ISIG off), so ctrl-z is delivered to it as a byte and never escapes. By
// interposing a pty and reading the user's input first, clown reclaims the key
// for ANY downstream provider, uniformly whether clown is running inline or as
// the command inside a multiplexer pane (clown owns the pane process either
// way). The escape is a SHELL-OUT, not a job-control SIGTSTP: clown spawns the
// escape command itself, so it does not depend on a job-control shell parent or
// on the multiplexer — the mode-independent mechanism. See FDR-0017 ctrl-z recon.
package ptysuspend

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// DefaultEscapeKey is ctrl-z (0x1a) — the byte a terminal in canonical mode
// would translate into SIGTSTP; in a raw-mode TUI it is delivered verbatim, so
// the proxy intercepts it. Callers may override via Options.EscapeKey.
const DefaultEscapeKey = 0x1a

// Options configures the proxy.
type Options struct {
	// Enabled gates the whole proxy; callers also check Supported() + interactivity.
	Enabled bool
	// EscapeKey is the input byte intercepted as the escape trigger. Zero means
	// DefaultEscapeKey (ctrl-z).
	EscapeKey byte
	// EscapeArgv is the command run (with the terminal handed to it) when the
	// escape key is pressed; it runs in clown's cwd (the worktree). Empty means
	// the escape key is swallowed (no-op) rather than forwarded.
	EscapeArgv []string
}

func (o Options) escapeKey() byte {
	if o.EscapeKey == 0 {
		return DefaultEscapeKey
	}
	return o.EscapeKey
}

// Supported reports whether the pty-suspend proxy is implemented on this
// platform (linux). Callers gate on it so an enabled config degrades to a
// normal launch elsewhere rather than erroring.
func Supported() bool { return true }

// Run starts argv on a fresh inner pty, relays I/O to/from outer (the
// user-facing terminal, typically os.Stdin), and gives escape-to-shell
// semantics on opts.EscapeKey. It returns the child's exit code. outer MUST be
// an interactive terminal; callers should gate on that before calling.
func Run(argv []string, outer *os.File, opts Options) (int, error) {
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
	// Setsid + Setctty: the child leads its own session with the inner pts as its
	// controlling tty, so its raw-mode/ISIG changes land on the inner pty and its
	// job-control signals stay off the outer terminal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		slave.Close()
		return 1, fmt.Errorf("ptysuspend: start %q: %w", argv[0], err)
	}
	slave.Close() // the parent does not need the slave once the child holds it

	r := &relay{outer: outer, master: master, childPid: cmd.Process.Pid, escapeArgv: opts.EscapeArgv}
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

	// Forward a process-level SIGTERM to the child's process group (keyboard ^C
	// is a byte in raw mode and is relayed inline, so it needs no handler).
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

	// Child output -> user terminal (pausable while the escape command holds the
	// terminal). User input -> child, with the escape key intercepted.
	go r.copyOutput()
	go relayInput(r.outer, r.master, opts.escapeKey(), r.shellOut)

	err = cmd.Wait()
	return exitCode(err), nil
}

type relay struct {
	outer      *os.File
	master     *os.File
	origState  *term.State
	childPid   int
	escapeArgv []string
	// outMu serialises writes to the outer terminal so shellOut can pause the
	// child-output relay while the escape command owns the screen.
	outMu sync.Mutex
}

// copyOutput pumps child output (inner master) to the outer terminal, taking
// outMu around each write so shellOut can hold the screen. While paused the
// child's output accumulates in the pty buffer and is drained on resume.
func (r *relay) copyOutput() {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.master.Read(buf)
		if n > 0 {
			r.outMu.Lock()
			_, _ = r.outer.Write(buf[:n])
			r.outMu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// relayInput pumps in -> out, intercepting the escape key: bytes before it are
// forwarded, the escape key itself is NOT forwarded but invokes onEscape
// (synchronously — onEscape blocks until the escape command exits), and bytes
// after resume forwarding. The escape key is a single byte, so no cross-read
// scan state is needed. Split out as a free function (in/out as interfaces,
// onEscape as a hook) so the interception logic is unit-testable.
func relayInput(in io.Reader, out io.Writer, key byte, onEscape func()) {
	buf := make([]byte, 4096)
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			start := 0
			for i := 0; i < len(chunk); i++ {
				if chunk[i] == key {
					if i > start {
						_, _ = out.Write(chunk[start:i])
					}
					onEscape()
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

// shellOut hands the outer terminal to the configured escape command, waits for
// it to exit, then re-enters raw mode and repaints the child. Called from the
// input goroutine, so that goroutine is parked here (not reading the terminal)
// while the escape command reads it — no input contention. The child keeps
// running on the inner pty; copyOutput is paused via outMu so its output does
// not interleave with the escape command's screen, then drains on resume. This
// works identically inline and inside a multiplexer pane: clown spawns the
// escape command itself, with no dependence on a job-control shell parent.
func (r *relay) shellOut() {
	if len(r.escapeArgv) == 0 {
		return // escape key swallowed; nothing configured to run
	}

	r.outMu.Lock()
	defer r.outMu.Unlock()

	_ = term.Restore(int(r.outer.Fd()), r.origState)

	fmt.Fprintf(r.outer, "\r\n[clown] escape: %s — exit to resume\r\n", r.escapeArgv[0])
	cmd := exec.Command(r.escapeArgv[0], r.escapeArgv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = r.outer, r.outer, r.outer
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(r.outer, "\r\n[clown] escape command failed: %v\r\n", err)
	}

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
// claude's TUI) to redraw after resume — the screen is stale because the proxy
// drew nothing while the escape command held the terminal. A bare TIOCSWINSZ at
// the unchanged size can be a no-op for apps that only redraw on an actual size
// *change*, so we briefly perturb the size by one row and set it back (two
// resize events the app cannot coalesce away), then send SIGWINCH to the child.
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
