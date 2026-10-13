package jugglerloop

import (
	"encoding/json"
	"testing"
)

func canonicalOf(t *testing.T, doc string) string {
	t.Helper()
	out, err := CanonicalJSON(json.RawMessage(doc))
	if err != nil {
		t.Fatalf("CanonicalJSON(%s): %v", doc, err)
	}
	return string(out)
}

func TestCanonicalJSON_KeyOrderDoesNotAffectResult(t *testing.T) {
	a := canonicalOf(t, `{"b":1,"a":2,"c":{"z":3,"y":4}}`)
	b := canonicalOf(t, `{"c":{"y":4,"z":3},"a":2,"b":1}`)
	if a != b {
		t.Fatalf("two key orderings of the same document canonicalized differently:\n%s\n%s", a, b)
	}
}

func TestCanonicalJSON_WhitespaceDoesNotAffectResult(t *testing.T) {
	a := canonicalOf(t, `{"a":1,"b":[1,2,3]}`)
	b := canonicalOf(t, "{\n  \"a\" : 1,\n  \"b\": [ 1, 2, 3 ]\n}\n")
	if a != b {
		t.Fatalf("insignificant whitespace affected the result:\n%s\n%s", a, b)
	}
}

func TestCanonicalJSON_OneByteChangeDiffers(t *testing.T) {
	a := canonicalOf(t, `{"title":"file this issue"}`)
	b := canonicalOf(t, `{"title":"file this issues"}`)
	if a == b {
		t.Fatal("a one-byte change in a value produced the same canonical bytes")
	}
}

// TestCanonicalJSON_PreservesLargeIntegerPrecision: decoding with UseNumber
// keeps a number's original text, so an id or count past float64's 53-bit
// mantissa (2^53, beyond which not every integer is representable) survives
// unchanged rather than being silently reformatted or rounded.
func TestCanonicalJSON_PreservesLargeIntegerPrecision(t *testing.T) {
	const big = `{"id":9007199254740993}`
	got := canonicalOf(t, big)
	if got != big {
		t.Fatalf("canonical = %s, want the large integer preserved exactly as %s", got, big)
	}
}

func TestCanonicalJSON_InvalidJSONErrors(t *testing.T) {
	if _, err := CanonicalJSON(json.RawMessage(`{not json`)); err == nil {
		t.Fatal("want an error for invalid JSON")
	}
}

// TestArgsDigest_TwoOrderingsEqualOneByteChangeDiffers is the exact property
// clown#255 asks be tested: two logically-equal argument documents, however
// differently key-ordered, digest equal; a one-byte change in a value
// digests differently.
func TestArgsDigest_TwoOrderingsEqualOneByteChangeDiffers(t *testing.T) {
	a := ArgsDigest(json.RawMessage(`{"title":"x","body":{"a":1,"b":2}}`))
	b := ArgsDigest(json.RawMessage(`{"body":{"b":2,"a":1},"title":"x"}`))
	if a != b {
		t.Fatalf("two key orderings of the same arguments digested differently: %s != %s", a, b)
	}
	c := ArgsDigest(json.RawMessage(`{"title":"y","body":{"a":1,"b":2}}`))
	if a == c {
		t.Fatalf("a one-byte change in a value produced the same digest: %s", a)
	}
}

// TestArgsDigest_InvalidJSONFallsBackToRawBytes covers a tool call whose
// arguments were truncated by a token limit (validateToolArgs's own case):
// ArgsDigest must not panic or return empty, it falls back to hashing args
// as sent.
func TestArgsDigest_InvalidJSONFallsBackToRawBytes(t *testing.T) {
	invalid := json.RawMessage(`{"title":"cut off`)
	got := ArgsDigest(invalid)
	if got == "" {
		t.Fatal("want a non-empty digest even for invalid JSON")
	}
	want := ArgsDigest(invalid)
	if got != want {
		t.Fatal("ArgsDigest of the same invalid bytes must be deterministic")
	}
}
