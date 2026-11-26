// Package bridgewire is the wire of the bridge (FDR 0048 "Wire", contract v1,
// agreed between clown and spinclass 2026-11-25): the first bytes of every
// connection, the one-line answer, the codes and the limits. It has no network
// code of its own beyond reading and writing those lines; the shim
// (internal/*/bridgeshim) and the tests' fake servers build on it.
//
// spinclass keeps the other implementation of the same wire (internal/0/bridge,
// stdlib only) and publishes testdata/vectors.json with its sha256 file; this
// repo vendors both byte for byte (testdata/vectors.json and .sha256), runs every
// case against THIS package (wire_test.go), and pins the copy to the vendored
// digest file there. The comparison with spinclass's published files is
// `just verify-bridge-vectors DIR` (by hand, here) and spinclass's own gate,
// which locks this repo.
//
//	stream     one connection; no multiplexer
//	hello      "BRIDGE/1 <service>\n", the first and only bytes the client sends
//	           before the answer; at most 64 bytes including the newline;
//	           <service> is [a-z][a-z0-9-]{0,31}
//	answer     "OK\n" (the connection is then the service's bytes, both ways; the
//	           server sends none before it) or "ERR <code> <text>\n" and close
//	           (<text> one line, at most 512 bytes, no CR)
//	version    the major only: a server accepts exactly its own major
package bridgewire

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Major is the wire's major version.
const Major = 1

const (
	// MaxHelloBytes bounds a hello line, newline included.
	MaxHelloBytes = 64
	// MaxReplyTextBytes bounds the text of an ERR answer.
	MaxReplyTextBytes = 512
	// maxReplyBytes bounds an answer line as read: a hello-sized head (the word
	// ERR, a code, spaces) and a text, newline included.
	maxReplyBytes = MaxHelloBytes + MaxReplyTextBytes
	// HelloTimeout is how long a server waits for a complete hello; the
	// connection is then closed with no reply.
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

var codes = map[string]bool{CodeVersion: true, CodeUnknownService: true, CodeRefused: true, CodeError: true, CodeBusy: true}

// Reserved labels. A server answers unknown-service for any other label, and
// for a reserved one it does not serve.
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

// ReservedServices is every label the contract reserves, in the contract's order.
var ReservedServices = []string{ServiceEgress, ServiceControl, ServiceSession, ServiceHook, ServiceChat, ServiceModel, ServiceKeys, ServiceStatus}

// Errors of the line readers and parsers. Both wrap a short, sanitised detail.
var (
	// ErrMalformed is a line that is not what the contract says, or ended
	// (end of stream) before its newline.
	ErrMalformed = errors.New("bridge: malformed")
	// ErrTooLong is a line that did not end within its bound.
	ErrTooLong = errors.New("bridge: line too long")
)

var serviceRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ValidService reports whether s is a legal service label.
func ValidService(s string) bool { return serviceRE.MatchString(s) }

// Hello is a parsed hello line.
type Hello struct {
	Major   int
	Service string
}

// FormatHello is the bytes a client sends. It refuses what ParseHello refuses.
func FormatHello(major int, service string) ([]byte, error) {
	if major < 1 || major > 999 || !ValidService(service) {
		return nil, fmt.Errorf("%w: hello of major %d, service %q", ErrMalformed, major, escape(service, 40))
	}
	return []byte("BRIDGE/" + strconv.Itoa(major) + " " + service + "\n"), nil
}

// ParseHello parses one hello line, its newline included. A line over
// MaxHelloBytes is ErrTooLong; any other departure from the contract is
// ErrMalformed (a foreign major with a well-formed label is NOT an error here:
// the caller judges the major, see Decide).
func ParseHello(line []byte) (Hello, error) {
	if len(line) > MaxHelloBytes {
		return Hello{}, fmt.Errorf("%w: a hello of %d bytes (bound %d)", ErrTooLong, len(line), MaxHelloBytes)
	}
	if len(line) == 0 || line[len(line)-1] != '\n' {
		if len(line) >= MaxHelloBytes {
			return Hello{}, fmt.Errorf("%w: no newline within %d bytes", ErrTooLong, MaxHelloBytes)
		}
		return Hello{}, fmt.Errorf("%w: a hello without its newline", ErrMalformed)
	}
	body := string(line[:len(line)-1])
	rest, ok := strings.CutPrefix(body, "BRIDGE/")
	if !ok {
		return Hello{}, fmt.Errorf("%w: hello %q", ErrMalformed, escape(body, 32))
	}
	majorStr, service, ok := strings.Cut(rest, " ")
	if !ok {
		return Hello{}, fmt.Errorf("%w: hello without a service", ErrMalformed)
	}
	major, ok := parseMajor(majorStr)
	if !ok {
		return Hello{}, fmt.Errorf("%w: hello major %q", ErrMalformed, escape(majorStr, 8))
	}
	if !ValidService(service) {
		return Hello{}, fmt.Errorf("%w: service label %q", ErrMalformed, escape(service, 32))
	}
	return Hello{Major: major, Service: service}, nil
}

// parseMajor: one to three digits, no sign, no leading zero, at least 1.
func parseMajor(s string) (int, bool) {
	if len(s) < 1 || len(s) > 3 || s[0] == '0' {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, _ := strconv.Atoi(s)
	return n, true
}

// Printable is s with every byte outside printable ASCII escaped, at most
// MaxReplyTextBytes of it: what a text read off the socket may be shown as.
func Printable(s string) string { return escape(s, MaxReplyTextBytes) }

// escape makes s printable and at most n bytes (an error text must not carry
// raw control bytes from the peer).
func escape(s string, n int) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < n; {
		r, w := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && w == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r > 0x7e:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
		i += w
	}
	out := b.String()
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// readLine reads one line, newline included, ONE BYTE AT A TIME and never past
// the newline: whatever follows belongs to the stream (a service's first bytes
// can follow the answer at once; a buffered reader would swallow them, which the
// first proof of concept did). More than max bytes without a newline is
// ErrTooLong; the end of the stream before the newline is ErrMalformed.
func readLine(r io.Reader, max int) ([]byte, error) {
	line := make([]byte, 0, 80)
	one := make([]byte, 1)
	for len(line) < max {
		n, err := r.Read(one)
		if n == 1 {
			line = append(line, one[0])
			if one[0] == '\n' {
				return line, nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return line, fmt.Errorf("%w: the stream ended after %d bytes, before the newline", ErrMalformed, len(line))
			}
			return line, fmt.Errorf("bridge: reading a line: %w", err)
		}
	}
	return line, fmt.Errorf("%w: no newline within %d bytes", ErrTooLong, max)
}

// ReadHello reads a client's hello from r (exactly up to its newline, at most
// MaxHelloBytes) and parses it.
func ReadHello(r io.Reader) (Hello, error) {
	line, err := readLine(r, MaxHelloBytes)
	if err != nil {
		return Hello{}, err
	}
	return ParseHello(line)
}

// OK is the answer that opens a stream.
func OK() []byte { return []byte("OK\n") }

// MalformedReply is the answer to a hello that does not parse.
func MalformedReply() []byte { return []byte("ERR error malformed hello\n") }

// FormatErr is the ERR answer for code and text: an unknown code becomes
// `error`; CR and LF in the text become spaces (before the cut); the text is cut
// to MaxReplyTextBytes bytes at a rune boundary. The caller closes the
// connection after it.
func FormatErr(code, text string) []byte {
	if !codes[code] {
		code = CodeError
	}
	text = strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	if len(text) > MaxReplyTextBytes {
		cut := MaxReplyTextBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return []byte("ERR " + code + " " + text + "\n")
}

// VersionHint is the server's hint in a version refusal.
const VersionHint = "rebuild or restart the session so the shim and the frontend match"

// Decide is the server's judgment of a parsed hello: the major first (a foreign
// major is `ERR version server=1 shim=N: ...` whatever the label), then whether
// the label is both reserved and served. It returns the bytes to answer and
// whether the stream is accepted.
func Decide(h Hello, served map[string]bool) ([]byte, bool) {
	if h.Major != Major {
		return FormatErr(CodeVersion, fmt.Sprintf("server=%d shim=%d: %s", Major, h.Major, VersionHint)), false
	}
	reserved := false
	for _, s := range ReservedServices {
		if s == h.Service {
			reserved = true
		}
	}
	if !reserved || !served[h.Service] {
		return FormatErr(CodeUnknownService, h.Service), false
	}
	return OK(), true
}

// ReadReply reads the answer to a hello, exactly up to its newline. ok is true
// for "OK"; otherwise code and text are the ERR answer's. A reply that is neither
// is ErrMalformed (an unknown code, a text over MaxReplyTextBytes or with a CR
// included); one that does not end within the bound is ErrTooLong.
func ReadReply(r io.Reader) (ok bool, code, text string, err error) {
	line, err := readLine(r, maxReplyBytes)
	if err != nil {
		return false, "", "", err
	}
	body := string(line[:len(line)-1])
	if body == "OK" {
		return true, "", "", nil
	}
	rest, isErr := strings.CutPrefix(body, "ERR ")
	if !isErr {
		return false, "", "", fmt.Errorf("%w: an answer that is neither OK nor ERR: %q", ErrMalformed, escape(body, 32))
	}
	code, text, _ = strings.Cut(rest, " ")
	if !codes[code] {
		return false, "", "", fmt.Errorf("%w: an ERR answer with the code %q", ErrMalformed, escape(code, 24))
	}
	if len(text) > MaxReplyTextBytes || strings.ContainsRune(text, '\r') {
		return false, "", "", fmt.Errorf("%w: an ERR text over %d bytes or with a carriage return", ErrMalformed, MaxReplyTextBytes)
	}
	return false, code, text, nil
}

// RefusedUnadmitted is what a listener answers a peer it does not admit (no
// cgroup file yet, a file it cannot use, a peer outside the unit): always
// `ERR refused <text>`, never a silent close. The shim's retry of `control`
// rests on it (a close is retried too, but `refused` is the contract's answer;
// round 2, M4). The text is escaped and bounded.
func RefusedUnadmitted(reason string) []byte {
	return FormatErr(CodeRefused, "the peer is not admitted: "+reason)
}

// SocketPath is the bridge socket inside a bridge directory: <dir>/bridge.sock,
// refused when the path would pass the unix limit (the bridge then stays off, a
// launch never fails for it).
func SocketPath(dir string) (string, error) {
	dir = strings.TrimRight(dir, "/")
	if dir == "" {
		return "", errors.New("bridge: the bridge directory is empty; the bridge stays off")
	}
	p := dir + "/" + SocketName
	if len(p) > MaxSocketPath {
		return "", fmt.Errorf("bridge: the socket path %q is %d bytes, over the unix socket limit of %d; the bridge stays off", p, len(p), MaxSocketPath)
	}
	return p, nil
}

// Names the contract fixes.
const (
	SocketName = "bridge.sock"
	EnvDir     = "CLOWN_BRIDGE_DIR"
	EnvUnit    = "CLOWN_BRIDGE_UNIT"
	// EnvCgroupFile names the file clown writes, once the unit's ControlGroup is
	// known, holding that exact cgroup path (option A, agreed with spinclass);
	// serve reads it per connection and admits a peer only if the peer's cgroup
	// EQUALS it (strictly: not a descendant; peercheck.CheckCgroupExact). A
	// missing, empty or unreadable file admits nobody. It lives beside, never
	// inside, the read-only bridge directory.
	EnvCgroupFile = "CLOWN_BRIDGE_CGROUP_FILE"
	// CgroupFileName is that file's name, under <runtime>/clown/launch/<key>/.
	CgroupFileName = "bridge.cgroup"
)

// DownMessage is the fixed text a shim shows when the bridge is unreachable
// (contract amendment f). reason says why: a dial failure, the end of the
// control stream, a version refusal.
func DownMessage(reason string) string {
	return fmt.Sprintf("clown: the bridge is down (%s); no tool or model call can leave this session; the session's frontend is gone or restarting", reason)
}

// WriteCgroupFile records the unit's exact cgroup for the serve at path (the
// bridge.cgroup file, CgroupFileName): 0600, a regular file, exactly the path
// with no trailing newline, written to path+".tmp" and renamed over so a reader
// sees the old value or the new, never a part, and a symlink at path is
// replaced rather than followed. It refuses a value that is not an absolute
// path with no trailing slash, newline or NUL. This is the writer clown uses
// at launch (cmd/clown's writeCgroup adds the check that the value is the
// unit's own); peercheck.ReadUnitCgroupFile is the reader. Exported so an
// end-to-end test can stand in for clown's half of the seam.
func WriteCgroupFile(path, cgroup string) error {
	if path == "" {
		return errors.New("bridge: no cgroup file path")
	}
	if !strings.HasPrefix(cgroup, "/") || strings.HasSuffix(cgroup, "/") || strings.ContainsAny(cgroup, "\n\x00") {
		return fmt.Errorf("bridge: unusable cgroup %q", cgroup)
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(cgroup)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// MaxUnusableReasonBytes bounds the reason UnusableMessage prints: the reason is
// cut at a rune boundary to at most this many bytes and then ends in "...".
// DownMessage applies no bound; its reasons are the shim's own fixed strings.
const MaxUnusableReasonBytes = 200

// UnusableMessage is the fixed text a shim shows when the bridge cannot be used
// although a frontend answered or was reachable: an `ERR version` on control or
// on a data stream (the reason is the server's text), or a control stream that
// was not admitted within the retry window (the reason is the last refusal
// text). DownMessage stays for an unreachable bridge and an ended control
// stream. spinclass's bridge.UnusableMessage is the other implementation; the
// vectors (op unusable-message) pin both.
//
// The reason is printed on one line with no control characters: every control
// rune, every format rune (the bidi overrides among them), U+2028, U+2029 and
// every byte that is not valid UTF-8 becomes one space, and what remains is cut
// to MaxUnusableReasonBytes.
func UnusableMessage(reason string) string {
	return fmt.Sprintf("clown: the bridge is unusable (%s); no tool or model call can leave this session; check the serve's log and spinclass session-health, and that the session's bridge setting matches the serve's", boundedReason(reason))
}

func boundedReason(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == utf8.RuneError, unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == ' ', r == ' ':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	clean := b.String()
	if len(clean) <= MaxUnusableReasonBytes {
		return clean
	}
	cut := MaxUnusableReasonBytes
	for cut > 0 && !utf8.RuneStart(clean[cut]) {
		cut--
	}
	return clean[:cut] + "..."
}
