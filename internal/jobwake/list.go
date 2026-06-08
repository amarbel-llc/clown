package jobwake

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// JobSummary pairs a job id (and, for cross-channel listings, its channel id)
// with the job's derived status. It is the row type the operator-facing
// `ringmaster ls` renders, and is the JSON shape emitted under --json.
type JobSummary struct {
	Channel string `json:"channel,omitempty"`
	JobID   string `json:"job"`
	Status  Status `json:"status"`
}

// JobsRoot is the directory holding every per-channel job dir
// ($XDG_STATE_HOME/clown/jobs). Each immediate child is a ChannelID dir.
func JobsRoot() string {
	return filepath.Join(stateHome(), "clown", "jobs")
}

// listJobIDs returns the job ids (one per <id>.jsonl) in a channel dir, skipping
// dotfiles and non-journal files. A missing channel dir yields an empty slice.
func listJobIDs(cid string) ([]string, error) {
	entries, err := os.ReadDir(JournalDir(cid))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, ".jsonl"))
	}
	return ids, nil
}

// channelSummaries derives a JobSummary for every job in one channel, sorted by
// start time (oldest first). tailN bounds the spool tail lines carried per job
// (0 = none, the ls default). Jobs whose journal vanished or went empty between
// the dir scan and the status read are skipped rather than failing the listing.
func channelSummaries(cid string, tailN int, now time.Time) ([]JobSummary, error) {
	ids, err := listJobIDs(cid)
	if err != nil {
		return nil, err
	}
	var out []JobSummary
	for _, id := range ids {
		st, err := statusOfChannel(cid, id, tailN, now)
		if err != nil {
			continue // raced reap / empty journal: drop from the listing
		}
		out = append(out, JobSummary{JobID: id, Status: st})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Status.Started < out[j].Status.Started })
	return out, nil
}

// ListJobs returns the status summaries for every job in the channel resolved
// from target (empty => the current session, per resolveSession), oldest first.
func ListJobs(target string, tailN int, now time.Time) ([]JobSummary, error) {
	return channelSummaries(ChannelID(resolveSession(target)), tailN, now)
}

// ListAllJobs returns the status summaries for every job in every channel under
// JobsRoot, each row tagged with its channel id, oldest first across channels.
// It is the operator's "show me everything on this host" listing — channels are
// per-session and hashed, so without it an operator only sees the session whose
// key it happens to hold.
func ListAllJobs(tailN int, now time.Time) ([]JobSummary, error) {
	entries, err := os.ReadDir(JobsRoot())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []JobSummary
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cid := e.Name()
		rows, err := channelSummaries(cid, tailN, now)
		if err != nil {
			continue
		}
		for _, r := range rows {
			r.Channel = cid
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Status.Started < out[j].Status.Started })
	return out, nil
}
