package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"code.linenisgreat.com/clown/internal/0/disconnectlog"
)

// endInputs are what the bridge waits on once it is serving: the events that
// end its life, and what it needs to tell them apart. run builds one from its
// real channels; a test builds one from channels it controls.
type endInputs struct {
	sig             <-chan os.Signal
	parentGone      <-chan struct{}
	childDone       chan error
	readerDone      <-chan struct{}
	stdinFail       <-chan struct{}
	serveErr        <-chan error
	shutdownNoticed func() bool
	rec             *recorder
	settle          time.Duration
	stderr          io.Writer
}

// endDecision is how the bridge ends: the exit status, and whether the child
// is still running and must be stopped.
type endDecision struct {
	exit           int
	terminateChild bool
}

// awaitEnd blocks until the first event that ends the bridge, writes the
// disconnect record for it, and says how to end.
func awaitEnd(in endInputs) endDecision {
	exit := 0
	terminateChild := false
	// reportChildExit is the child-exit path, shared by the exit itself and by
	// the pipe events that arrive just ahead of it.
	reportChildExit := func(err error) {
		if err != nil {
			fmt.Fprintf(in.stderr, "clown-stdio-bridge: wrapped child exited unexpectedly: %v\n", err)
		} else {
			fmt.Fprintln(in.stderr, "clown-stdio-bridge: wrapped child exited cleanly")
		}
		in.rec.record(disconnectlog.KindChildExit, disconnectlog.ClosedByChild, exitOf(err))
		exit = 1
	}
	// pipeEvent handles the child's stdout or stdin ending: a child that is
	// dying shows these within milliseconds of its exit, so the exit gets a
	// moment to arrive and is what the record then says; a child that is still
	// alive with a dead pipe is unusable, so the bridge records that and stops
	// it, as for an exit.
	pipeEvent := func(kind string) {
		if exited, err := awaitChild(in.childDone, in.settle); exited {
			reportChildExit(err)
			return
		}
		fmt.Fprintf(in.stderr, "clown-stdio-bridge: wrapped child is alive but its pipe ended (%s); stopping it\n", kind)
		in.rec.record(kind, disconnectlog.ClosedByChild, nil)
		terminateChild = true
		exit = 1
	}
	select {
	case sig := <-in.sig:
		fmt.Fprintf(in.stderr, "clown-stdio-bridge: received %s; shutting down\n", sig)
		// The host says so before the SIGTERM that ends a session; any other
		// signal is a disconnect worth a record.
		if !in.shutdownNoticed() {
			in.rec.record(disconnectlog.KindSignal, disconnectlog.ClosedByExternal, nil)
		}
		terminateChild = true
	case <-in.parentGone:
		fmt.Fprintln(in.stderr, "clown-stdio-bridge: parent clown is gone; shutting down the serve socket")
		in.rec.record(disconnectlog.KindParentGone, disconnectlog.ClosedByHost, nil)
		terminateChild = true
		exit = 1
	case err := <-in.childDone:
		// Re-fill the channel so terminate() doesn't block if it's
		// invoked elsewhere (defensive — it's not, in this path).
		in.childDone <- err
		reportChildExit(err)
	case <-in.readerDone:
		pipeEvent(disconnectlog.KindChildStdoutEOF)
	case <-in.stdinFail:
		pipeEvent(disconnectlog.KindChildStdinClosed)
	case err := <-in.serveErr:
		if err != nil {
			fmt.Fprintf(in.stderr, "clown-stdio-bridge: HTTP serve error: %v\n", err)
			in.rec.record(disconnectlog.KindHTTPServeError, disconnectlog.ClosedByBridge, nil)
			exit = 1
		}
		terminateChild = true
	}
	return endDecision{exit: exit, terminateChild: terminateChild}
}
