package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"code.linenisgreat.com/clown/internal/staging"
)

// A session ends itself in two halves (spinclass#389): its close-this-session
// tool only records a request, and `spinclass close-self-run` does the teardown,
// which stops the agent unit and so cannot run inside it. clown#287 is the
// trigger: a Stop hook in the agent pokes the frontend over a unix datagram
// socket, and the frontend — outside the unit — runs close-self-run. The runner
// is a no-op without a request and re-checks everything itself, so poking it on
// every idle transition is safe, and nothing here needs to authenticate the
// poke: forging one can only run a no-op.

// idleSocketPath is where the frontend listens for idle pokes.
func idleSocketPath(runtimeDir, key string) string {
	return filepath.Join(runtimeDir, "clown", "idle", key+".sock")
}

// selfCloseArmed reports whether this launch should listen for idle pokes, and
// the spinclass binary to run. It needs a claude launch (the Stop hook is a
// plugin hook) inside a spinclass session, a runtime dir for the socket and
// spinclass on PATH. --print-launch-plan never arms: it writes no state.
func selfCloseArmed(flags parsedFlags) (spinclassPath string, ok bool) {
	if flags.provider != "claude" || flags.naked || flags.printLaunchPlan || flags.identity.Key == "" {
		return "", false
	}
	if os.Getenv("SPINCLASS_SESSION_ID") == "" || os.Getenv("XDG_RUNTIME_DIR") == "" {
		return "", false
	}
	path, err := exec.LookPath("spinclass")
	if err != nil {
		return "", false
	}
	return path, true
}

// selfCloseRunner runs `spinclass close-self-run` for one session.
type selfCloseRunner struct {
	bin       string
	principal string
	logPath   string
}

// run starts close-self-run with the principal in CLOWN_SESSION_ID and waits for
// it. A non-zero exit is spinclass refusing (it has already told the holders),
// so it is logged, not returned; only failing to start is an error.
//
// The runner gets its own session so it outlives the frontend: the teardown it
// does stops the unit, which ends this process. Its output goes to a log file,
// never the terminal claude owns, and never a pipe the exiting frontend would
// break under it.
func (r *selfCloseRunner) run() error {
	if err := os.MkdirAll(filepath.Dir(r.logPath), 0o700); err != nil {
		return err
	}
	log, err := os.OpenFile(r.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	fmt.Fprintf(log, "%s close-self-run %s\n", time.Now().UTC().Format(time.RFC3339), r.principal)

	cmd := exec.Command(r.bin, "close-self-run")
	cmd.Env = append(os.Environ(), "CLOWN_SESSION_ID="+r.principal)
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(log, "start: %v\n", err)
		return err
	}
	if err := cmd.Wait(); err != nil {
		fmt.Fprintf(log, "%v\n", err)
	}
	return nil
}

// idleListener receives idle pokes on a unixgram socket and runs a function
// once per poke, serially. Pokes that arrive during a run collapse into one
// more run: spinclass's runner is not safe to overlap.
type idleListener struct {
	conn *net.UnixConn
	path string
	wake chan struct{}
	once sync.Once
}

func startIdleListener(path string, run func()) (*idleListener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		conn.Close()
		return nil, err
	}
	l := &idleListener{conn: conn, path: path, wake: make(chan struct{}, 1)}
	go func() {
		buf := make([]byte, 64)
		for {
			if _, _, err := conn.ReadFromUnix(buf); err != nil {
				close(l.wake)
				return
			}
			select {
			case l.wake <- struct{}{}:
			default:
			}
		}
	}()
	go func() {
		for range l.wake {
			run()
		}
	}()
	return l, nil
}

// Close stops listening and removes the socket. A run already in flight is left
// to finish: killing a half-done teardown would strand the session.
func (l *idleListener) Close() {
	l.once.Do(func() {
		l.conn.Close()
		_ = os.Remove(l.path)
	})
}

// runIdleNotify is `clown idle-notify SOCKET`, the Stop hook's command: it
// sends one datagram to the frontend and always exits 0, so it can never fail
// or stall the agent's turn.
func runIdleNotify(args []string) int {
	if len(args) != 1 {
		return 0
	}
	conn, err := net.DialTimeout("unixgram", args[0], 250*time.Millisecond)
	if err != nil {
		return 0
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(250 * time.Millisecond))
	_, _ = conn.Write([]byte{1})
	return 0
}

// synthIdlePluginDir writes a built-in plugin directory whose hooks.json runs
// `clown idle-notify SOCK` on Stop, and returns its path. The staging root owns
// the directory.
func synthIdlePluginDir(root *staging.Root, clownBin, sock string) (string, error) {
	dir, err := root.Dir("clown-selfclose-plugin-*")
	if err != nil {
		return "", err
	}
	manifestDir := filepath.Join(dir, ".claude-plugin")
	hooksDir := filepath.Join(dir, "hooks")
	for _, d := range []string{manifestDir, hooksDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", err
		}
	}
	manifest, err := json.MarshalIndent(map[string]any{"name": "clown-selfclose", "version": "1"}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), manifest, 0o600); err != nil {
		return "", err
	}
	hooks, err := json.MarshalIndent(map[string]any{"hooks": map[string]any{
		"Stop": []any{map[string]any{"hooks": []any{map[string]any{
			"type":    "command",
			"command": clownBin + " idle-notify " + sock,
			"timeout": 5,
		}}}},
	}}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), hooks, 0o600); err != nil {
		return "", err
	}
	return dir, nil
}

// armSelfClose starts the idle listener and registers the Stop hook for a
// launch that selfCloseArmed allows, returning the plugin dir to add and the
// socket to bind into an agent unit ("" when not armed or on any failure: a
// session that cannot self-close is still a working session).
func armSelfClose(flags parsedFlags, root *staging.Root) (pluginDir, sock string, closeFn func()) {
	bin, ok := selfCloseArmed(flags)
	if !ok {
		return "", "", func() {}
	}
	sock = idleSocketPath(os.Getenv("XDG_RUNTIME_DIR"), flags.identity.Key)
	logPath, err := userStateWritePath("close-self-run.log")
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: self-close not armed: %v\n", err)
		return "", "", func() {}
	}
	runner := &selfCloseRunner{bin: bin, principal: flags.identity.Key, logPath: logPath}
	l, err := startIdleListener(sock, func() { _ = runner.run() })
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: self-close not armed: %v\n", err)
		return "", "", func() {}
	}
	dir, err := synthIdlePluginDir(root, flags.identity.Bin, sock)
	if err != nil {
		l.Close()
		fmt.Fprintf(os.Stderr, "clown: self-close not armed: %v\n", err)
		return "", "", func() {}
	}
	return dir, sock, l.Close
}
