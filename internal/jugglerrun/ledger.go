package jugglerrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"code.linenisgreat.com/clown/internal/jugglerdecide"
	"code.linenisgreat.com/clown/internal/jugglerloop"
)

// RunLedgerSchema is the version carried by every RunLedger.
const RunLedgerSchema = 1

// Artifact is one tool's created URIs, as the --wait record and the
// fallback's artifacts carry them.
type Artifact struct {
	Tool string   `json:"tool"`
	Kind *string  `json:"kind"`
	URIs []string `json:"uris"`
}

// ArtifactsFromLedger lists every successful call that returned URIs.
func ArtifactsFromLedger(l jugglerloop.Ledger) []Artifact {
	out := []Artifact{}
	for _, c := range l.Calls {
		if c.OK && len(c.URIs) > 0 {
			out = append(out, Artifact{Tool: c.Tool, Kind: c.Kind, URIs: c.URIs})
		}
	}
	return out
}

// RunLedger is the run-level ledger (FDR 0019 §1, §4): the lifecycle owner's
// recorded facts about the run, closed by `juggler resolve`.
type RunLedger struct {
	Schema   int              `json:"schema"`
	RunKey   string           `json:"run_key"`
	Calls    []RunLedgerEntry `json:"calls"`
	Resolved *Resolution      `json:"resolved"`
}

// RunLedgerEntry mirrors the agent ledger's call entry so one evaluator
// vocabulary (tool, kind, ok, uris) covers both.
type RunLedgerEntry struct {
	Tool   string   `json:"tool"`
	Kind   string   `json:"kind"`
	OK     bool     `json:"ok"`
	URIs   []string `json:"uris"`
	Reason string   `json:"reason,omitempty"`
	// Ran is set on the fallback entry: whether the fallback script ran.
	Ran       *bool      `json:"ran,omitempty"`
	Artifacts []Artifact `json:"artifacts,omitempty"`

	// Child-stop entry fields (`juggler resolve` stopping a live subagent):
	// OK is whether the child's job terminalized within the stop grace.
	Job              string `json:"job,omitempty"`
	Principal        string `json:"principal,omitempty"`
	StoppedByResolve bool   `json:"stopped_by_resolve,omitempty"`

	// Route-entry fields (`juggler decide --run-key`).
	Choice         string   `json:"choice,omitempty"`
	Confidence     *float64 `json:"confidence,omitempty"`
	TopProbability *float64 `json:"top_probability,omitempty"`
	Threshold      *float64 `json:"threshold,omitempty"`
	Verdict        string   `json:"verdict,omitempty"`
	StanzaID       string   `json:"stanza_id,omitempty"`
}

// RouteDecision is one `juggler decide` outcome as the run ledger records it.
type RouteDecision struct {
	Choice         string
	Confidence     float64
	TopProbability *float64
	Threshold      float64
	// Verdict is jugglerdecide's: usable, below-threshold or no-choice.
	Verdict  string
	Reason   string
	StanzaID string
}

// RouteEntry is the run ledger's `route` entry (FDR 0019 §1, §4): ok
// exactly when the router's verdict is usable.
func RouteEntry(d RouteDecision) RunLedgerEntry {
	conf, threshold := d.Confidence, d.Threshold
	return RunLedgerEntry{
		Tool: "route", Kind: "route", OK: d.Verdict == string(jugglerdecide.Usable), URIs: []string{},
		Reason: d.Reason, Choice: d.Choice, Confidence: &conf, TopProbability: d.TopProbability,
		Threshold: &threshold, Verdict: d.Verdict, StanzaID: d.StanzaID,
	}
}

// ChildStopEntry is the run ledger's entry for a subagent still live when the
// run resolved: ok exactly when the job terminalized within the stop grace.
func ChildStopEntry(child *ChildRecord, terminalized bool) RunLedgerEntry {
	reason := "stopped by resolve"
	if !terminalized {
		reason = "still running after the stop grace"
	}
	return RunLedgerEntry{
		Tool: "subagent_stop", Kind: "subagent", OK: terminalized, URIs: []string{}, Reason: reason,
		Job: child.Job, Principal: child.Principal, StoppedByResolve: true,
	}
}

// AppendRunLedgerEntry appends e to run runKey's ledger, under the run's
// lock. A resolved run's ledger is closed and refuses new entries.
func AppendRunLedgerEntry(store Store, runKey string, e RunLedgerEntry) error {
	if err := ValidRunKey(runKey); err != nil {
		return err
	}
	unlock, err := lock(store.runPath(runKey) + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	run, err := store.LoadRun(runKey)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("no run with key %q", runKey)
	}
	l, err := store.loadRunLedger(runKey)
	if err != nil {
		return err
	}
	if l.Resolved != nil || run.Resolved != nil {
		return fmt.Errorf("run %s is already resolved; its ledger is closed", runKey)
	}
	l.Calls = append(l.Calls, e)
	return writeJSONAtomic(store.RunLedgerPath(runKey), l)
}

// Resolution is how the run ended.
type Resolution struct {
	State  string    `json:"state"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// FallbackEntry is the run ledger's `fallback` entry. The fallback runs
// exactly when the run resolves failed (FDR 0019 §6's one rule); its own
// outcome is ok when it did not need to run, or when it ran and produced at
// least one artifact URI.
func FallbackEntry(state, reason string, artifacts []Artifact) RunLedgerEntry {
	ran := state == StateFailed
	uris := []string{}
	for _, a := range artifacts {
		uris = append(uris, a.URIs...)
	}
	return RunLedgerEntry{
		Tool:      "fallback",
		Kind:      "fallback",
		OK:        !ran || len(uris) > 0,
		URIs:      uris,
		Reason:    reason,
		Ran:       &ran,
		Artifacts: artifacts,
	}
}

func (s Store) loadRunLedger(key string) (RunLedger, error) {
	l := RunLedger{Schema: RunLedgerSchema, RunKey: key, Calls: []RunLedgerEntry{}}
	if _, err := readJSONFile(s.RunLedgerPath(key), &l); err != nil {
		return RunLedger{}, err
	}
	return l, nil
}

// writeSpool writes a job's result document to its ringmaster output spool
// (RFC-0010 §2) and returns the path, which the terminal record carries as
// result_ref. The producer owns the spool; juggler writes it once, at the
// end, so the spool IS the ledger. An empty path (the facility disabled)
// yields "" and no error.
func writeSpool(ctx context.Context, rmc Ringmaster, target, job string, doc []byte) (string, error) {
	path, err := rmc.SpoolPath(ctx, target, job)
	if err != nil {
		return "", fmt.Errorf("ringmaster spool-path: %w", err)
	}
	if path == "" {
		return "", nil
	}
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		return "", fmt.Errorf("writing ledger to spool %s: %w", path, err)
	}
	return path, nil
}

// readAgentLedger reads an agent ledger from path; a missing or empty file
// yields ok=false.
func readAgentLedger(path string) (jugglerloop.Ledger, bool, error) {
	if path == "" {
		return jugglerloop.Ledger{}, false, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && len(data) == 0) {
		return jugglerloop.Ledger{}, false, nil
	}
	if err != nil {
		return jugglerloop.Ledger{}, false, err
	}
	var l jugglerloop.Ledger
	if err := json.Unmarshal(data, &l); err != nil {
		return jugglerloop.Ledger{}, false, fmt.Errorf("parsing ledger %s: %w", path, err)
	}
	return l, true, nil
}

func marshalDocument(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
