package jobwake

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
)

// ReadJob parses a job's JSONL journal in append order, skipping blank or
// malformed lines (RFC-0009 §10). Callers tolerate os.IsNotExist.
func ReadJob(channelID, jobID string) ([]Record, error) {
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
