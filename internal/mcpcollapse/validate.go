package mcpcollapse

// validate.go is a MINIMAL, deliberately-partial JSON-Schema validator — NOT a
// full JSON-Schema engine. clown keeps a zero-heavy-dependency posture (a full
// validator is a large dependency and gomod2nix/vendoring churn for a
// prototype), so mcp_call validates args against the stored inputSchema with a
// hand-rolled check that covers the real footguns and nothing more:
//
//   - the schema's declared type:object is honored (args must be a JSON object);
//   - every name in the schema's "required" array must be present in args;
//   - every top-level property present in args whose schema declares a "type"
//     must match that type (string/number/integer/boolean/object/array/null).
//
// It does NOT recurse into nested object/array item schemas, does NOT handle
// enum/const/pattern/format/oneOf/anyOf/$ref, and does NOT reject unknown
// properties. Those gaps are acceptable: the collapse's goal is to catch the
// missing-required-arg and wrong-typed-arg mistakes an agent actually makes
// before they cost an upstream round-trip, not to be a conformant validator.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// validateArgs checks args against schema per the minimal rules above. A nil or
// empty schema, or one that does not declare type:object, imposes no constraint
// (the upstream is the authority for anything the aggregator cannot cheaply
// check) — so validation only ever REJECTS on a constraint it can positively
// evaluate, never on an unrecognized schema shape. Returns nil when args
// satisfy the checked constraints, else an error naming the first violation.
func validateArgs(schema, args json.RawMessage) error {
	if len(schema) == 0 {
		return nil
	}

	var s struct {
		Type       string                     `json:"type"`
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		// A schema we cannot parse is not something we can validate against;
		// defer to the upstream rather than blocking a call on our own limits.
		return nil
	}

	// We only validate object-shaped input schemas — the near-universal MCP
	// tool-input shape. Anything else is left to the upstream.
	if s.Type != "" && s.Type != "object" {
		return nil
	}

	var argsObj map[string]json.RawMessage
	if err := json.Unmarshal(args, &argsObj); err != nil {
		return fmt.Errorf("args must be a JSON object")
	}

	for _, req := range s.Required {
		if _, ok := argsObj[req]; !ok {
			return fmt.Errorf("missing required field %q", req)
		}
	}

	// Check top-level property types in a stable order so the reported error is
	// deterministic across runs.
	names := make([]string, 0, len(argsObj))
	for name := range argsObj {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		propSchema, ok := s.Properties[name]
		if !ok {
			continue // unknown property — not rejected (minimal validator)
		}
		var p struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(propSchema, &p); err != nil || p.Type == "" {
			continue // no declared type to check against
		}
		if err := checkType(name, p.Type, argsObj[name]); err != nil {
			return err
		}
	}
	return nil
}

// checkType verifies that raw's JSON type matches the schema-declared want for
// property name. "number" accepts any JSON number; "integer" additionally
// requires no fractional part. Anything else is compared by JSON kind.
func checkType(name, want string, raw json.RawMessage) error {
	got := jsonKind(raw)
	switch want {
	case "number":
		if got != "number" {
			return typeErr(name, want, got)
		}
	case "integer":
		if got != "number" || !isIntegerNumber(raw) {
			return typeErr(name, "integer", got)
		}
	default:
		if got != want {
			return typeErr(name, want, got)
		}
	}
	return nil
}

// typeErr formats a wrong-type violation naming the field, expected type, and
// observed type.
func typeErr(name, want, got string) error {
	return fmt.Errorf("field %q should be %s, got %s", name, want, got)
}

// jsonKind classifies a raw JSON value into a JSON-Schema primitive type name:
// object, array, string, number, boolean, or null.
func jsonKind(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "null"
	}
	switch trimmed[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

// isIntegerNumber reports whether a JSON number literal has no fractional or
// exponent part — i.e. it is an integer, so the "integer" schema type accepts
// it. A value like 3.0 is treated as non-integer here (it carries a decimal
// point), which is stricter than JSON-Schema's numeric equality but adequate
// for the footgun this catches.
func isIntegerNumber(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return !strings.ContainsAny(s, ".eE")
}
