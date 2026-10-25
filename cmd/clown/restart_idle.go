package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// preflightVerdict is `spinclass restart-preflight`'s one JSON line (spinclass
// FDR 0037): restart (consumed, go ahead), none (nothing to restart), refused
// (an obstacle; spinclass cleared the request and told the holders).
type preflightVerdict struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
	When    string `json:"when"`
}

// parsePreflightVerdict accepts exactly one JSON line naming a known verdict.
// Anything else is an error, which the caller treats as "do not restart".
func parsePreflightVerdict(out []byte) (preflightVerdict, error) {
	line := strings.TrimSpace(string(out))
	if line == "" {
		return preflightVerdict{}, errors.New("no output")
	}
	if strings.Contains(line, "\n") {
		return preflightVerdict{}, errors.New("more than one line of output")
	}
	var v preflightVerdict
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		return preflightVerdict{}, fmt.Errorf("not a verdict: %w", err)
	}
	switch v.Verdict {
	case "restart", "none", "refused":
		return v, nil
	}
	return preflightVerdict{}, fmt.Errorf("unknown verdict %q", v.Verdict)
}

// preflightTimeout bounds `spinclass restart-preflight`; a hung preflight must
// not wedge the idle runner, which also runs close-self-run.
const preflightTimeout = 30 * time.Second

// restartPreflight runs `spinclass restart-preflight` for one session.
type restartPreflight struct {
	bin       string
	principal string
	logPath   string
}

// run asks spinclass whether the pending restart may proceed, with the
// principal in CLOWN_SESSION_ID. A non-zero exit, a timeout or unparsable
// output is an error. Its stderr and the verdict line go to the log.
func (p *restartPreflight) run() (preflightVerdict, error) {
	log, err := openLogForAppend(p.logPath)
	if err != nil {
		return preflightVerdict{}, err
	}
	defer log.Close()
	ctx, cancel := context.WithTimeout(context.Background(), preflightTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.bin, "restart-preflight")
	cmd.Env = append(os.Environ(), "CLOWN_SESSION_ID="+p.principal)
	cmd.Stderr = log
	out, err := outputViaFile(cmd)
	fmt.Fprintf(log, "%s restart-preflight %s: %q\n", time.Now().UTC().Format(time.RFC3339), p.principal, out)
	if err != nil {
		return preflightVerdict{}, err
	}
	return parsePreflightVerdict(out)
}

// outputViaFile runs cmd with its stdout in an unlinked temporary file and
// returns what it wrote (64 KiB at most). Unlike a pipe, a file does not hold
// Wait until every process that inherited stdout has closed it, so a
// grandchild the command leaves behind cannot stall the serial idle runner.
func outputViaFile(cmd *exec.Cmd) ([]byte, error) {
	f, err := os.CreateTemp("", "clown-restart-out-*")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	_ = os.Remove(f.Name())
	cmd.Stdout = f
	runErr := cmd.Run()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	out, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return nil, err
	}
	return out, runErr
}

// idleRunner is what one idle transition does, in a fixed order: run
// close-self-run, then, only when a restart is queued, take it and ask
// spinclass, restarting only on a "restart" verdict. The queued request is
// taken before the preflight, so whatever the answer (a close that landed
// gives none) it is never asked again.
type idleRunner struct {
	closer    *selfCloseRunner
	preflight *restartPreflight
	queue     *restartQueue
	restart   func(restartRequest, preflightVerdict)
	logPath   string
}

func (r *idleRunner) onIdle() {
	// Once a restart has stopped (or is stopping) the provider, an idle poke
	// must not start a close or another restart: the session is between two
	// frontends.
	if restartUnderway() {
		logSelfClose(r.logPath, "idle poke ignored: a restart is underway")
		return
	}
	_ = r.closer.run()
	if r.queue == nil {
		return
	}
	req, ok := r.queue.take()
	if !ok {
		return
	}
	principal := r.preflight.principal
	v, err := r.preflight.run()
	switch {
	case err != nil:
		logSelfClose(r.logPath, "restart-preflight failed (%s); not restarting: %v", principal, err)
	case v.Verdict != "restart":
		logSelfClose(r.logPath, "restart-preflight verdict %s (%s): %s; not restarting", v.Verdict, principal, sanitizeRestartReason(v.Reason))
	default:
		logSelfClose(r.logPath, "restart-preflight verdict restart (%s): %s; restarting", principal, sanitizeRestartReason(v.Reason))
		r.restart(req, v)
	}
}
