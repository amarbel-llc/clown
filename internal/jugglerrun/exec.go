// Package jugglerrun is the lifecycle half of the juggler agent substrate
// (FDR 0019 §1, §2, §6, §9, §11): the `juggler run` agent driver, the
// `juggler spawn` launcher glue (new runs and subagents), the post-stop
// `juggler exit-wake` hook, `juggler resolve`, and the job ledger / handle
// read paths.
//
// The job platform (ringmaster), the XMPP surface (troupe) and systemd are
// consumed as external binaries. Every exec sits behind a small interface
// (Ringmaster, Troupe, UnitLauncher, RoomProvisioner) whose Exec*
// implementation shells the binary, so the logic is testable with fakes.
package jugglerrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Environment overrides for the consumed binaries and the moxy upstream.
// Brief 6 burns nix store paths in; until then the bare names resolve on PATH.
const (
	RingmasterBinEnv = "JUGGLER_RINGMASTER_BIN"
	TroupeBinEnv     = "JUGGLER_TROUPE_BIN"
	SystemdRunBinEnv = "JUGGLER_SYSTEMD_RUN_BIN"
	MoxyURLEnv       = "JUGGLER_MOXY_URL"

	SessionIDEnv = "CLOWN_SESSION_ID"
)

// ResolveBinary picks a binary path: an explicit flag value wins, then the
// environment override, then the bare name (looked up on PATH at exec time).
func ResolveBinary(flagValue, envVar, bareName string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return bareName
}

// Command is one subprocess invocation.
type Command struct {
	Argv []string
	// Env is the complete child environment; nil inherits os.Environ().
	Env []string
}

// CommandRunner runs a Command and returns its stdout.
type CommandRunner interface {
	Run(ctx context.Context, cmd Command) ([]byte, error)
}

// OSCommandRunner runs commands with os/exec, capturing stderr into the
// returned *CommandError.
type OSCommandRunner struct{}

func (OSCommandRunner) Run(ctx context.Context, c Command) ([]byte, error) {
	if len(c.Argv) == 0 {
		return nil, errors.New("jugglerrun: empty argv")
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Env = c.Env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		code := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
		return out, &CommandError{Argv: c.Argv, ExitCode: code, Stderr: strings.TrimSpace(stderr.String()), Err: err}
	}
	return out, nil
}

// CommandError is a failed subprocess. ExitCode is -1 when the process did
// not exit normally (not found, killed by ctx).
type CommandError struct {
	Argv     []string
	ExitCode int
	Stderr   string
	Err      error
}

func (e *CommandError) Error() string {
	name := filepath.Base(e.Argv[0])
	if len(e.Argv) > 1 {
		name += " " + e.Argv[1]
	}
	if e.Stderr != "" {
		return fmt.Sprintf("%s: %v: %s", name, e.Err, e.Stderr)
	}
	return fmt.Sprintf("%s: %v", name, e.Err)
}

func (e *CommandError) Unwrap() error { return e.Err }

// EnvWith returns base with every override set, replacing existing entries.
// Keys are applied in sorted order so the result is deterministic.
func EnvWith(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, replaced := overrides[k]; !replaced {
			out = append(out, kv)
		}
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+overrides[k])
	}
	return out
}

// StripPrincipalEnv returns env without the principal's identity:
// CLOWN_SESSION_ID (the per-instance key, FDR 0019 §2 / clown#136) and every
// TROUPE_XMPP_* variable (the minted XMPP credential reference and its
// connection settings). It is applied to every environment handed to a
// process outside the runtime scope — moxy's tool servers, and the
// systemd-run client process `juggler spawn` execs, so the spawner's own
// identity never leaks into a unit (the unit gets only what --setenv names).
func StripPrincipalEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == SessionIDEnv || strings.HasPrefix(k, "TROUPE_XMPP_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// UnitPassthroughEnvVars are the spawner's environment variables a transient
// agent unit inherits via --setenv. They locate shared state (the ringmaster
// journal, the models file) and the XMPP server, never an identity: the
// unit's identity is the child's, set explicitly by SpawnChild.
var UnitPassthroughEnvVars = []string{
	"XDG_STATE_HOME",
	"XDG_RUNTIME_DIR",
	"JUGGLER_MODELS_PATH",
	RingmasterBinEnv,
	TroupeBinEnv,
	MoxyBinEnv,
	"TROUPE_TRANSPORT",
	"TROUPE_XMPP_HOST",
	"TROUPE_XMPP_PORT",
	"TROUPE_XMPP_INSECURE",
}

// PassthroughUnitEnv selects UnitPassthroughEnvVars that are set in environ.
func PassthroughUnitEnv(environ []string) map[string]string {
	want := map[string]bool{}
	for _, k := range UnitPassthroughEnvVars {
		want[k] = true
	}
	out := map[string]string{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok && want[k] && v != "" {
			out[k] = v
		}
	}
	return out
}
