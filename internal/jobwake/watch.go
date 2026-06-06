package jobwake

import (
	"context"
	"os"
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
	// One-shot journal GC + ack reaping at monitor start (clown#113).
	// Best-effort by construction; runs neither on ticks nor in ReplayOnce.
	sweep(cid, time.Now())
	if err := serviceChannels(cid, emit); err != nil {
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
			if err := serviceChannels(cid, emit); err != nil {
				return err
			}
		case <-ticker.C:
			if err := serviceChannels(cid, emit); err != nil {
				return err
			}
		}
	}
}

// ReplayOnce emits every unacked waking event for the session's own channel
// and the broadcast channel once, advancing the respective ack cursors, and
// returns without binding the nudge socket or blocking. It backs
// `clown job-watch --once` (the conformance suite and pull-style replay); the
// long-running monitor uses Watch.
func ReplayOnce(sessionKey string, emit func(Record) error) error {
	return serviceChannels(ChannelID(sessionKey), emit)
}

// serviceChannels runs one monitor cycle for a reader (RFC-0009 §9): replay
// the reader's own channel against its `.ack.json`, then the broadcast channel
// against the reader's per-reader ack file. The nudge socket stays
// own-channel-only; broadcast delivery rides the periodic rescan.
func serviceChannels(cid string, emit func(Record) error) error {
	if err := emitUnacked(cid, AckFile(cid), emit); err != nil {
		return err
	}
	return serviceBroadcast(cid, emit)
}

// serviceBroadcast replays the broadcast channel for one reader with condvar
// semantics (RFC-0009 §9): on first attach — the reader's ack file does not
// exist (os.Stat, not corrupt-loads-empty) — initialize at current end by
// persisting an ack map covering every existing waking record WITHOUT
// emitting; thereafter normal replay-unacked applies.
func serviceBroadcast(readerCID string, emit func(Record) error) error {
	bcid := ChannelID(BroadcastKey)
	ackPath := AckFileFor(bcid, readerCID)
	if _, err := os.Stat(ackPath); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		return initBroadcastAck(bcid, ackPath)
	}
	return emitUnacked(bcid, ackPath, emit)
}

// initBroadcastAck persists a first-attach ack covering every waking record
// already in the broadcast channel, without emitting any of them. The channel
// dir is created (mode 0700) if needed so the ack file persists even when the
// broadcast channel is empty — a restart must not look like first attach
// again (RFC-0009 §9).
func initBroadcastAck(bcid, ackPath string) error {
	waking, err := scanWaking(bcid)
	if err != nil {
		return err
	}
	a := ack{V: 1, Acked: map[string]int{}}
	for _, r := range waking {
		if prev, ok := a.Acked[r.Job]; !ok || r.Seq > prev {
			a.Acked[r.Job] = r.Seq
		}
	}
	if err := os.MkdirAll(JournalDir(bcid), 0o700); err != nil {
		return err
	}
	return saveAckPath(ackPath, a)
}

// emitUnacked emits every waking record whose seq exceeds the acked seq for
// its job, oldest first, advancing the ack persisted at ackPath after each
// successful emit (at-least-once: persist follows emit).
func emitUnacked(cid, ackPath string, emit func(Record) error) error {
	a := loadAckPath(ackPath)
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
		if err := saveAckPath(ackPath, a); err != nil { // persist after emit => at-least-once
			return err
		}
	}
	return nil
}
