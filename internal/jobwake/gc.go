package jobwake

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// journalRetention is the journal GC retention window (TUNING LEVER,
// FDR-0013): job journals — and, in the broadcast channel, other readers'
// ack files — whose mtime is older than this are reaped by the sweep that
// runs once at Watch start.
const journalRetention = 7 * 24 * time.Hour

// restingRetention is the shorter GC window for DELIVERED lifecycle terminals
// (TUNING LEVER, RFC-0010 §4): a terminal record whose ack confirms delivery
// needs only brief post-mortem retention for `ls`/`tail`, not the full
// journalRetention. journalRetention remains the backstop for UNDELIVERED
// terminals (a producer that died before its terminal was acked) and for
// orphaned never-terminal jobs. MUST be <= journalRetention.
const restingRetention = 24 * time.Hour

// sweep is the one-shot GC pass run at monitor start (clown#113). For the
// reader's own channel and the broadcast channel it:
//
//  1. removes job journals older than journalRetention (by file mtime);
//  2. prunes ack-map entries whose journal no longer exists — run AFTER
//     journal removal so entries for just-reaped journals go too. An entry
//     whose journal still exists is NEVER pruned (that would re-emit);
//  3. in the broadcast channel dir only, removes OTHER readers' per-reader
//     ack files older than journalRetention (a reader gone for the whole
//     window; saveAckPath refreshes mtime on every save, so active readers
//     are safe).
//
// The sweep is best-effort: it has no logger and never reports failure —
// erroring entries are skipped so a GC hiccup cannot break the monitor.
func sweep(readerCID string, now time.Time) {
	bcid := ChannelID(BroadcastKey)
	cutoff := now.Add(-journalRetention)
	restingCutoff := now.Add(-restingRetention)

	// Resting-retention tier (own channel only): a DELIVERED lifecycle terminal
	// is reaped after the short window, ahead of the journalRetention backstop.
	// Broadcast is excluded — its per-reader acks give no single "delivered"
	// verdict, so any terminals there rest on the age backstop below.
	reapAckedTerminals(readerCID, AckFile(readerCID), restingCutoff)

	reapAgedJournals(readerCID, cutoff)
	reapAgedJournals(bcid, cutoff)

	reapOrphanSpools(readerCID, cutoff)
	reapOrphanSpools(bcid, cutoff)

	pruneAckEntries(readerCID, AckFile(readerCID))
	pruneAckEntries(bcid, AckFileFor(bcid, readerCID))

	reapStaleReaderAcks(bcid, readerCID, cutoff)
}

// reapAgedJournals removes <job-id>.jsonl files in the channel dir whose
// mtime is before cutoff, along with each reaped job's <job-id>.out spool
// (RFC-0010 §4: the spool dies with its journal, regardless of the spool's own
// mtime). Dotfiles (ack cursors) and non-journal files are never touched.
func reapAgedJournals(cid string, cutoff time.Time) {
	entries, err := os.ReadDir(JournalDir(cid))
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			reapJob(cid, strings.TrimSuffix(name, ".jsonl"))
		}
	}
}

// reapJob removes a job's journal and its output spool together (RFC-0010 §4:
// the spool dies with its journal, regardless of the spool's own mtime).
// Best-effort; missing files are ignored.
func reapJob(cid, jobID string) {
	_ = os.Remove(JournalFile(cid, jobID))
	_ = os.Remove(SpoolFile(cid, jobID))
}

// reapAckedTerminals reaps job journals on the owning channel whose terminal
// record has been ACKED (delivered) and whose mtime is older than restingCutoff
// — the RFC-0010 §4 resting-retention tier. A delivered terminal needs only
// brief post-mortem retention for `ls`/`tail`; undelivered terminals (producer
// died before the ack) and orphaned never-terminal jobs are left to
// reapAgedJournals' longer journalRetention backstop. Acks are read from
// ackPath (the channel's own `.ack.json`); the pruneAckEntries pass that runs
// later in the same sweep drops the now-dangling ack entries for jobs reaped
// here. Broadcast is not swept this way (its per-reader acks carry no single
// delivered verdict).
func reapAckedTerminals(cid, ackPath string, restingCutoff time.Time) {
	entries, err := os.ReadDir(JournalDir(cid))
	if err != nil {
		return
	}
	a := loadAckPath(ackPath)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(restingCutoff) {
			continue
		}
		jobID := strings.TrimSuffix(name, ".jsonl")
		recs, err := ReadJob(cid, jobID)
		if err != nil || len(recs) == 0 {
			continue
		}
		last := recs[len(recs)-1]
		if !IsTerminal(last.Type) {
			continue // not terminal: backstop only
		}
		if acked, ok := a.Acked[jobID]; !ok || acked < last.Seq {
			continue // terminal not yet delivered: backstop only
		}
		reapJob(cid, jobID)
	}
}

// reapOrphanSpools removes <job-id>.out spools whose <job-id>.jsonl journal is
// absent and whose own mtime is before cutoff (RFC-0010 §4). The age gate means
// a spool created by `clown job spool-path` before its `started` journal is
// written is never reaped mid-setup. A spool whose journal still exists is left
// to reapAgedJournals, which removes the pair together.
func reapOrphanSpools(cid string, cutoff time.Time) {
	entries, err := os.ReadDir(JournalDir(cid))
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".out") {
			continue
		}
		jobID := strings.TrimSuffix(name, ".out")
		if _, err := os.Stat(JournalFile(cid, jobID)); err == nil {
			continue // journal present => reaped with its journal, not here
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(SpoolFile(cid, jobID))
		}
	}
}

// pruneAckEntries drops acked entries whose <job-id>.jsonl is confirmed
// missing from the channel dir. The ack file is rewritten only when entries
// were actually dropped, and never created when absent — conjuring a
// broadcast per-reader ack here would corrupt first-attach (init-at-end)
// semantics.
func pruneAckEntries(cid, ackPath string) {
	if _, err := os.Stat(ackPath); err != nil {
		return // missing (or unreadable) ack file: nothing to prune
	}
	a := loadAckPath(ackPath)
	changed := false
	for job := range a.Acked {
		if _, err := os.Stat(JournalFile(cid, job)); os.IsNotExist(err) {
			delete(a.Acked, job)
			changed = true
		}
	}
	if changed {
		_ = saveAckPath(ackPath, a)
	}
}

// reapStaleReaderAcks removes other readers' `.ack-<reader>.json` files in
// the broadcast channel dir whose mtime is before cutoff. The sweeping
// reader's own ack file is always kept — it is about to be refreshed by the
// monitor it belongs to.
func reapStaleReaderAcks(bcid, ownReaderCID string, cutoff time.Time) {
	dir := JournalDir(bcid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	ownName := filepath.Base(AckFileFor(bcid, ownReaderCID))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, ".ack-") || !strings.HasSuffix(name, ".json") || name == ownName {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}
