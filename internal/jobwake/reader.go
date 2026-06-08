package jobwake

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ReadJob parses a job's JSONL journal in append order, skipping blank or
// malformed lines (RFC-0009 §10). Callers tolerate os.IsNotExist.
func ReadJob(channelID, jobID string) ([]Record, error) {
	if err := validateJobID(jobID); err != nil {
		return nil, err
	}
	f, err := os.Open(JournalFile(channelID, jobID))
	if err != nil {
		return nil, err // callers tolerate os.IsNotExist
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Record
		if json.Unmarshal([]byte(line), &r) != nil {
			continue // skip malformed line (RFC-0009 §10)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// scanWaking returns every waking record across all of a channel's job journals
// sorted by timestamp (RFC-0009 §9). Dotfiles (the ack cursor) and non-jsonl
// files are skipped, as is an unreadable job file. A missing channel dir yields
// an empty result rather than an error.
func scanWaking(channelID string) ([]Record, error) {
	entries, err := os.ReadDir(JournalDir(channelID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Record
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		jobID := strings.TrimSuffix(name, ".jsonl")
		recs, err := ReadJob(channelID, jobID)
		if err != nil {
			continue
		}
		for _, r := range recs {
			if IsWaking(r.Type) {
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}

// ScanWaking returns every waking (terminal) record across all jobs in the
// channel, oldest first by timestamp. It is the read-side counterpart used by
// `clown job read` and the monitor's replay.
func ScanWaking(channelID string) ([]Record, error) { return scanWaking(channelID) }

// ack is the monitor's per-channel delivery cursor (RFC-0009 §9): the highest
// emitted seq per job id.
type ack struct {
	V     int            `json:"v"`
	Acked map[string]int `json:"acked"`
}

// AckFileFor is the per-reader ack cursor on a shared (broadcast) channel
// (RFC-0009 §9): each reader keeps its own watermark so independent monitors
// each see a broadcast exactly once. readerID is the reader's own channel id.
// The `.ack-` prefix keeps the file inside scanWaking's dotfile skip, so it is
// never parsed as a job journal. Ack files accumulate one per reader; GC is
// tracked as clown#113.
func AckFileFor(channelID, readerID string) string {
	return filepath.Join(JournalDir(channelID), ".ack-"+readerID+".json")
}

// loadAckPath reads an ack cursor file, treating a missing or corrupt file as
// an empty set so unacked events re-emit (at-least-once, RFC-0009 §10).
func loadAckPath(path string) ack {
	a := ack{V: 1, Acked: map[string]int{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return a // missing => empty (RFC-0009 §10)
	}
	var parsed ack
	if json.Unmarshal(b, &parsed) == nil && parsed.Acked != nil {
		return parsed
	}
	return a // corrupt => empty
}

// saveAckPath writes an ack cursor atomically via temp-file + rename.
func saveAckPath(path string, a ack) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path) // atomic
}

// loadAck reads the channel's own `.ack.json` cursor.
func loadAck(channelID string) ack { return loadAckPath(AckFile(channelID)) }

// saveAck writes the channel's own `.ack.json` cursor.
func saveAck(channelID string, a ack) error { return saveAckPath(AckFile(channelID), a) }
