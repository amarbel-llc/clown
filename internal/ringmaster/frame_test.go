package ringmaster

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"
)

func TestFrame_RoundTrip(t *testing.T) {
	env := Envelope{JSONRPC: "2.0", ID: json.Number("1"), Method: "Ping"}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, env); err != nil {
		t.Fatal(err)
	}
	// Frame must end with exactly one newline.
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		t.Fatalf("frame missing trailing newline: %q", buf.String())
	}
	got, err := ReadFrame(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "Ping" || got.ID != json.Number("1") {
		t.Errorf("got %+v", got)
	}
}

// TestFrame_BackToBack guards the doc-comment promise that callers can
// wrap r in a bufio.Reader and read multiple frames without losing bytes
// to over-read buffering.
func TestFrame_BackToBack(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, Envelope{ID: json.Number("1"), Method: "First"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(&buf, Envelope{ID: json.Number("2"), Method: "Second"}); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(&buf)
	first, err := ReadFrame(br)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ReadFrame(br)
	if err != nil {
		t.Fatal(err)
	}
	if first.Method != "First" || first.ID != json.Number("1") {
		t.Errorf("first: got %+v", first)
	}
	if second.Method != "Second" || second.ID != json.Number("2") {
		t.Errorf("second: got %+v", second)
	}
}
