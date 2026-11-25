// Package bridgewire is the wire of the bridge (FDR 0048 "Wire", contract v1,
// agreed between clown and spinclass 2026-11-25): the first bytes of every
// connection, the one-line answer, the codes and the limits. It has no
// network code of its own beyond reading and writing those lines; the shim
// (internal/*/bridgeshim) and the server's tests build on it. spinclass keeps a
// package of the same wire and publishes vectors both sides must pass; this
// package's tests carry the contract's own cases until the vectors are
// vendored (cmd/clown/testdata, compared by `just verify-bridge-vectors`).
//
//	stream     one connection; no multiplexer
//	hello      "BRIDGE/1 <service>\n", the first and only bytes the client sends
//	           before the answer; at most 64 bytes including the newline;
//	           <service> is [a-z][a-z0-9-]{0,31}
//	answer     "OK\n" (the connection is then the service's bytes, both ways;
//	           the server sends none before it) or "ERR <code> <text>\n" and close
//	           (<text> one line, at most 512 bytes)
//	version    the major only: a server accepts exactly its own major
package bridgewire

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Major is the wire's major version.
const Major = 1

const (
	// helloPrefix is the fixed start of a hello for this major.
	helloPrefix = "BRIDGE/"
	// MaxHelloLine bounds a hello line, newline included.
	MaxHelloLine = 64
	// MaxReplyLine bounds an answer line, newline included: "ERR " + a 512-byte
	// text + the code and separators.
	MaxReplyLine = 600
	// MaxErrText bounds the text of an ERR line.
	MaxErrText = 512
	// HelloTimeout is how long a server waits for a complete hello.
	HelloTimeout = 5 * time.Second
	// MaxStreamsPerSession caps concurrent streams of one session (code busy).
	MaxStreamsPerSession = 64
	// MaxSocketPath is the longest unix socket path (108 bytes with the NUL).
	MaxSocketPath = 107
)

// Codes of an ERR answer.
const (
	CodeVersion        = "version"
	CodeUnknownService = "unknown-service"
	CodeRefused        = "refused"
	CodeError          = "error"
	CodeBusy           = "busy"
)

// Reserved labels. A server answers unknown-service for any other label.
const (
	ServiceControl = "control"
	ServiceSession = "session"
	ServiceEgress  = "egress"
	ServiceHook    = "hook"
	ServiceChat    = "chat"
	ServiceModel   = "model"
	ServiceKeys    = "keys"
	ServiceStatus  = "status"
)

// ReservedServices is every label the contract reserves.
var ReservedServices = []string{ServiceEgress, ServiceControl, ServiceSession, ServiceHook, ServiceChat, ServiceModel, ServiceKeys, ServiceStatus}

var serviceRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ValidService reports whether s is a legal service label.
func ValidService(s string) bool { return serviceRE.MatchString(s) }

// Hello is the bytes a client sends for service. It refuses an illegal label.
func Hello(service string) ([]byte, error) {
	if !ValidService(service) {
		return nil, fmt.Errorf("bridge: %q is not a service label ([a-z][a-z0-9-]{0,31})", service)
	}
	return []byte(helloPrefix + strconv.Itoa(Major) + " " + service + "\n"), nil
}

// VersionError is a hello of another major.
type VersionError struct{ Shim int }

func (e *VersionError) Error() string {
	return fmt.Sprintf("bridge: hello of major %d, this wire is major %d", e.Shim, Major)
}

// ErrHelloTooLong is a hello line over MaxHelloLine bytes.
var ErrHelloTooLong = fmt.Errorf("bridge: hello longer than %d bytes", MaxHelloLine)

// ParseHello reads a complete hello line (its newline included) and returns the
// service. A malformed line is an error; a well-formed one of another major is a
// *VersionError.
func ParseHello(line string) (string, error) {
	if len(line) > MaxHelloLine {
		return "", ErrHelloTooLong
	}
	if !strings.HasSuffix(line, "\n") {
		return "", errors.New("bridge: hello without a newline")
	}
	body := strings.TrimSuffix(line, "\n")
	rest, ok := strings.CutPrefix(body, helloPrefix)
	if !ok {
		return "", fmt.Errorf("bridge: hello %q does not start with %q", clip(body, 24), helloPrefix)
	}
	major, service, ok := strings.Cut(rest, " ")
	if !ok {
		return "", errors.New("bridge: hello has no service label")
	}
	n, err := strconv.Atoi(major)
	if err != nil || n < 1 || major != strconv.Itoa(n) {
		return "", fmt.Errorf("bridge: hello major %q is not a number", clip(major, 8))
	}
	if n != Major {
		return "", &VersionError{Shim: n}
	}
	if !ValidService(service) {
		return "", fmt.Errorf("bridge: %q is not a service label", clip(service, 40))
	}
	return service, nil
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// ReadLine reads one line, newline included, one byte at a time and never past
// the newline: whatever follows belongs to the stream (a service's first bytes
// can follow the answer at once; a buffered reader would swallow them, which the
// first proof of concept did). max bounds the line; a longer one returns
// ErrLineTooLong with what was read.
func ReadLine(r io.Reader, max int) (string, error) {
	var line []byte
	one := make([]byte, 1)
	for len(line) < max {
		n, err := r.Read(one)
		if n == 1 {
			line = append(line, one[0])
			if one[0] == '\n' {
				return string(line), nil
			}
		}
		if err != nil {
			return string(line), err
		}
	}
	return string(line), ErrLineTooLong
}

// ErrLineTooLong is a line that did not end within its bound.
var ErrLineTooLong = errors.New("bridge: line too long")

// ReadHello reads the client's hello from r (exactly up to its newline) and
// parses it. A hello that does not end within MaxHelloLine bytes is
// ErrHelloTooLong.
func ReadHello(r io.Reader) (string, error) {
	line, err := ReadLine(r, MaxHelloLine)
	if errors.Is(err, ErrLineTooLong) {
		return "", ErrHelloTooLong
	}
	if err != nil {
		return "", fmt.Errorf("bridge: reading the hello: %w", err)
	}
	return ParseHello(line)
}

// OKLine is the answer that opens a stream.
const OKLine = "OK\n"

// ErrLine is the ERR answer for code and text (the text is cut to one line of
// at most MaxErrText bytes). The caller closes the connection after it.
func ErrLine(code, text string) []byte {
	text = strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	if len(text) > MaxErrText {
		text = text[:MaxErrText]
	}
	return []byte("ERR " + code + " " + text + "\n")
}

// VersionErrLine is the answer to a hello of another major.
func VersionErrLine(shimMajor int) []byte {
	return ErrLine(CodeVersion, fmt.Sprintf("server=%d shim=%d: the shim and the server speak different bridge majors; use the clown that matches this session's frontend", Major, shimMajor))
}

// Reply is a parsed answer.
type Reply struct {
	OK   bool
	Code string
	Text string
}

// ReadReply reads the answer to a hello, exactly up to its newline.
func ReadReply(r io.Reader) (Reply, error) {
	line, err := ReadLine(r, MaxReplyLine)
	if err != nil {
		if errors.Is(err, ErrLineTooLong) {
			return Reply{}, errors.New("bridge: the answer is too long")
		}
		return Reply{}, fmt.Errorf("bridge: reading the answer: %w", err)
	}
	return ParseReply(line)
}

// ParseReply parses an answer line (newline included).
func ParseReply(line string) (Reply, error) {
	body := strings.TrimSuffix(line, "\n")
	if body == "OK" {
		return Reply{OK: true}, nil
	}
	rest, ok := strings.CutPrefix(body, "ERR ")
	if !ok {
		return Reply{}, fmt.Errorf("bridge: an answer that is neither OK nor ERR: %q", clip(body, 40))
	}
	code, text, _ := strings.Cut(rest, " ")
	if code == "" {
		return Reply{}, errors.New("bridge: an ERR answer without a code")
	}
	return Reply{Code: code, Text: text}, nil
}

// DownMessage is the fixed text a shim shows when the bridge is unreachable
// (contract amendment f). reason says why: a dial failure, the end of the
// control stream, a version refusal.
func DownMessage(reason string) string {
	return fmt.Sprintf("clown: the bridge is down (%s); no tool or model call can leave this session; the session's frontend is gone or restarting", reason)
}
