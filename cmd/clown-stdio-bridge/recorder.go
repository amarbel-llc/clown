package main

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"code.linenisgreat.com/clown/internal/0/disconnectlog"
)

// The environment the plugin host gives a bridge so it can write a disconnect
// record (clown#344): the session (CLOWN_SESSION_ID, already part of every
// managed server's identity), the plugin the bridge belongs to, where records
// go, and the host's own log path for the record's pointer. The server name is
// CLOWN_BRIDGE_SERVER_NAME (the statsd prefix). Without a record directory,
// a plugin name and a session the bridge writes no record and behaves as
// before.
const (
	envSession    = "CLOWN_SESSION_ID"
	envPlugin     = "CLOWN_BRIDGE_PLUGIN_NAME"
	envServer     = "CLOWN_BRIDGE_SERVER_NAME"
	envRecordDir  = "CLOWN_BRIDGE_RECORD_DIR"
	envHostLog    = "CLOWN_BRIDGE_HOST_LOG"
	envBridgeNote = "clown-stdio-bridge: disconnect record"
)

// recorder writes the one disconnect record of a bridge's life. The first
// event wins: a dying child shows up as an exit, a stdout EOF and a stdin
// failure within milliseconds, and the record is of the first reading the
// caller settled on.
type recorder struct {
	root, session, plugin, server, hostLog string
	bridgePID, childPID                    int
	started                                time.Time
	tr                                     *translator
	tail                                   *disconnectlog.Ring
	logger                                 logger

	once sync.Once
	rec  disconnectlog.Record
}

// newRecorder reads the identity from getenv and returns nil (a no-op
// recorder) when any of the session, the plugin or the record directory is
// missing.
func newRecorder(getenv func(string) string, childPID int, tr *translator, tail *disconnectlog.Ring, log logger) *recorder {
	r := &recorder{
		root:      getenv(envRecordDir),
		session:   getenv(envSession),
		plugin:    getenv(envPlugin),
		server:    getenv(envServer),
		hostLog:   getenv(envHostLog),
		bridgePID: os.Getpid(),
		childPID:  childPID,
		started:   time.Now(),
		tr:        tr,
		tail:      tail,
		logger:    log,
	}
	if r.root == "" || r.session == "" || r.plugin == "" || r.server == "" {
		return nil
	}
	return r
}

// record writes the record for kind and fails every request still waiting
// with an error that ends in the record's URI. It returns the one-line
// summary, "" when this recorder is nil, has already recorded, or could not
// write (the failure is logged and the requests are still failed, without a
// URI). childExit is how the child ended, nil when it was still alive.
func (r *recorder) record(kind, closedBy string, childExit *disconnectlog.Exit) string {
	if r == nil {
		return ""
	}
	var summary string
	r.once.Do(func() {
		live, abandoned := r.tr.inFlight()
		depth, dropped := r.tr.queueStats()
		rec := disconnectlog.Record{
			Session:         r.session,
			Plugin:          r.plugin,
			Server:          r.server,
			Kind:            kind,
			ClosedBy:        closedBy,
			BridgePID:       r.bridgePID,
			ChildPID:        r.childPID,
			ChildExit:       childExit,
			UptimeMS:        time.Since(r.started).Milliseconds(),
			InFlight:        live,
			Abandoned:       abandoned,
			QueueDepth:      depth,
			DroppedOutbound: dropped,
			StderrTail:      r.tail.Snapshot(),
			HostLog:         r.hostLog,
		}
		written, err := disconnectlog.Write(r.root, rec)
		if err != nil {
			r.logger.Printf("%s not written: %v", envBridgeNote, err)
			summary = "plugin " + r.plugin + "/" + r.server + " disconnected (" + kind + "); the disconnect record could not be written"
		} else {
			r.rec = written
			summary = disconnectlog.Summary(written)
			r.logger.Printf("%s %d: %s", envBridgeNote, written.N, written.URI)
		}
		if n := r.tr.failAll(summary); n > 0 {
			r.logger.Printf("clown-stdio-bridge: failed %d in-flight request(s) with the disconnect summary", n)
		}
	})
	return summary
}

// exitOf reads how a child ended from cmd.Wait's error: a nil error is exit 0,
// an *exec.ExitError carries the code and, for a signal, the signal's name.
func exitOf(err error) *disconnectlog.Exit {
	if err == nil {
		return &disconnectlog.Exit{Code: 0}
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return &disconnectlog.Exit{Code: -1}
	}
	x := &disconnectlog.Exit{Code: ee.ExitCode()}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		x.Signal = ws.Signal().String()
	}
	return x
}

// teeStderr copies the child's stderr to dst unchanged, so the host's log is as
// before, and keeps a scrubbed tail of its lines. It returns at EOF. A line
// longer than the scanner's bound is cut for the tail only; dst still gets
// every byte.
func teeStderr(src io.Reader, dst io.Writer, tail *disconnectlog.Ring) {
	br := bufio.NewReaderSize(src, 64<<10)
	var line []byte
	flush := func() {
		if len(line) > 0 {
			tail.Add(string(line))
			line = line[:0]
		}
	}
	for {
		chunk, isPrefix, err := br.ReadLine()
		if len(chunk) > 0 {
			_, _ = dst.Write(chunk)
			if len(line) < 4*disconnectlog.StderrLineBytes {
				line = append(line, chunk...)
			}
		}
		if err != nil {
			flush()
			return
		}
		if !isPrefix {
			_, _ = dst.Write([]byte{'\n'})
			flush()
		}
	}
}
