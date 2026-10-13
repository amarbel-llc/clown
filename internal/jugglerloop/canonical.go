package jugglerloop

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// CanonicalJSON reencodes doc (an arbitrary JSON document — a tool call's
// arguments) with object keys sorted and no insignificant whitespace
// (clown#255): the same bytes, however originally ordered or formatted,
// always canonicalize to the same output, so two writers that send the same
// argument bytes in a different order or layout produce the same
// args_digest. This is NOT full semantic equality: a number's original text
// is kept verbatim (see below), so `1` and `1.0` — the same numeric value,
// different literal text — canonicalize, and therefore digest, differently.
// Numbers are decoded with UseNumber so that text survives unchanged (no
// float64 reformatting or precision loss on a large integer); encoding/json's
// own Marshal already sorts map keys, which is the whole of the "sorted"
// rule — this function exists so the rule is stated and tested in exactly
// one place. The canonical form is precisely encoding/json's Marshal output
// with HTML-escaping disabled (SetEscapeHTML(false)): without that, `<`,
// `>`, `&` and U+2028/U+2029 inside a string value would be escaped to
// `<` etc, which a verifier re-deriving the digest with a generic JSON
// tool (not Go's encoder) would not reproduce.
func CanonicalJSON(doc json.RawMessage) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("canonicalizing: %w", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("canonicalizing: %w", err)
	}
	// Encode always appends a trailing newline; the canonical form has none.
	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), nil
}
