package jugglerrun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// JobRecord is one ringmaster journal record, as `ringmaster read --json`
// prints it (ringmaster internal/0/jobwake Record).
type JobRecord struct {
	Job       string `json:"job"`
	Session   string `json:"session,omitempty"`
	Source    string `json:"source"`
	From      string `json:"from,omitempty"`
	Type      string `json:"type"`
	Seq       int    `json:"seq"`
	TS        string `json:"ts"`
	Message   string `json:"message,omitempty"`
	ResultRef string `json:"result_ref,omitempty"`
}

// JobStatus is the subset of `ringmaster status --json` this package reads.
type JobStatus struct {
	State    string `json:"state"`
	Source   string `json:"source"`
	Started  string `json:"started"`
	Ended    string `json:"ended,omitempty"`
	Progress string `json:"progress,omitempty"`
	Liveness string `json:"liveness,omitempty"`
}

// DoneRecord is a terminal record's payload.
type DoneRecord struct {
	State     string
	Message   string
	ResultRef string
}

// Ringmaster is the job-platform surface juggler consumes. Every method is
// addressed by target: the session key whose channel holds the job (the
// parent principal for an agent job, the issuer for a run job).
type Ringmaster interface {
	Start(ctx context.Context, target, label, source string) (job string, err error)
	Progress(ctx context.Context, target, job, message string) error
	Done(ctx context.Context, target, job string, d DoneRecord) error
	Records(ctx context.Context, target, job string) ([]JobRecord, error)
	Status(ctx context.Context, target, job string) (JobStatus, error)
	SpoolPath(ctx context.Context, target, job string) (string, error)
	// WaitTerminal blocks until the job is terminal; timeout 0 blocks until
	// terminal or ctx. A timeout and an unknown job both return an error —
	// callers disambiguate from the journal (Records) or Status.
	WaitTerminal(ctx context.Context, target, job string, timeout time.Duration) error
	// WaitCancelRequested blocks until the job carries a cancel-requested
	// record (a holder's job_cancel, ringmaster RFC-0018) or a terminal.
	WaitCancelRequested(ctx context.Context, target, job string) error
}

// ExecRingmaster shells the ringmaster binary (ringmaster(1); the verbs are
// RFC-0015 §2's producer/agent set). Argv shapes:
//
//	start --target T --label L --source S          -> job id on stdout
//	progress <job> --target T --message M
//	done <job> --target T --state S --message M [--result-ref R]
//	read --job <job> --json      (env CLOWN_SESSION_ID=T: read has no --target)
//	status <job> --target T --json
//	spool-path <job> --target T
//	wait <job> --target T [--timeout D] --json [--on-cancel]
type ExecRingmaster struct {
	Bin string
}

func (r ExecRingmaster) run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	return runCommand(ctx, Command{Argv: append([]string{r.Bin}, args...), Env: env})
}

func (r ExecRingmaster) Start(ctx context.Context, target, label, source string) (string, error) {
	out, err := r.run(ctx, nil, "start", "--target", target, "--label", label, "--source", source)
	if err != nil {
		return "", err
	}
	job := strings.TrimSpace(string(out))
	if job == "" {
		return "", fmt.Errorf("ringmaster start: no job id on stdout (is CLOWN_DISABLE_JOB_WAKEUP=1?)")
	}
	return job, nil
}

func (r ExecRingmaster) Progress(ctx context.Context, target, job, message string) error {
	_, err := r.run(ctx, nil, "progress", job, "--target", target, "--message", message)
	return err
}

func (r ExecRingmaster) Done(ctx context.Context, target, job string, d DoneRecord) error {
	args := []string{"done", job, "--target", target, "--state", d.State, "--message", d.Message}
	if d.ResultRef != "" {
		args = append(args, "--result-ref", d.ResultRef)
	}
	_, err := r.run(ctx, nil, args...)
	return err
}

func (r ExecRingmaster) Records(ctx context.Context, target, job string) ([]JobRecord, error) {
	env := EnvWith(os.Environ(), map[string]string{SessionIDEnv: target})
	out, err := r.run(ctx, env, "read", "--job", job, "--json")
	if err != nil {
		return nil, err
	}
	return parseJobRecords(out)
}

func parseJobRecords(out []byte) ([]JobRecord, error) {
	var recs []JobRecord
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec JobRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("ringmaster read: parsing record: %w", err)
		}
		recs = append(recs, rec)
	}
	return recs, sc.Err()
}

func (r ExecRingmaster) Status(ctx context.Context, target, job string) (JobStatus, error) {
	out, err := r.run(ctx, nil, "status", job, "--target", target, "--json")
	if err != nil {
		return JobStatus{}, err
	}
	var st JobStatus
	if err := json.Unmarshal(bytes.TrimSpace(out), &st); err != nil {
		return JobStatus{}, fmt.Errorf("ringmaster status: parsing: %w", err)
	}
	return st, nil
}

func (r ExecRingmaster) SpoolPath(ctx context.Context, target, job string) (string, error) {
	out, err := r.run(ctx, nil, "spool-path", job, "--target", target)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (r ExecRingmaster) WaitTerminal(ctx context.Context, target, job string, timeout time.Duration) error {
	args := []string{"wait", job, "--target", target, "--json"}
	if timeout > 0 {
		args = append(args, "--timeout", timeout.String())
	}
	_, err := r.run(ctx, nil, args...)
	return err
}

func (r ExecRingmaster) WaitCancelRequested(ctx context.Context, target, job string) error {
	_, err := r.run(ctx, nil, "wait", job, "--target", target, "--json", "--on-cancel")
	return err
}

// IsTerminalState reports whether state is a ringmaster terminal state:
// succeeded, failed, aborted, interrupted, or the protocol-1 `cancelled`.
func IsTerminalState(state string) bool {
	switch state {
	case StateSucceeded, StateFailed, StateAborted, StateInterrupted, legacyStateCancelled:
		return true
	}
	return false
}

// TerminalRecord returns the job's terminal record, if it has one.
func TerminalRecord(recs []JobRecord) (JobRecord, bool) {
	for i := len(recs) - 1; i >= 0; i-- {
		if IsTerminalState(recs[i].Type) {
			return recs[i], true
		}
	}
	return JobRecord{}, false
}
