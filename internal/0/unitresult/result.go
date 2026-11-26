// Package unitresult is the file clown writes once a confined unit is active
// (FDR 0020 "Launcher interface"): the unit's name and cgroup, its pty and main
// pid, the capabilities this launch offers and what became of the bridge. It
// holds only the shape and the atomic writer, with no launch code, so another
// module (spinclass, whose end-to-end test stands in for clown's half of the
// bridge seam) can write and read the same file. No facade is published for it
// (nothing outside this repo imports it; the test seam is a binary,
// cmd/clown-bridge-seam, .#bridge-seam). internal/*/agentscope re-exports
// Result and wraps WriteResult with its own version and capability defaults.
package unitresult

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Result is what clown reports back once the unit is active (FDR 0020
// "Launcher interface"). Unit is authoritative.
type Result struct {
	Version      int      `json:"version"`
	Capabilities []string `json:"capabilities"`
	Unit         string   `json:"unit,omitempty"`
	// Principal is the session's CLOWN_SESSION_ID, reported by the frontend:
	// clown keeps it out of the claude subtree's environment (clown#136), so
	// it is not readable from /proc/<main_pid>/environ.
	Principal    string `json:"principal,omitempty"`
	Cgroup       string `json:"cgroup,omitempty"`
	InvocationID string `json:"invocation_id,omitempty"`
	UID          int    `json:"uid"`
	Pty          string `json:"pty,omitempty"`
	MainPID      int    `json:"main_pid,omitempty"`
	// ServeSocket is the session's serve socket (F12): the frontend unix
	// socket spinclass serve's bridge listens on, which admits only this unit
	// (and the frontend). Absent when the session has none.
	ServeSocket string `json:"serve_socket,omitempty"`
	// ModelCredential is how the unit got its model credential: "proxy" (the
	// frontend injects it; the unit holds a placeholder) or "login-file" (the
	// operator's login bound read-only, the fallback). Absent when the launch
	// routes the model API itself (a gateway profile). Read-only for consumers.
	ModelCredential string `json:"model_credential,omitempty"`
	// Bridge says what became of the bridge (FDR 0048): absent when it was never
	// asked for, "recorded" (the directory is bound and the unit's cgroup was
	// written for the serve; NOT a claim that the listener admits the unit or
	// that `control` opened: this result is written once and cannot know), or
	// "off: <reason>" when the spec's switch was set and the bridge is missing,
	// so a missing bridge never looks like one never switched on. Written by the
	// frontend; read-only for consumers.
	Bridge string `json:"bridge,omitempty"`
	// ExitCode is set once the unit has exited after its provider started.
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

// WriteResult writes r to path exactly as given, atomically (a temporary file in
// the same directory, then a rename), creating the directory. It sets no
// defaults: clown's agentscope.WriteResult fills Version and Capabilities first;
// a caller standing in for clown sets the ones its reader checks.
func WriteResult(path string, r Result) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agent-unit-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
