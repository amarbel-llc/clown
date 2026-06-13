package jobwake

import (
	"context"
	"os"
	"time"
)

// rescanInterval is the safety-net re-scan cadence against lost nudges (TUNING
// LEVER, RFC-0009 §9 / FDR-0013). 1s matches spinclass chat-watch parity.
const rescanInterval = time.Second

// sweepInterval is how often the monitor runs the GC sweep while it lives
// (TUNING LEVER, RFC-0010 §4). The sweep also runs once at start; without a
// periodic cadence a long-lived monitor on an always-on host would almost never
// GC (clown#126).
const sweepInterval = time.Hour

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
	if err := serviceChannels(cid, sessionKey, emit); err != nil {
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
	sweepTicker := time.NewTicker(sweepInterval)
	defer sweepTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-datagrams:
			if err := serviceChannels(cid, sessionKey, emit); err != nil {
				return err
			}
		case <-ticker.C:
			if err := serviceChannels(cid, sessionKey, emit); err != nil {
				return err
			}
		case <-sweepTicker.C:
			sweep(cid, time.Now()) // periodic GC so a long-lived monitor keeps reaping (clown#126)
		}
	}
}

// ReplayOnce emits every unacked waking event for the session's own channel
// and the broadcast channel once, advancing the respective ack cursors, and
// returns without binding the nudge socket or blocking. It backs
// `clown job-watch --once` (the conformance suite and pull-style replay); the
// long-running monitor uses Watch.
func ReplayOnce(sessionKey string, emit func(Record) error) error {
	return serviceChannels(ChannelID(sessionKey), sessionKey, emit)
}

// serviceChannels runs one monitor cycle for a reader (RFC-0009 §9): replay
// the reader's own channel against its `.ack.json`, then the broadcast channel
// against the reader's per-reader ack file. The nudge socket stays
// own-channel-only; broadcast delivery rides the periodic rescan. sessionKey is
// the reader's resolved key (RFC-0009 §2); it is passed to the broadcast path
// for self-echo suppression but NOT to the own-channel path, so a deliberate
// directed self-message still wakes.
func serviceChannels(cid, sessionKey string, emit func(Record) error) error {
	if err := emitUnacked(cid, AckFile(cid), "", true /* reap delivered messages */, emit); err != nil {
		return err
	}
	return serviceBroadcast(cid, sessionKey, emit)
}

// serviceBroadcast replays the broadcast channel for one reader with condvar
// semantics (RFC-0009 §9): on first attach — the reader's ack file does not
// exist (os.Stat, not corrupt-loads-empty) — initialize at current end by
// persisting an ack map covering every existing waking record WITHOUT
// emitting; thereafter normal replay-unacked applies.
func serviceBroadcast(readerCID, readerKey string, emit func(Record) error) error {
	bcid := ChannelID(BroadcastKey)
	ackPath := AckFileFor(bcid, readerCID)
	if _, err := os.Stat(ackPath); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		return initBroadcastAck(bcid, ackPath)
	}
	return emitUnacked(bcid, ackPath, readerKey, false /* per-reader acks: do not reap */, emit)
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
//
// selfKey suppresses broadcast self-echo (RFC-0009 §9): when non-empty, a
// record whose `from` equals selfKey is acked WITHOUT being emitted — the
// sender already knows it sent the broadcast, so waking it on its own message
// is a wake for zero new information, but the ack must still advance so the
// record does not linger unacked in this reader's broadcast ack map. The
// own-channel path passes an empty selfKey, so a deliberate directed
// self-message (--target <own-key>, the "remind myself" case) still wakes.
//
// reapDelivered enables immediate reap of a delivered standalone message
// (RFC-0010 §4): on the OWNING channel a message's wake IS the whole job
// (RFC-0009 §4) and any chat body lives in a separate store, so once emitted it
// has no residual value and is reaped at once instead of accruing a msg-* row.
// It is false on the broadcast channel: many readers share the journal, so
// reaping on one reader's ack would starve the others (broadcast messages rest
// on the age backstop, acked per-reader).
func emitUnacked(cid, ackPath, selfKey string, reapDelivered bool, emit func(Record) error) error {
	a := loadAckPath(ackPath)
	waking, err := scanWaking(cid)
	if err != nil {
		return err
	}
	for _, r := range waking {
		if prev, ok := a.Acked[r.Job]; ok && r.Seq <= prev {
			continue
		}
		if selfKey == "" || r.From != selfKey {
			if err := emit(r); err != nil {
				return err
			}
		}
		// A delivered message on the owning channel is reaped in place of
		// acking: the gone journal can never re-emit, so the reap IS the ack —
		// no entry is persisted (which would only be a dangling entry for the
		// next sweep to prune). A crash between emit and reap re-emits on
		// restart, which the at-least-once contract already permits.
		if reapDelivered && r.Type == TypeMessage {
			reapJob(cid, r.Job)
			continue
		}
		a.Acked[r.Job] = r.Seq
		if err := saveAckPath(ackPath, a); err != nil { // persist after emit => at-least-once
			return err
		}
	}
	return nil
}
