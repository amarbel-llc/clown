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
// args_digest. This is NOT full semantic equality, in two ways: a number's
// original text is kept verbatim (see below), so `1` and `1.0` — the same
// numeric value, different literal text — canonicalize, and therefore
// digest, differently; and invalid UTF-8 inside a string value is replaced
// with U+FFFD when DECODED (json.Unmarshal's own documented behavior — by
// the time the encoder sees the value, it is already a valid Go string), so
// two inputs whose only difference is WHICH invalid bytes appear there
// canonicalize, and digest, equal. Numbers are decoded with UseNumber so
// that text survives
// unchanged (no float64 reformatting or precision loss on a large integer);
// encoding/json's own Marshal already sorts map keys, which is the whole of
// the "sorted" rule — this function exists so the rule is stated and
// tested in exactly one place. The canonical form is precisely
// encoding/json's Marshal output with HTML-escaping disabled
// (SetEscapeHTML(false)): without that, `<`, `>` and `&` inside a string
// value would come out as their JSON numeric escapes (U+003C and
// friends) instead of the literal character, which a verifier
// re-deriving the digest with a generic JSON tool (not Go's encoder)
// would not reproduce.
// U+2028 and U+2029 are still escaped regardless of SetEscapeHTML (Go's
// encoder always does this, for JavaScript-embedding safety, not gated by
// the HTML-escaping flag), so a generic tool must still special-case those
// two code points to reproduce this function's output exactly.
//
// Only the FIRST JSON value in doc is decoded; trailing bytes after it
// (e.g. `{"a":1}junk`) are silently ignored, so two inputs differing only
// past their first value's closing bracket would canonicalize equal. args
// is always a single complete value in practice (a tool call's arguments
// object, decoded as one), so this is unreached today; noted for whoever
// next extends this function to accept pre-split input.
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
