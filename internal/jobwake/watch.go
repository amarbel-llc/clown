package jobwake

import (
	"context"
	"time"
)

// rescanInterval is the safety-net re-scan cadence against lost nudges (TUNING
// LEVER, RFC-0009 §9 / FDR-0013). 1s matches spinclass chat-watch parity.
const rescanInterval = time.Second

// Watch runs the channel monitor (RFC-0009 §9): it replays unacked waking
// events, binds the nudge socket, then loops on datagram-or-ticker re-scanning,
// emitting each new waking event exactly once via emit. The ack is persisted
// AFTER emit, so a crash between the two re-emits the event (at-least-once). It
// returns nil on a clean ctx cancel.
func Watch(ctx context.Context, sessionKey string, emit func(Record) error) error {
	cid := ChannelID(sessionKey)
	if err := emitUnacked(cid, emit); err != nil {
		return err
	}
	conn, err := bindNudge(cid)
	if err != nil {
		return err
	}
	defer func() {
		conn.Close()
		_ = removeSocket(cid)
	}()

	datagrams := make(chan struct{}, 64)
	go func() {
		buf := make([]byte, 512)
		for {
			if _, _, err := conn.ReadFromUnix(buf); err != nil {
				return // conn closed on ctx cancel
			}
			select {
			case datagrams <- struct{}{}:
			default:
			}
		}
	}()

	ticker := time.NewTicker(rescanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-datagrams:
			if err := emitUnacked(cid, emit); err != nil {
				return err
			}
		case <-ticker.C:
			if err := emitUnacked(cid, emit); err != nil {
				return err
			}
		}
	}
}

// ReplayOnce emits every unacked waking event for the session's channel once,
// advancing the ack cursor, and returns without binding the nudge socket or
// blocking. It backs `clown job-watch --once` (the conformance suite and
// pull-style replay); the long-running monitor uses Watch.
func ReplayOnce(sessionKey string, emit func(Record) error) error {
	return emitUnacked(ChannelID(sessionKey), emit)
}

// emitUnacked emits every waking record whose seq exceeds the acked seq for its
// job, oldest first, advancing the persisted ack after each successful emit.
func emitUnacked(cid string, emit func(Record) error) error {
	a := loadAck(cid)
	waking, err := scanWaking(cid)
	if err != nil {
		return err
	}
	for _, r := range waking {
		if prev, ok := a.Acked[r.Job]; ok && r.Seq <= prev {
			continue
		}
		if err := emit(r); err != nil {
			return err
		}
		a.Acked[r.Job] = r.Seq
		if err := saveAck(cid, a); err != nil { // persist after emit => at-least-once
			return err
		}
	}
	return nil
}
