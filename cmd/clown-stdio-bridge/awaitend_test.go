package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/0/disconnectlog"
)

// endHarness is awaitEnd's inputs over channels the test controls, a recorder
// that writes to a temp directory, and a buffer for what the bridge says on
// stderr.
type endHarness struct {
	in              endInputs
	sig             chan os.Signal
	parentGone      chan struct{}
	childDone       chan error
	readerDone      chan struct{}
	stdinFail       chan struct{}
	serveErr        chan error
	shutdownNoticed bool
	stderr          *bytes.Buffer
	root            string
}

func newEndHarness(t *testing.T) *endHarness {
	t.Helper()
	h := &endHarness{
		sig:        make(chan os.Signal, 1),
		parentGone: make(chan struct{}),
		childDone:  make(chan error, 1),
		readerDone: make(chan struct{}),
		stdinFail:  make(chan struct{}, 1),
		serveErr:   make(chan error, 1),
		stderr:     &bytes.Buffer{},
		root:       t.TempDir(),
	}
	tr := newTranslator(io.Discard, strings.NewReader(""), nullLogger{})
	h.in = endInputs{
		sig:             h.sig,
		parentGone:      h.parentGone,
		childDone:       h.childDone,
		readerDone:      h.readerDone,
		stdinFail:       h.stdinFail,
		serveErr:        h.serveErr,
		shutdownNoticed: func() bool { return h.shutdownNoticed },
		rec:             newRecorder(recorderEnv(h.root), 4242, tr, disconnectlog.NewRing(), nullLogger{}),
		settle:          30 * time.Millisecond,
		stderr:          h.stderr,
	}
	return h
}

// records is the kinds and who-closed-it of the records written, oldest first.
func (h *endHarness) records(t *testing.T) []disconnectlog.Record {
	t.Helper()
	var out []disconnectlog.Record
	for _, e := range disconnectlog.List(h.root, recorderTestSession, "cutting-garden", "cg") {
		raw, err := disconnectlog.Read(h.root, recorderTestSession, "cutting-garden", "cg", e.N)
		if err != nil {
			t.Fatal(err)
		}
		var r disconnectlog.Record
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		out = append([]disconnectlog.Record{r}, out...)
	}
	return out
}

func (h *endHarness) wantOneRecord(t *testing.T, kind, closedBy string) disconnectlog.Record {
	t.Helper()
	recs := h.records(t)
	if len(recs) != 1 || recs[0].Kind != kind || recs[0].ClosedBy != closedBy {
		t.Fatalf("records = %+v; want one %s closed by %s", recs, kind, closedBy)
	}
	return recs[0]
}

// A signal the host did not announce is a disconnect: recorded, child stopped,
// exit 0 as before.
func TestAwaitEndRecordsAnUnannouncedSignal(t *testing.T) {
	h := newEndHarness(t)
	h.sig <- syscall.SIGTERM
	got := awaitEnd(h.in)
	if got != (endDecision{exit: 0, terminateChild: true}) {
		t.Errorf("decision = %+v", got)
	}
	h.wantOneRecord(t, disconnectlog.KindSignal, disconnectlog.ClosedByExternal)
	if !strings.Contains(h.stderr.String(), "received terminated; shutting down") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

// After POST /clown/shutdown the host's SIGTERM is a stop on purpose: no record.
func TestAwaitEndWritesNoRecordForAnAnnouncedSignal(t *testing.T) {
	h := newEndHarness(t)
	h.shutdownNoticed = true
	h.sig <- syscall.SIGTERM
	if got := awaitEnd(h.in); got != (endDecision{exit: 0, terminateChild: true}) {
		t.Errorf("decision = %+v", got)
	}
	if recs := h.records(t); len(recs) != 0 {
		t.Errorf("a stop on purpose wrote %+v", recs)
	}
}

func TestAwaitEndRecordsTheChildExitAndLeavesItForTerminate(t *testing.T) {
	h := newEndHarness(t)
	h.childDone <- errors.New("exit status 3")
	got := awaitEnd(h.in)
	if got != (endDecision{exit: 1, terminateChild: false}) {
		t.Errorf("decision = %+v", got)
	}
	h.wantOneRecord(t, disconnectlog.KindChildExit, disconnectlog.ClosedByChild)
	if len(h.childDone) != 1 {
		t.Error("the exit status must stay in childDone for terminate and the caller")
	}
	if !strings.Contains(h.stderr.String(), "wrapped child exited unexpectedly: exit status 3") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

func TestAwaitEndSaysACleanChildExitWasClean(t *testing.T) {
	h := newEndHarness(t)
	h.childDone <- nil
	awaitEnd(h.in)
	if !strings.Contains(h.stderr.String(), "exited cleanly") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
	if rec := h.wantOneRecord(t, disconnectlog.KindChildExit, disconnectlog.ClosedByChild); rec.ChildExit == nil || rec.ChildExit.Code != 0 {
		t.Errorf("child_exit = %+v; want exit 0", rec.ChildExit)
	}
}

// A dying child shows a pipe event just ahead of its exit; the exit is what the
// record then says.
func TestAwaitEndPrefersTheExitOverAPipeEventJustAheadOfIt(t *testing.T) {
	for name, fire := range map[string]func(*endHarness){
		"stdout eof":  func(h *endHarness) { close(h.readerDone) },
		"stdin close": func(h *endHarness) { h.stdinFail <- struct{}{} },
	} {
		t.Run(name, func(t *testing.T) {
			h := newEndHarness(t)
			h.childDone <- errors.New("killed")
			fire(h)
			got := awaitEnd(h.in)
			if got != (endDecision{exit: 1, terminateChild: false}) {
				t.Errorf("decision = %+v", got)
			}
			h.wantOneRecord(t, disconnectlog.KindChildExit, disconnectlog.ClosedByChild)
		})
	}
}

// A pipe that ended on a child that is still alive makes the child unusable:
// recorded as that, and the child is stopped.
func TestAwaitEndStopsAChildWhosePipeEndedButWhoLives(t *testing.T) {
	cases := map[string]struct {
		fire func(*endHarness)
		kind string
	}{
		"stdout eof":  {func(h *endHarness) { close(h.readerDone) }, disconnectlog.KindChildStdoutEOF},
		"stdin close": {func(h *endHarness) { h.stdinFail <- struct{}{} }, disconnectlog.KindChildStdinClosed},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newEndHarness(t)
			c.fire(h)
			got := awaitEnd(h.in)
			if got != (endDecision{exit: 1, terminateChild: true}) {
				t.Errorf("decision = %+v", got)
			}
			h.wantOneRecord(t, c.kind, disconnectlog.ClosedByChild)
			if !strings.Contains(h.stderr.String(), "alive but its pipe ended ("+c.kind+")") {
				t.Errorf("stderr = %q", h.stderr.String())
			}
		})
	}
}

func TestAwaitEndRecordsTheParentGoing(t *testing.T) {
	h := newEndHarness(t)
	close(h.parentGone)
	if got := awaitEnd(h.in); got != (endDecision{exit: 1, terminateChild: true}) {
		t.Errorf("decision = %+v", got)
	}
	h.wantOneRecord(t, disconnectlog.KindParentGone, disconnectlog.ClosedByHost)
}

func TestAwaitEndRecordsAFailedHTTPServerButNotItsCleanClose(t *testing.T) {
	h := newEndHarness(t)
	h.serveErr <- errors.New("accept: boom")
	if got := awaitEnd(h.in); got != (endDecision{exit: 1, terminateChild: true}) {
		t.Errorf("decision = %+v", got)
	}
	h.wantOneRecord(t, disconnectlog.KindHTTPServeError, disconnectlog.ClosedByBridge)

	clean := newEndHarness(t)
	close(clean.serveErr) // the serve goroutine closes it on a clean shutdown
	if got := awaitEnd(clean.in); got != (endDecision{exit: 0, terminateChild: true}) {
		t.Errorf("clean close: decision = %+v", got)
	}
	if recs := clean.records(t); len(recs) != 0 {
		t.Errorf("a clean close wrote %+v", recs)
	}
}

// With no recorder (no identity from the host) every path still decides the
// same and nothing panics.
func TestAwaitEndWithoutARecorderDecidesTheSame(t *testing.T) {
	h := newEndHarness(t)
	h.in.rec = nil
	h.sig <- syscall.SIGINT
	if got := awaitEnd(h.in); got != (endDecision{exit: 0, terminateChild: true}) {
		t.Errorf("decision = %+v", got)
	}
}
