package catalogcontract

import (
	"strings"
	"testing"
)

// The expected ids are toolbox(1)'s `digest -a blake2b256` over the canonical
// bytes, an independent implementation of the same markl id.
func TestLockDigestMatchesAnIndependentBlake2b256MarklID(t *testing.T) {
	for _, tc := range []struct{ schema, want string }{
		{
			`{ "type": "object",  "properties": { "a": { "type": "string" } } }`,
			"blake2b256-t6s4wjfpttlrxp75yvxmf8f8v9a2qfvmrduj8gz4jcjp25w84g4sjk5c2y",
		},
		{`{ }`, "blake2b256-czw62gk6cfsu85jkvgcta5gd9ul0z0uwwe29wmqjapt7qlmcvzvqzamyau"},
	} {
		got, err := LockDigest([]byte(tc.schema))
		if err != nil {
			t.Fatalf("LockDigest(%s): %v", tc.schema, err)
		}
		if got != tc.want {
			t.Errorf("LockDigest(%s) = %s, want %s", tc.schema, got, tc.want)
		}
	}
}

func TestCanonicalJSONSortsKeysDropsWhitespaceAndKeepsTextVerbatim(t *testing.T) {
	got, err := CanonicalJSON([]byte(`{"z": 1.50, "a": ["<b>&", {"y": true, "x": null}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":["<b>&",{"x":null,"y":true}],"z":1.50}`
	if string(got) != want {
		t.Errorf("CanonicalJSON = %s, want %s", got, want)
	}
}

func TestLockDigestRefusesWhatIsNotOneJSONValue(t *testing.T) {
	for _, in := range []string{"", "{", `{} {}`, "not json"} {
		if got, err := LockDigest([]byte(in)); err == nil {
			t.Errorf("LockDigest(%q) = %s, want an error", in, got)
		}
	}
}

func TestBlech32IsBech32WithAHyphenSeparator(t *testing.T) {
	// BIP173's test vector "a12uel5l" (HRP "a", no data), with "-" for "1".
	got := blech32("a", nil)
	if got != "a-2uel5l" {
		t.Errorf("blech32(a, nil) = %s, want a-2uel5l", got)
	}
	// The data part, after the separator, is drawn from the alphabet, which
	// leaves out 1, b, i and o; the HRP itself is free text.
	_, data, _ := strings.Cut(blech32("blake2b256", []byte{0xff, 0, 1}), "-")
	if strings.ContainsAny(data, "1bio") {
		t.Errorf("blech32 data part %q used a character outside its alphabet", data)
	}
}
