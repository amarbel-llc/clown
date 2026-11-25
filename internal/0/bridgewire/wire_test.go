package bridgewire

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestHelloIsTheContractsExactBytes(t *testing.T) {
	b, err := Hello("session")
	if err != nil || string(b) != "BRIDGE/1 session\n" {
		t.Fatalf("Hello = %q, %v", b, err)
	}
	for _, s := range []string{strings.Repeat("a", 32), "a", "a-b-9"} {
		if _, err := Hello(s); err != nil {
			t.Errorf("%q refused: %v", s, err)
		}
	}
	for _, s := range []string{"", strings.Repeat("a", 33), "Session", "9a", "-a", "a b", "a\n", "a_b", "é"} {
		if _, err := Hello(s); err == nil {
			t.Errorf("%q must be refused", s)
		}
	}
}

func TestParseHello(t *testing.T) {
	if svc, err := ParseHello("BRIDGE/1 control\n"); err != nil || svc != "control" {
		t.Errorf("good hello: %q, %v", svc, err)
	}
	// A 32-character label is the longest legal one; 33 is refused.
	if _, err := ParseHello("BRIDGE/1 " + strings.Repeat("a", 32) + "\n"); err != nil {
		t.Errorf("32-char label: %v", err)
	}
	if _, err := ParseHello("BRIDGE/1 " + strings.Repeat("a", 33) + "\n"); err == nil {
		t.Error("33-char label must be refused")
	}
	var ve *VersionError
	if _, err := ParseHello("BRIDGE/2 session\n"); !errors.As(err, &ve) || ve.Shim != 2 {
		t.Errorf("another major: %v", err)
	}
	for name, line := range map[string]string{
		"no newline":     "BRIDGE/1 session",
		"wrong prefix":   "HELLO/1 session\n",
		"no label":       "BRIDGE/1\n",
		"empty label":    "BRIDGE/1 \n",
		"major word":     "BRIDGE/x session\n",
		"major zero":     "BRIDGE/0 session\n",
		"leading zero":   "BRIDGE/01 session\n",
		"two spaces":     "BRIDGE/1  session\n",
		"carriage ret":   "BRIDGE/1 session\r\n",
		"over the limit": "BRIDGE/1 " + strings.Repeat("a", 60) + "\n",
	} {
		if _, err := ParseHello(line); err == nil {
			t.Errorf("%s: want a refusal", name)
		}
	}
}

// A hello of exactly 64 bytes passes the length bound; 65 does not.
func TestHelloLineBound(t *testing.T) {
	r := strings.NewReader("BRIDGE/1 " + strings.Repeat("a", 32) + "\n")
	if _, err := ReadHello(r); err != nil {
		t.Errorf("a legal hello: %v", err)
	}
	// 70 bytes without a newline: too long, read no further than the bound.
	long := strings.Repeat("x", 70)
	r2 := strings.NewReader(long)
	if _, err := ReadHello(r2); !errors.Is(err, ErrHelloTooLong) {
		t.Errorf("an over-long hello: %v", err)
	}
	if r2.Len() != 70-MaxHelloLine {
		t.Errorf("read %d bytes past the bound's limit, want exactly %d read", 70-r2.Len(), MaxHelloLine)
	}
	// A hello cut short by the end of the stream, before its newline.
	if _, err := ReadHello(strings.NewReader("BRIDGE/1 sess")); err == nil {
		t.Error("a hello cut short must be refused")
	}
}

// The reply is read exactly to its newline: the service's first bytes, which can
// follow the answer at once, stay in the stream (the proof of concept swallowed
// them through a buffered reader).
func TestReadReplyLeavesTheServicesFirstBytes(t *testing.T) {
	r := strings.NewReader("OK\nfirst service bytes")
	rep, err := ReadReply(r)
	if err != nil || !rep.OK {
		t.Fatalf("reply: %+v, %v", rep, err)
	}
	rest, _ := io.ReadAll(r)
	if string(rest) != "first service bytes" {
		t.Errorf("the stream after the answer = %q", rest)
	}
}

func TestReplies(t *testing.T) {
	rep, err := ParseReply("ERR refused restart-child-session needs the right \"restart\"\n")
	if err != nil || rep.OK || rep.Code != "refused" || !strings.HasPrefix(rep.Text, "restart-child-session needs") {
		t.Errorf("ERR reply: %+v, %v", rep, err)
	}
	if rep, err := ParseReply("ERR busy\n"); err != nil || rep.Code != "busy" || rep.Text != "" {
		t.Errorf("ERR without text: %+v, %v", rep, err)
	}
	for _, bad := range []string{"", "OK \n", "ok\n", "ERR\n", "ERR \n", "HELLO\n", "OKAY\n"} {
		if _, err := ParseReply(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	// An answer that never ends is cut at the bound, not read forever.
	if _, err := ReadReply(strings.NewReader(strings.Repeat("E", 5000))); err == nil {
		t.Error("an endless answer must fail")
	}
}

func TestErrLineIsOneBoundedLine(t *testing.T) {
	got := ErrLine(CodeRefused, "line one\nline two\r"+strings.Repeat("x", 600))
	if !bytes.HasSuffix(got, []byte("\n")) || bytes.Count(got, []byte("\n")) != 1 {
		t.Errorf("not one line: %q", got)
	}
	if len(got) > len("ERR refused ")+MaxErrText+1 {
		t.Errorf("text not bounded: %d bytes", len(got))
	}
	if string(VersionErrLine(2)[:len("ERR version server=1 shim=2")]) != "ERR version server=1 shim=2" {
		t.Errorf("version line: %q", VersionErrLine(2))
	}
	if rep, err := ParseReply(string(VersionErrLine(3))); err != nil || rep.Code != CodeVersion {
		t.Errorf("a version line must parse: %+v, %v", rep, err)
	}
}

func TestDownMessageIsTheFixedForm(t *testing.T) {
	want := "clown: the bridge is down (EOF on control); no tool or model call can leave this session; the session's frontend is gone or restarting"
	if got := DownMessage("EOF on control"); got != want {
		t.Errorf("down message\n got %q\nwant %q", got, want)
	}
}

func TestReservedLabelsAreLegal(t *testing.T) {
	for _, s := range ReservedServices {
		if !ValidService(s) {
			t.Errorf("reserved label %q is not a legal label", s)
		}
	}
	if len(ReservedServices) != 8 {
		t.Errorf("the contract reserves 8 labels, have %d", len(ReservedServices))
	}
}
