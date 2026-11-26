package bridgewire

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/0/bridgevectors"
)

// The vectors are spinclass's (internal/0/bridge/testdata/vectors.json and its
// .sha256, the contract's one home), vendored byte for byte with the digest file
// they were published with. The pin is that digest file, not a constant written
// here: re-copy BOTH files from spinclass, never edit either (round 3, C-M1).
const vectorsFile = "testdata/vectors.json"

func TestVendoredVectorsAreSpinclasssBytes(t *testing.T) {
	b, err := os.ReadFile(vectorsFile)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(vectorsFile + ".sha256")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]) + "  vectors.json\n"; got != string(want) {
		t.Errorf("%s has sha256 %s, but %s.sha256 says %q; re-copy both files from spinclass", vectorsFile, hex.EncodeToString(sum[:]), vectorsFile, want)
	}
}

// The recorded digest (what spinclass's gate compares) is the vendored file's.
func TestRecordedVectorsDigestIsTheVendoredFiles(t *testing.T) {
	b, err := os.ReadFile(vectorsFile)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != bridgevectors.SHA256 {
		t.Errorf("bridgevectors.SHA256 = %s but %s hashes to %s: update internal/*/bridgevectors when re-vendoring", bridgevectors.SHA256, vectorsFile, got)
	}
	if bridgevectors.Source == "" {
		t.Error("bridgevectors.Source is empty: record the spinclass commit the vectors came from")
	}
}

// gen builds a long string without writing it out in the vectors file.
type gen struct {
	Prefix string `json:"prefix"`
	Repeat string `json:"repeat"`
	N      int    `json:"n"`
	Suffix string `json:"suffix"`
}

func (g *gen) String() string { return g.Prefix + strings.Repeat(g.Repeat, g.N) + g.Suffix }

type vector struct {
	Name       string   `json:"name"`
	Op         string   `json:"op"`
	Input      string   `json:"input"`
	InputGen   *gen     `json:"input_gen"`
	Served     []string `json:"served"`
	ParseErr   string   `json:"parse_err"`
	Major      int      `json:"major"`
	Service    string   `json:"service"`
	ServiceGen *gen     `json:"service_gen"`
	Reply      string   `json:"reply"`
	ReplyGen   *gen     `json:"reply_gen"`
	Accept     bool     `json:"accept"`
	Rest       string   `json:"rest"`
	OK         bool     `json:"ok"`
	Code       string   `json:"code"`
	Text       string   `json:"text"`
	TextGen    *gen     `json:"text_gen"`

	// socket-path
	Dir     string `json:"dir"`
	DirGen  *gen   `json:"dir_gen"`
	Path    string `json:"path"`
	PathGen *gen   `json:"path_gen"`
	PathLen int    `json:"path_len"`
	PathErr string `json:"path_err"`
	// down-message
	Reason     string `json:"reason"`
	ReasonGen  *gen   `json:"reason_gen"`
	Message    string `json:"message"`
	MessageGen *gen   `json:"message_gen"`
	// constants
	Constants map[string]any `json:"constants"`
}

func (v vector) input() string {
	if v.InputGen != nil {
		return v.InputGen.String()
	}
	return v.Input
}

func (v vector) wantReply() string {
	if v.ReplyGen != nil {
		return v.ReplyGen.String()
	}
	return v.Reply
}

func (v vector) wantService() string {
	if v.ServiceGen != nil {
		return v.ServiceGen.String()
	}
	return v.Service
}

func (v vector) text() string {
	if v.TextGen != nil {
		return v.TextGen.String()
	}
	return v.Text
}

func (v vector) reason() string {
	if v.ReasonGen != nil {
		return v.ReasonGen.String()
	}
	return v.Reason
}

func (v vector) message() string {
	if v.MessageGen != nil {
		return v.MessageGen.String()
	}
	return v.Message
}

func errClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrTooLong):
		return "too-long"
	case errors.Is(err, ErrMalformed):
		return "malformed"
	}
	return "other: " + err.Error()
}

// onlyOneByte fails the test if a reader is asked for more than one byte at a
// time: that is what keeps service bytes out of the handshake's buffers.
type onlyOneByte struct {
	t *testing.T
	r io.Reader
}

func (o onlyOneByte) Read(p []byte) (int, error) {
	if len(p) > 1 {
		o.t.Errorf("a read asked for %d bytes; the handshake must read one byte at a time", len(p))
		p = p[:1]
	}
	return o.r.Read(p)
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	b, err := os.ReadFile(vectorsFile)
	if err != nil {
		t.Fatal(err)
	}
	var vs []vector
	if err := json.Unmarshal(b, &vs); err != nil {
		t.Fatal(err)
	}
	if len(vs) < 92 {
		t.Fatalf("only %d vectors", len(vs))
	}
	return vs
}

func servedSet(v vector) map[string]bool {
	labels := v.Served
	if labels == nil {
		labels = ReservedServices
	}
	m := map[string]bool{}
	for _, l := range labels {
		m[l] = true
	}
	return m
}

// Every case of spinclass's vectors, driven through this package.
func TestVectors(t *testing.T) {
	ops := map[string]int{}
	for _, v := range loadVectors(t) {
		ops[v.Op]++
		t.Run(v.Name, func(t *testing.T) {
			switch v.Op {
			case "hello":
				runHello(t, v)
			case "reply":
				runReply(t, v)
			case "format-err":
				if got := string(FormatErr(v.Code, v.text())); got != v.wantReply() {
					t.Errorf("FormatErr(%q, ...) = %q, want %q", v.Code, got, v.wantReply())
				}
			case "socket-path":
				runSocketPath(t, v)
			case "down-message":
				if got := DownMessage(v.Reason); got != v.Message {
					t.Errorf("DownMessage(%q) = %q, want %q", v.Reason, got, v.Message)
				}
			case "unusable-message":
				if got := UnusableMessage(v.reason()); got != v.message() {
					t.Errorf("UnusableMessage(%d bytes) = %q, want %q", len(v.reason()), got, v.message())
				}
			case "constants":
				runConstants(t, v)
			default:
				t.Fatalf("unknown op %q: this build does not understand the vectors file", v.Op)
			}
		})
	}
	for _, op := range []string{"hello", "reply", "format-err", "socket-path", "down-message", "unusable-message", "constants"} {
		if ops[op] == 0 {
			t.Errorf("no vector for op %q", op)
		}
	}
}

func runSocketPath(t *testing.T, v vector) {
	dir := v.Dir
	if v.DirGen != nil {
		dir = v.DirGen.String()
	}
	got, err := SocketPath(dir)
	switch v.PathErr {
	case "":
		if err != nil {
			t.Fatalf("SocketPath(%q) = %v", dir, err)
		}
		want := v.Path
		if v.PathGen != nil {
			want = v.PathGen.String()
		}
		if got != want {
			t.Errorf("SocketPath(%q) = %q, want %q", dir, got, want)
		}
		if v.PathLen != 0 && len(got) != v.PathLen {
			t.Errorf("path is %d bytes, want %d", len(got), v.PathLen)
		}
	case "too-long":
		if err == nil || !strings.Contains(err.Error(), "over the unix socket limit") {
			t.Errorf("SocketPath(%d bytes) = %q, %v; want the too-long refusal", len(dir), got, err)
		}
	case "empty":
		if err == nil || !strings.Contains(err.Error(), "is empty") {
			t.Errorf("SocketPath(%q) = %q, %v; want the empty refusal", dir, got, err)
		}
	default:
		t.Fatalf("unknown path_err %q", v.PathErr)
	}
}

func runConstants(t *testing.T, v vector) {
	asStrings := func(key string) string {
		var parts []string
		list, _ := v.Constants[key].([]any)
		for _, e := range list {
			parts = append(parts, fmt.Sprint(e))
		}
		return strings.Join(parts, " ")
	}
	checks := map[string][2]any{
		"socket_name":             {v.Constants["socket_name"], SocketName},
		"env_dir":                 {v.Constants["env_dir"], EnvDir},
		"env_unit":                {v.Constants["env_unit"], EnvUnit},
		"env_cgroup_file":         {v.Constants["env_cgroup_file"], EnvCgroupFile},
		"max_hello_bytes":         {v.Constants["max_hello_bytes"], float64(MaxHelloBytes)},
		"max_reply_text_bytes":    {v.Constants["max_reply_text_bytes"], float64(MaxReplyTextBytes)},
		"max_streams_per_session": {v.Constants["max_streams_per_session"], float64(MaxStreamsPerSession)},
		"max_socket_path":         {v.Constants["max_socket_path"], float64(MaxSocketPath)},
		"hello_timeout_ms":        {v.Constants["hello_timeout_ms"], float64(HelloTimeout.Milliseconds())},
		"server_major":            {v.Constants["server_major"], float64(Major)},
		"version_hint":            {v.Constants["version_hint"], VersionHint},
		"reserved_services":       {asStrings("reserved_services"), strings.Join(ReservedServices, " ")},
		"codes":                   {asStrings("codes"), strings.Join([]string{CodeVersion, CodeUnknownService, CodeRefused, CodeBusy, CodeError}, " ")},
	}
	if len(v.Constants) != len(checks) {
		t.Errorf("the vector names %d constants, the test checks %d: they must match", len(v.Constants), len(checks))
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s: vector %v, package %v", name, c[0], c[1])
		}
	}
}

func runHello(t *testing.T, v vector) {
	in := v.input()
	rd := bytes.NewReader([]byte(in))
	h, err := ReadHello(onlyOneByte{t, rd})
	if got := errClass(err); got != v.ParseErr {
		t.Fatalf("ReadHello error class = %q (%v), want %q", got, err, v.ParseErr)
	}
	if v.ParseErr == "" {
		if h.Major != v.Major || h.Service != v.wantService() {
			t.Errorf("ReadHello = %+v, want major %d service %q", h, v.Major, v.wantService())
		}
		if rest := in[len(in)-rd.Len():]; rest != v.Rest {
			t.Errorf("ReadHello left %q unread, want %q", rest, v.Rest)
		}
	} else if len(in)-rd.Len() > MaxHelloBytes {
		t.Errorf("ReadHello consumed %d bytes, more than %d", len(in)-rd.Len(), MaxHelloBytes)
	}
	// The line parser sees the line the stream reader would have delivered.
	line := in
	if i := strings.IndexByte(in, '\n'); i >= 0 {
		line = in[:i+1]
	}
	ph, perr := ParseHello([]byte(line))
	if got := errClass(perr); got != v.ParseErr {
		t.Fatalf("ParseHello error class = %q (%v), want %q", got, perr, v.ParseErr)
	}
	if perr == nil && ph != h {
		t.Errorf("ParseHello = %+v, ReadHello = %+v", ph, h)
	}
	var reply []byte
	var accept bool
	if perr != nil {
		reply, accept = MalformedReply(), false
	} else {
		reply, accept = Decide(ph, servedSet(v))
	}
	if string(reply) != v.wantReply() || accept != v.Accept {
		t.Errorf("answer = %q accept %v, want %q accept %v", reply, accept, v.wantReply(), v.Accept)
	}
}

func runReply(t *testing.T, v vector) {
	in := v.input()
	rd := bytes.NewReader([]byte(in))
	ok, code, text, err := ReadReply(onlyOneByte{t, rd})
	if got := errClass(err); got != v.ParseErr {
		t.Fatalf("ReadReply error class = %q (%v), want %q", got, err, v.ParseErr)
	}
	if v.ParseErr != "" {
		if len(in)-rd.Len() > MaxHelloBytes+MaxReplyTextBytes {
			t.Errorf("ReadReply consumed %d bytes, more than %d", len(in)-rd.Len(), MaxHelloBytes+MaxReplyTextBytes)
		}
		return
	}
	if ok != v.OK || code != v.Code || text != v.text() {
		t.Errorf("ReadReply = %v %q %q, want %v %q %q", ok, code, text, v.OK, v.Code, v.text())
	}
	if rest := in[len(in)-rd.Len():]; rest != v.Rest {
		t.Errorf("ReadReply left %q unread, want %q", rest, v.Rest)
	}
}

func TestConstantsAreTheContracts(t *testing.T) {
	if SocketName != "bridge.sock" || EnvDir != "CLOWN_BRIDGE_DIR" || EnvUnit != "CLOWN_BRIDGE_UNIT" {
		t.Errorf("names: %q %q %q", SocketName, EnvDir, EnvUnit)
	}
	if EnvCgroupFile != "CLOWN_BRIDGE_CGROUP_FILE" || CgroupFileName != "bridge.cgroup" {
		t.Errorf("cgroup file names: %q %q", EnvCgroupFile, CgroupFileName)
	}
	if MaxHelloBytes != 64 || MaxReplyTextBytes != 512 || MaxStreamsPerSession != 64 || MaxSocketPath != 107 || Major != 1 {
		t.Error("limits drifted from the contract")
	}
	if HelloTimeout.Seconds() != 5 {
		t.Errorf("HelloTimeout = %v", HelloTimeout)
	}
	if got := strings.Join(ReservedServices, " "); got != "egress control session hook chat model keys status" {
		t.Errorf("ReservedServices = %q", got)
	}
}

func TestFormatHelloRoundTripsAndRefusesWhatParseRefuses(t *testing.T) {
	b, err := FormatHello(1, "egress")
	if err != nil || string(b) != "BRIDGE/1 egress\n" {
		t.Fatalf("FormatHello = %q, %v", b, err)
	}
	if h, err := ParseHello(b); err != nil || h != (Hello{Major: 1, Service: "egress"}) {
		t.Errorf("ParseHello = %+v, %v", h, err)
	}
	for _, c := range []struct {
		major   int
		service string
	}{{0, "egress"}, {1000, "egress"}, {-1, "egress"}, {1, "Egress"}, {1, ""}, {1, strings.Repeat("a", 33)}} {
		if b, err := FormatHello(c.major, c.service); !errors.Is(err, ErrMalformed) {
			t.Errorf("FormatHello(%d, %q) = %q, %v; want ErrMalformed", c.major, c.service, b, err)
		}
	}
}

// An error text never carries raw bytes from the peer.
func TestMalformedErrorEchoIsEscapedAndCapped(t *testing.T) {
	_, err := ParseHello([]byte("BRIDGE/1 \x00\x1b[31m\xff" + strings.Repeat("z", 30) + "\n"))
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v", err)
	}
	for _, r := range err.Error() {
		if r < 0x20 || r > 0x7e {
			t.Errorf("error text holds the raw rune %q: %q", r, err.Error())
		}
	}
	if !strings.Contains(err.Error(), `\x00`) {
		t.Errorf("the NUL byte is not escaped: %q", err.Error())
	}
}

func TestSocketPath(t *testing.T) {
	got, err := SocketPath("/run/user/1000/clown/launch/abc/bridge/")
	if err != nil || got != "/run/user/1000/clown/launch/abc/bridge/bridge.sock" {
		t.Errorf("SocketPath = %q, %v", got, err)
	}
	// A 36-character key under /run/user/1001: 83 bytes (the figure first agreed,
	// 79, left out 4 bytes of the launch path).
	if p, err := SocketPath("/run/user/1001/clown/launch/" + strings.Repeat("k", 36) + "/bridge"); err != nil || len(p) != 83 {
		t.Errorf("a real key's path: %q (%d), %v", p, len(p), err)
	}
	dir := "/" + strings.Repeat("d", MaxSocketPath-len("/"+"/"+SocketName))
	if p, err := SocketPath(dir); err != nil || len(p) != MaxSocketPath {
		t.Errorf("107-byte path: %q (%d), %v", p, len(p), err)
	}
	if _, err := SocketPath(dir + "d"); err == nil || !strings.Contains(err.Error(), "bridge stays off") {
		t.Errorf("a 108-byte path: %v", err)
	}
	if _, err := SocketPath(""); err == nil {
		t.Error("an empty directory was accepted")
	}
}

// Round 2, M4: a listener refuses an unadmitted peer with `ERR refused`, and a
// reader parses it back as that code; hostile text cannot break the line.
func TestRefusedUnadmittedIsAnErrRefusedLine(t *testing.T) {
	for _, reason := range []string{"no cgroup file yet", "caller is in \"/x\", not in \"/y\"", "evil\r\n\x00\x1b[31m" + strings.Repeat("z", 2000)} {
		b := RefusedUnadmitted(reason)
		if !strings.HasPrefix(string(b), "ERR refused ") || !strings.HasSuffix(string(b), "\n") || strings.Count(string(b), "\n") != 1 {
			t.Fatalf("RefusedUnadmitted(%.20q) = %q", reason, b)
		}
		ok, code, text, err := ReadReply(bytes.NewReader(b))
		if err != nil || ok || code != CodeRefused || !strings.HasPrefix(text, "the peer is not admitted: ") {
			t.Errorf("ReadReply = %v %q %q %v", ok, code, text, err)
		}
	}
}

func TestDownMessageIsTheFixedForm(t *testing.T) {
	want := "clown: the bridge is down (EOF on control); no tool or model call can leave this session; the session's frontend is gone or restarting"
	if got := DownMessage("EOF on control"); got != want {
		t.Errorf("down message\n got %q\nwant %q", got, want)
	}
}
