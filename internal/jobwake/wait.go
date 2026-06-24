package jobwake

import (
	"context"
	"errors"
	"os"
	"time"
)

// ErrJobNotFound is returned by WaitDone when the job has no journal at the
// moment the wait begins — the clown analogue of spinclass session-job-wait's
// "errors if no job was started" (clown#154). A job id that names a journal which
// has not been written is indistinguishable from a typo, so WaitDone refuses to
// block on it rather than hang until the caller's deadline.
var ErrJobNotFound = errors.New("jobwake: no such job")

// waitPollInterval is how often WaitDone re-reads a job's journal looking for a
// terminal record (TUNING LEVER). It matches the monitor's rescanInterval: the
// journal is the source of truth and 1s is the established re-scan granularity
// for this channel (RFC-0009 §9).
//
// WaitDone polls rather than riding the nudge socket for two reasons (clown#154):
// the per-channel nudge socket is a singleton owned by the live job-watch monitor
// (clown#132), so a second binder would fight the monitor for datagrams; and the
// waiter (the job-mcp process serving job_wait) is a different process from the
// producer that writes the terminal record, so an in-process completion channel
// like spinclass's job.WaitDone cannot reach across the boundary. A bounded poll
// inside one blocking call is still a true join — it never burns agent turns,
// which is the tight-poll cost #154 set out to remove.
const waitPollInterval = time.Second

// WaitDone blocks until the job identified by (target, jobID) has a terminal
// record in its journal, then returns that terminal record. It returns
// immediately if the job is already terminal. A job whose journal does not exist
// at wait start (or is reaped mid-wait) returns ErrJobNotFound rather than
// blocking. ctx cancellation (deadline or caller abort) returns ctx.Err(). An
// invalid job id wraps ErrInvalidJobID (via ReadJob).
//
// target selects the channel exactly as `clown job status --target` does (empty
// => current session). A standalone `message`/`chat` job has no terminal record
// and so never satisfies WaitDone — it is for lifecycle jobs (started + a
// terminal), the join surface moxy's async dispatch needs (clown#154).
func WaitDone(ctx context.Context, target, jobID string) (Record, error) {
	return waitDoneChannel(ctx, ChannelID(resolveSession(target)), jobID, waitPollInterval)
}

// waitDoneChannel is WaitDone for an already-resolved channel id, with the poll
// interval injected so tests need not wait a full second. The first read doubles
// as the unknown-job guard: a missing journal at wait start is ErrJobNotFound,
// not a state to wait for.
func waitDoneChannel(ctx context.Context, cid, jobID string, interval time.Duration) (Record, error) {
	if r, err := pollTerminal(cid, jobID); err != nil || r != nil {
		if err != nil {
			return Record{}, err
		}
		return *r, nil
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return Record{}, ctx.Err()
		case <-ticker.C:
			if r, err := pollTerminal(cid, jobID); err != nil || r != nil {
				if err != nil {
					return Record{}, err
				}
				return *r, nil
			}
		}
	}
}

// pollTerminal reads the job's journal once and returns its terminal record if
// present. A missing journal is reported as ErrJobNotFound; a present journal
// with no terminal yet returns (nil, nil) so the caller keeps waiting. An invalid
// job id surfaces from ReadJob (wrapping ErrInvalidJobID).
func pollTerminal(cid, jobID string) (*Record, error) {
	recs, err := ReadJob(cid, jobID)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrJobNotFound
		}
		return nil, err
	}
	for _, r := range recs {
		if IsTerminal(r.Type) {
			r := r
			return &r, nil
		}
	}
	return nil, nil
}
