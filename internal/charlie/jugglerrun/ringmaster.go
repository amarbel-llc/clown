package jugglerrun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.linenisgreat.com/ringmaster/pkgs/jobwake"
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
	// Cancel writes a cancel-requested record (ringmaster RFC-0018): the
	// owning producer tears down and writes its own aborted terminal. An
	// already-terminal job is an error; callers re-read the journal.
	Cancel(ctx context.Context, target, job, message string) error
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
//	cancel <job> --target T --message M
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

// exitAlreadyTerminal is ringmaster's `done` exit code for a write refused
// because the job already carries a terminal record (jobwake.ErrAlreadyTerminal,
// protocol 3, clown#256): a losing race against the v3 liveness reaper or
// another writer, not a failure of the write itself.
const exitAlreadyTerminal = 3

func (r ExecRingmaster) Done(ctx context.Context, target, job string, d DoneRecord) error {
	args := []string{"done", job, "--target", target, "--state", d.State, "--message", d.Message}
	if d.ResultRef != "" {
		args = append(args, "--result-ref", d.ResultRef)
	}
	_, err := r.run(ctx, nil, args...)
	if err == nil {
		return nil
	}
	var ce *CommandError
	if errors.As(err, &ce) && ce.ExitCode == exitAlreadyTerminal {
		return fmt.Errorf("%w: %s", jobwake.ErrAlreadyTerminal, ce.Stderr)
	}
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

func (r ExecRingmaster) Cancel(ctx context.Context, target, job, message string) error {
	_, err := r.run(ctx, nil, "cancel", job, "--target", target, "--message", message)
	return err
}

// requestStop asks job to cancel and waits up to grace for its terminal
// record. It reports the journal as read afterwards and whether a terminal
// record is present; a failed cancel (an already-terminal job, a race with
// the producer's own done) is not an error, the journal decides.
func requestStop(ctx context.Context, rmc Ringmaster, target, job, message string, grace time.Duration) ([]JobRecord, bool, error) {
	if grace <= 0 {
		grace = DefaultStopGrace
	}
	_ = rmc.Cancel(ctx, target, job, message)
	_ = rmc.WaitTerminal(ctx, target, job, grace)
	recs, err := rmc.Records(ctx, target, job)
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", job, err)
	}
	_, terminal := TerminalRecord(recs)
	return recs, terminal, nil
}

// RingmasterProtocolVersion is the job-platform protocol version juggler was
// built against (jobwake.ProtocolVersion, clown#256).
const RingmasterProtocolVersion = jobwake.ProtocolVersion

// CheckRingmasterProtocol runs `<bin> version --protocol` and compares its
// bare-integer stdout against RingmasterProtocolVersion, exact match: clown
// execs a separately resolved ringmaster binary rather than linking its CLI,
// so a mismatched build (the on-disk journal layout and the cancel/wait
// state machine may have changed) must be caught before any job-platform
// call, not discovered as a confusing mid-flow failure. A mismatch, an
// unparseable reply, or a failure to run bin at all is an error naming both
// versions so the fix — rebuild or upgrade one side to match — is obvious.
func CheckRingmasterProtocol(ctx context.Context, bin string) error {
	out, err := runCommand(ctx, Command{Argv: []string{bin, "version", "--protocol"}})
	if err != nil {
		return fmt.Errorf("checking %s's job-platform protocol version (version --protocol): %w", bin, err)
	}
	trimmed := strings.TrimSpace(string(out))
	got, perr := strconv.Atoi(trimmed)
	if perr != nil {
		return fmt.Errorf("checking %s's job-platform protocol version: unparseable output %q", bin, trimmed)
	}
	if got != RingmasterProtocolVersion {
		return fmt.Errorf("%s speaks job-platform protocol %d, juggler was built for protocol %d; rebuild or upgrade one to match", bin, got, RingmasterProtocolVersion)
	}
	return nil
}

// ProtocolCheckedRingmaster wraps a Ringmaster so the FIRST method call
// through it runs CheckRingmasterProtocol (clown#256) before delegating —
// not every `juggler` invocation, only the ones that actually reach a
// job-platform call, so a usage or parse-time refusal that never touches
// ringmaster costs nothing. The check runs at most once per instance; every
// method shares its result (success or failure) via checkOnce.
type ProtocolCheckedRingmaster struct {
	Inner Ringmaster
	// Bin is the ringmaster binary CheckRingmasterProtocol runs.
	Bin string

	checkOnce sync.Once
	checkErr  error
}

// protocolCheckTimeout bounds the one-time protocol check; it never inherits
// a caller's ctx (see check).
const protocolCheckTimeout = 10 * time.Second

// check runs CheckRingmasterProtocol at most once, under a fresh
// context.Background() bounded by protocolCheckTimeout rather than the
// caller's ctx (clown#256 review finding 2): the first call through may be
// WaitCancelRequested or WaitTerminal, whose ctx is deliberately cancelled
// (SIGTERM, a holder's cancel) — latching that cancellation into checkErr via
// sync.Once would fail every later call sharing this instance, including
// finish's Done under a fresh ctx, losing the agent's own terminal record.
func (p *ProtocolCheckedRingmaster) check(context.Context) error {
	p.checkOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), protocolCheckTimeout)
		defer cancel()
		p.checkErr = CheckRingmasterProtocol(ctx, p.Bin)
	})
	return p.checkErr
}

// checkRingmasterProtocol lets RunAgent run the same check explicitly,
// before AcquireJobLock, on an adopted job where no Ringmaster method call
// happens first (clown#256 review finding 3).
func (p *ProtocolCheckedRingmaster) checkRingmasterProtocol(ctx context.Context) error {
	return p.check(ctx)
}

// protocolChecker is implemented by a Ringmaster adapter that runs a lazy,
// once-per-process protocol check (ProtocolCheckedRingmaster). For a freshly
// started job, Start already runs the check first, so running it again here
// is a no-op (sync.Once); for an adopted job (`juggler run --job <id>`),
// nothing else would run it before AcquireJobLock otherwise.
type protocolChecker interface {
	checkRingmasterProtocol(ctx context.Context) error
}

func (p *ProtocolCheckedRingmaster) Start(ctx context.Context, target, label, source string) (string, error) {
	if err := p.check(ctx); err != nil {
		return "", err
	}
	return p.Inner.Start(ctx, target, label, source)
}

func (p *ProtocolCheckedRingmaster) Progress(ctx context.Context, target, job, message string) error {
	if err := p.check(ctx); err != nil {
		return err
	}
	return p.Inner.Progress(ctx, target, job, message)
}

func (p *ProtocolCheckedRingmaster) Done(ctx context.Context, target, job string, d DoneRecord) error {
	if err := p.check(ctx); err != nil {
		return err
	}
	return p.Inner.Done(ctx, target, job, d)
}

func (p *ProtocolCheckedRingmaster) Records(ctx context.Context, target, job string) ([]JobRecord, error) {
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	return p.Inner.Records(ctx, target, job)
}

func (p *ProtocolCheckedRingmaster) Status(ctx context.Context, target, job string) (JobStatus, error) {
	if err := p.check(ctx); err != nil {
		return JobStatus{}, err
	}
	return p.Inner.Status(ctx, target, job)
}

func (p *ProtocolCheckedRingmaster) SpoolPath(ctx context.Context, target, job string) (string, error) {
	if err := p.check(ctx); err != nil {
		return "", err
	}
	return p.Inner.SpoolPath(ctx, target, job)
}

func (p *ProtocolCheckedRingmaster) WaitTerminal(ctx context.Context, target, job string, timeout time.Duration) error {
	if err := p.check(ctx); err != nil {
		return err
	}
	return p.Inner.WaitTerminal(ctx, target, job, timeout)
}

func (p *ProtocolCheckedRingmaster) WaitCancelRequested(ctx context.Context, target, job string) error {
	if err := p.check(ctx); err != nil {
		return err
	}
	return p.Inner.WaitCancelRequested(ctx, target, job)
}

func (p *ProtocolCheckedRingmaster) Cancel(ctx context.Context, target, job, message string) error {
	if err := p.check(ctx); err != nil {
		return err
	}
	return p.Inner.Cancel(ctx, target, job, message)
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
