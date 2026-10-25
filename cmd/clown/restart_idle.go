package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	if err := os.MkdirAll(filepath.Dir(p.logPath), 0o700); err != nil {
		return preflightVerdict{}, err
	}
	log, err := os.OpenFile(p.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return preflightVerdict{}, err
	}
	defer log.Close()
	ctx, cancel := context.WithTimeout(context.Background(), preflightTimeout)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, p.bin, "restart-preflight")
	cmd.Env = append(os.Environ(), "CLOWN_SESSION_ID="+p.principal)
	cmd.Stdout = &out
	cmd.Stderr = log
	err = cmd.Run()
	fmt.Fprintf(log, "%s restart-preflight %s: %q\n", time.Now().UTC().Format(time.RFC3339), p.principal, out.String())
	if err != nil {
		return preflightVerdict{}, err
	}
	return parsePreflightVerdict(out.Bytes())
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
		logSelfClose(r.logPath, "restart-preflight verdict %s (%s): %s; not restarting", v.Verdict, principal, v.Reason)
	default:
		logSelfClose(r.logPath, "restart-preflight verdict restart (%s): %s; restarting", principal, v.Reason)
		r.restart(req, v)
	}
}
