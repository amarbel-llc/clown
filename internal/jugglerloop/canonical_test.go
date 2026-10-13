package jugglerloop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
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

// TestCanonicalJSON_HTMLCharsAreNotEscaped pins SetEscapeHTML(false)
// (clown#255 delta review finding 5): `<`, `>` and `&` in a string value
// must come out literal, not as their JSON numeric escapes (U+003C and
// friends), so a generic JSON tool (jq, a verifier not using Go's encoder)
// reproduces this function's output.
func TestCanonicalJSON_HTMLCharsAreNotEscaped(t *testing.T) {
	const doc = `{"q":"<a>&b"}`
	if got := canonicalOf(t, doc); got != doc {
		t.Fatalf("canonical = %s, want %s unescaped", got, doc)
	}
}

// TestCanonicalJSON_LineSeparatorsAreStillEscaped documents the one case
// SetEscapeHTML(false) does NOT cover (clown#255 delta review finding 5):
// Go's encoder escapes U+2028/U+2029 unconditionally (JS-embedding safety,
// not gated by the HTML-escaping flag), so a generic tool must special-case
// them to reproduce this function's output exactly.
func TestCanonicalJSON_LineSeparatorsAreStillEscaped(t *testing.T) {
	original := "a" + string(rune(0x2028)) + "b" + string(rune(0x2029)) + "c"
	doc, err := json.Marshal(map[string]string{"q": original})
	if err != nil {
		t.Fatal(err)
	}
	got := canonicalOf(t, string(doc))
	if strings.ContainsRune(got, rune(0x2028)) || strings.ContainsRune(got, rune(0x2029)) {
		t.Fatalf("canonical output still contains a raw line/paragraph separator rune, want it escaped: %q", got)
	}
	var roundtrip struct {
		Q string `json:"q"`
	}
	if err := json.Unmarshal([]byte(got), &roundtrip); err != nil {
		t.Fatalf("canonical output does not parse as JSON: %v (%q)", err, got)
	}
	if roundtrip.Q != original {
		t.Fatalf("round-trip = %q, want %q", roundtrip.Q, original)
	}
}

// TestCanonicalJSON_InvalidUTF8CanonicalizesToTheReplacementCharacter pins
// the other NOT-full-semantic-equality case the doc comment states
// (clown#255 delta review finding 5): invalid UTF-8 inside a string value
// is replaced with U+FFFD at DECODE time (json.Unmarshal's own documented
// behavior), so two inputs differing only in which invalid bytes appear
// there canonicalize equal.
func TestCanonicalJSON_InvalidUTF8CanonicalizesToTheReplacementCharacter(t *testing.T) {
	a := canonicalOf(t, "{\"q\":\"\xff\"}")
	b := canonicalOf(t, "{\"q\":\"\xfe\"}")
	if a != b {
		t.Fatalf("two different invalid byte sequences canonicalized differently: %s != %s", a, b)
	}
	if !strings.Contains(a, "�") {
		t.Fatalf("canonical = %s, want the replacement character", a)
	}
}

// TestCanonicalJSON_TrailingBytesAfterTheFirstValueAreIgnored pins the NIT
// noted in CanonicalJSON's own doc comment (clown#255 delta review finding
// 8, unreached in practice: args is always a single complete value).
func TestCanonicalJSON_TrailingBytesAfterTheFirstValueAreIgnored(t *testing.T) {
	a := canonicalOf(t, `{"a":1}`)
	b := canonicalOf(t, `{"a":1}junk`)
	if a != b {
		t.Fatalf("trailing bytes after the first value changed the result: %s != %s", a, b)
	}
}

// TestArgsDigest_TwoOrderingsEqualOneByteChangeDiffers is the exact property
// clown#255 asks be tested: two differently key-ordered encodings of the
// same argument bytes digest equal; a one-byte change in a value digests
// differently.
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
// unreachable through either of clown's codecs today (both guarantee valid
// JSON — see ArgsDigest's own doc comment), but if it ever happened,
// ArgsDigest must not panic or return empty; it falls back to hashing args
// exactly as sent, asserted directly here (not just that it's deterministic).
func TestArgsDigest_InvalidJSONFallsBackToRawBytes(t *testing.T) {
	invalid := json.RawMessage(`{"title":"cut off`)
	sum := sha256.Sum256(invalid)
	want := hex.EncodeToString(sum[:])
	if got := ArgsDigest(invalid); got != want {
		t.Fatalf("ArgsDigest(invalid) = %s, want sha256(raw bytes) = %s", got, want)
	}
}
