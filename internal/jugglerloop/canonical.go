package jugglerloop

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// CanonicalJSON reencodes doc (an arbitrary JSON document — a tool call's
// arguments) with object keys sorted and no insignificant whitespace
// (clown#255): the same logical document, however it was originally
// formatted or ordered, always canonicalizes to the same bytes, so two
// writers that produce logically equal arguments produce the same
// args_digest. Numbers are decoded with UseNumber so their original text
// survives unchanged (no float64 reformatting or precision loss on a large
// integer); encoding/json's own Marshal already sorts map keys, which is
// the whole of the "sorted" rule — this function exists so the rule is
// stated and tested in exactly one place.
func CanonicalJSON(doc json.RawMessage) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("canonicalizing: %w", err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("canonicalizing: %w", err)
	}
	return out, nil
}
