// Package jugglerbrief parses, validates and serialises the juggler agent
// brief (FDR 0019 §3): the contract a spawner signs and drops into the run's
// MUC as an agent's only instruction source.
//
// The package is schema-only. It does not interpret the inline moxyfile
// (moxy's lane), does not run evaluators (internal/jugglereval) and does not
// sign anything; Marshal is deterministic so a later signer can sign its bytes.
package jugglerbrief

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	// SchemaVersion is the only brief schema version this package accepts.
	SchemaVersion = 1

	// DefaultSteps is the loop step cap applied when limits.steps is unset.
	DefaultSteps = 12

	// DefaultWallClock is the wall-clock budget applied when limits.wall_clock
	// is unset.
	DefaultWallClock = "2m"

	// EvaluatorKindJQ is the only evaluator kind implemented in this slice.
	EvaluatorKindJQ = "jq"
)

// Brief is the FDR 0019 §3 field set.
type Brief struct {
	Schema    int    `toml:"schema"`
	Principal string `toml:"principal"`
	Parent    string `toml:"parent"`
	Room      string `toml:"room"`
	Model     string `toml:"model"`
	System    string `toml:"system"`
	Task      string `toml:"task"`

	// Moxyfile is the agent's inline moxyfile, kept as opaque text. Its
	// semantics (narrowing against the spawner's moxyfile) belong to moxy.
	Moxyfile string `toml:"moxyfile"`

	// BudgetKeyRef is the FDR 0032 D19 slot: a credential-by-reference path
	// (for example a password-file path) naming the spend key this agent
	// uses. It is never a secret itself.
	BudgetKeyRef string `toml:"budget_key_ref,omitempty"`

	Evaluator Evaluator `toml:"evaluator"`
	Limits    Limits    `toml:"limits"`
}

// Evaluator names how the run ledger is collapsed into a boolean verdict.
type Evaluator struct {
	// Kind is "jq" in this slice. "predicate" and "agent" are reserved.
	Kind    string `toml:"kind"`
	Program string `toml:"program"`
}

// Limits bounds a run.
type Limits struct {
	// Steps is the loop step cap; DefaultSteps when unset.
	Steps int `toml:"steps"`

	// WallClock is a Go duration string; DefaultWallClock when unset.
	WallClock string `toml:"wall_clock"`

	// Sandbox is RESERVED and UNUSED in this slice (FDR 0019 §9). Any TOML
	// value is accepted and preserved verbatim so that per-brief unit
	// properties can land here later without reshaping the brief.
	Sandbox any `toml:"sandbox,omitempty"`
}

// WallClockDuration returns the parsed wall-clock budget. It is valid on any
// Brief returned by Parse.
func (l Limits) WallClockDuration() (time.Duration, error) {
	return time.ParseDuration(l.WallClock)
}

// Parse decodes TOML bytes into a Brief, applies limit defaults and validates.
// Unknown keys are rejected: the brief is a signed contract, so a typo must
// not be silently ignored.
func Parse(data []byte) (*Brief, error) {
	var b Brief
	md, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&b)
	if err != nil {
		return nil, fmt.Errorf("jugglerbrief: decoding TOML: %w", err)
	}
	var unknown []string
	for _, k := range md.Undecoded() {
		// The reserved sandbox value is opaque: the decoder reports its
		// sub-keys as undecoded, but they are accepted by design.
		if len(k) > 2 && k[0] == "limits" && k[1] == "sandbox" {
			continue
		}
		unknown = append(unknown, k.String())
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("jugglerbrief: unknown field(s): %s", strings.Join(unknown, ", "))
	}
	b.applyDefaults()
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return &b, nil
}

func (b *Brief) applyDefaults() {
	if b.Limits.Steps == 0 {
		b.Limits.Steps = DefaultSteps
	}
	if b.Limits.WallClock == "" {
		b.Limits.WallClock = DefaultWallClock
	}
}

// Validate checks the brief. Every error names the offending field.
func (b *Brief) Validate() error {
	if b.Schema != SchemaVersion {
		return fmt.Errorf("jugglerbrief: schema: must be %d, got %d", SchemaVersion, b.Schema)
	}
	required := []struct{ field, value string }{
		{"principal", b.Principal},
		{"parent", b.Parent},
		{"room", b.Room},
		{"model", b.Model},
		{"system", b.System},
		{"task", b.Task},
	}
	for _, r := range required {
		if strings.TrimSpace(r.value) == "" {
			return fmt.Errorf("jugglerbrief: %s: must not be empty", r.field)
		}
	}
	switch b.Evaluator.Kind {
	case EvaluatorKindJQ:
	case "predicate", "agent":
		return fmt.Errorf("jugglerbrief: evaluator.kind: %q is reserved and not yet supported (only %q)", b.Evaluator.Kind, EvaluatorKindJQ)
	case "":
		return fmt.Errorf("jugglerbrief: evaluator.kind: must not be empty (only %q is supported)", EvaluatorKindJQ)
	default:
		return fmt.Errorf("jugglerbrief: evaluator.kind: unknown kind %q (only %q is supported)", b.Evaluator.Kind, EvaluatorKindJQ)
	}
	if strings.TrimSpace(b.Evaluator.Program) == "" {
		return fmt.Errorf("jugglerbrief: evaluator.program: must not be empty")
	}
	if b.Limits.Steps < 1 {
		return fmt.Errorf("jugglerbrief: limits.steps: must be >= 1, got %d", b.Limits.Steps)
	}
	d, err := b.Limits.WallClockDuration()
	if err != nil {
		return fmt.Errorf("jugglerbrief: limits.wall_clock: invalid duration %q: %w", b.Limits.WallClock, err)
	}
	if d <= 0 {
		return fmt.Errorf("jugglerbrief: limits.wall_clock: must be positive, got %q", b.Limits.WallClock)
	}
	return nil
}

// Marshal validates the brief and serialises it to TOML. Output is
// deterministic for a given Brief (struct field order, no maps in the schema
// beyond the opaque reserved sandbox value, whose keys the encoder sorts).
func (b *Brief) Marshal() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(b); err != nil {
		return nil, fmt.Errorf("jugglerbrief: encoding TOML: %w", err)
	}
	return buf.Bytes(), nil
}
