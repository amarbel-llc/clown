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

	"code.linenisgreat.com/clown/internal/jugglereval"
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
	EvaluatorKindJQ = jugglereval.KindJQ
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

	// Tools is the REQUIRED allowlist of exact tool names as moxy advertises
	// them (e.g. "ring_create_issue"); anything not listed is never offered
	// to the model.
	Tools []string `toml:"tools"`

	// Env is passed into the agent's transient unit (and so to moxy and its
	// moxins, which moxyfile(5) cannot configure). Keys spawn owns —
	// CLOWN_SESSION_ID, TROUPE_XMPP_*, TROUPE_MINT_* and JUGGLER_* — are
	// rejected.
	Env map[string]string `toml:"env,omitempty"`

	Evaluator Evaluator `toml:"evaluator"`
	Limits    Limits    `toml:"limits"`
}

// TemplateFields are the fields a brief template may leave empty: the
// spawner fills them (principal, parent, room from the run; task from the
// run's input) before the filled brief is validated with Parse.
var TemplateFields = []string{"principal", "parent", "room", "task"}

// IsPrincipalEnvKey reports whether an environment key carries a principal's
// identity: CLOWN_SESSION_ID (the per-instance key, FDR 0019 §2), any
// TROUPE_XMPP_* variable (the XMPP credential reference and its settings),
// or any TROUPE_MINT_* variable (the spawner's minter credential, which no
// agent or tool server may inherit).
func IsPrincipalEnvKey(key string) bool {
	return key == "CLOWN_SESSION_ID" || strings.HasPrefix(key, "TROUPE_XMPP_") || strings.HasPrefix(key, "TROUPE_MINT_")
}

// ReservedEnvKey reports whether an [env] key is one `juggler spawn` sets
// itself and a brief therefore may not.
func ReservedEnvKey(key string) bool {
	return IsPrincipalEnvKey(key) || strings.HasPrefix(key, "JUGGLER_")
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
	b, err := decode(data)
	if err != nil {
		return nil, err
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return b, nil
}

// ParseTemplate is Parse for a brief template: it applies the same checks
// except that the TemplateFields may be empty.
func ParseTemplate(data []byte) (*Brief, error) {
	b, err := decode(data)
	if err != nil {
		return nil, err
	}
	if err := b.ValidateTemplate(); err != nil {
		return nil, err
	}
	return b, nil
}

func decode(data []byte) (*Brief, error) {
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
func (b *Brief) Validate() error { return b.validate(false) }

// ValidateTemplate is Validate with the TemplateFields allowed empty.
func (b *Brief) ValidateTemplate() error { return b.validate(true) }

func (b *Brief) validate(template bool) error {
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
		if template && isTemplateField(r.field) {
			continue
		}
		if strings.TrimSpace(r.value) == "" {
			return fmt.Errorf("jugglerbrief: %s: must not be empty", r.field)
		}
	}
	if len(b.Tools) == 0 {
		return fmt.Errorf("jugglerbrief: tools: must list at least one tool name")
	}
	seen := map[string]bool{}
	for _, t := range b.Tools {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("jugglerbrief: tools: tool names must not be empty")
		}
		if seen[t] {
			return fmt.Errorf("jugglerbrief: tools: %q is listed twice", t)
		}
		seen[t] = true
	}
	for k := range b.Env {
		if k == "" || strings.Contains(k, "=") {
			return fmt.Errorf("jugglerbrief: env: invalid key %q", k)
		}
		if ReservedEnvKey(k) {
			return fmt.Errorf("jugglerbrief: env: %q is set by juggler spawn and may not appear in a brief", k)
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

func isTemplateField(field string) bool {
	for _, f := range TemplateFields {
		if f == field {
			return true
		}
	}
	return false
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
