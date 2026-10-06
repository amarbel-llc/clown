package jugglerloop

import "encoding/json"

// LedgerSchema is the version carried by every Ledger.
const LedgerSchema = 1

// EndReason is how a run's loop ended (FDR 0019 §4).
type EndReason string

const (
	EndTurn           EndReason = "end_turn"
	EndStepCap        EndReason = "step_cap"
	EndTimeout        EndReason = "timeout"
	EndToolError      EndReason = "tool_error"
	EndCannotComplete EndReason = "cannot_complete"
	// EndModelError is outside FDR 0019's five reasons: the model endpoint
	// itself failed (HTTP error, unparseable reply) before the deadline.
	// Run also returns a non-nil error in this case.
	EndModelError EndReason = "model_error"
	// EndMaxTokens is also outside FDR 0019's five reasons: the provider cut
	// the model's reply off at its token limit and the reply carried no tool
	// calls, so there is no finished answer for the evaluator to judge. (A
	// truncated reply that does carry tool calls runs them as usual.)
	EndMaxTokens EndReason = "max_tokens"
)

// Ledger is the runtime's measured record of a run, the evaluator's input.
// It is built from tool results and loop facts, never from model text.
type Ledger struct {
	Schema         int             `json:"schema"`
	Calls          []LedgerCall    `json:"calls"`
	End            LedgerEnd       `json:"end"`
	CannotComplete *CannotComplete `json:"cannot_complete"`
	Steps          int             `json:"steps"`
	ElapsedMS      int64           `json:"elapsed_ms"`
}

// LedgerCall is one tool-server call. Kind is the artifact kind the tool
// server declares for Tool (null when undeclared). The injected
// cannot_complete tool never appears here; it is recorded in
// Ledger.CannotComplete instead.
type LedgerCall struct {
	Tool  string   `json:"tool"`
	Kind  *string  `json:"kind"`
	OK    bool     `json:"ok"`
	URIs  []string `json:"uris"`
	Error string   `json:"error,omitempty"`
}

type LedgerEnd struct {
	Reason EndReason `json:"reason"`
}

type CannotComplete struct {
	Reason string `json:"reason"`
}

func newLedger() Ledger {
	return Ledger{Schema: LedgerSchema, Calls: []LedgerCall{}}
}

// URIExtractor pulls artifact URIs out of a successful tool result. It is
// the hook for tool-declared extraction; Config.URIExtractor defaults to
// ExtractURIsFromTopLevelFields.
type URIExtractor func(tool string, content json.RawMessage) []string

// ExtractURIsFromTopLevelFields is the default URI extraction rule: when
// content is a JSON object, collect its top-level "uri" and "url" string
// fields and every string in its top-level "uris" and "urls" arrays, in
// that order. Anything else (non-object content, nested fields, non-string
// values) yields no URIs. The result is never nil.
func ExtractURIsFromTopLevelFields(_ string, content json.RawMessage) []string {
	uris := []string{}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(content, &obj); err != nil {
		return uris
	}
	for _, key := range []string{"uri", "url"} {
		var s string
		if raw, ok := obj[key]; ok && json.Unmarshal(raw, &s) == nil && s != "" {
			uris = append(uris, s)
		}
	}
	for _, key := range []string{"uris", "urls"} {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			continue
		}
		for _, item := range items {
			var s string
			if json.Unmarshal(item, &s) == nil && s != "" {
				uris = append(uris, s)
			}
		}
	}
	return uris
}
