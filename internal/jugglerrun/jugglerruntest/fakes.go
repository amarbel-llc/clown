// Package jugglerruntest provides stateful fakes of the ringmaster, troupe,
// systemd-run and moxy (its narrowing CLI only, see fakeMoxyCLI) binaries for
// tests of internal/jugglerrun and cmd/juggler.
//
// Each fake is a /bin/sh wrapper that re-execs the running test binary with
// JUGGLERRUNTEST_FAKE=<tool>; a test package opts in by calling
// RunFakeIfRequested first thing in its TestMain. Every invocation's argv and
// identity environment is recorded to <dir>/calls.jsonl. The ringmaster fake
// keeps a per-target journal on disk, so start/progress/done/read/status/
// spool-path/wait behave like the real v3 protocol (ringmaster#27, clown#256):
// `done` against an already-terminal job exits 3 (not generic 1), and
// `version --protocol` prints fakeRingmasterDefaultProtocol ("3") unless
// SetRingmasterProtocolVersion overrides it, to simulate a mismatched build;
// the troupe fake records MUC posts and wakes and keeps room affiliations
// (`muc create`/`muc affiliate`, with troupe's owner and last-owner rules);
// the systemd-run fake records and succeeds; the systemctl fake answers
// `is-system-running` with "running" unless SetSystemctlState says
// otherwise. A file <dir>/fail/<tool>-<verb> makes that verb exit 1 (for
// systemctl, verb is always "is-system-running", so Fail(t, "systemctl",
// "is-system-running") simulates a check that cannot read any state at
// all); <dir>/fail/troupe-muc-<sub> fails one muc subverb, and
// <dir>/fail/troupe-mint-revoke-login makes mint-revoke fail the way a
// SASL login refusal does (exit 1, Prosody's refusal text after "xmpp:
// negotiate:", the file kept), and <dir>/fail/troupe-mint-revoke-tls the way
// a TLS failure during negotiation does.
package jugglerruntest

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	toolEnv = "JUGGLERRUNTEST_FAKE"
	dirEnv  = "JUGGLERRUNTEST_DIR"
)

// RunFakeIfRequested runs the fake named by the environment and exits; it
// returns immediately in a normal test process.
func RunFakeIfRequested() {
	tool := os.Getenv(toolEnv)
	if tool == "" {
		return
	}
	os.Exit(runFake(tool, os.Getenv(dirEnv), os.Args[1:]))
}

// Fakes is one installed set of fake binaries sharing a state directory.
type Fakes struct {
	Dir        string
	Ringmaster string
	Troupe     string
	SystemdRun string
	Moxy       string
	Systemctl  string
}

// Install writes the wrappers into a fresh temp dir.
func Install(t testing.TB) *Fakes {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := &Fakes{Dir: dir}
	for tool, dst := range map[string]*string{"ringmaster": &f.Ringmaster, "troupe": &f.Troupe, "systemd-run": &f.SystemdRun, "moxy": &f.Moxy, "systemctl": &f.Systemctl} {
		path := filepath.Join(dir, "bin", tool)
		script := fmt.Sprintf("#!/bin/sh\n%s=%s %s=%s exec %s \"$@\"\n", toolEnv, shQuote(tool), dirEnv, shQuote(dir), shQuote(self))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		*dst = path
	}
	return f
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Fail makes `<tool> <verb>` exit 1 (verb "launch" for systemd-run).
func (f *Fakes) Fail(t testing.TB, tool, verb string) {
	t.Helper()
	path := filepath.Join(f.Dir, "fail", tool+"-"+verb)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Call is one recorded invocation.
type Call struct {
	Tool string            `json:"tool"`
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env"`
}

// Calls returns every recorded invocation of tool ("" for all), in order.
func (f *Fakes) Calls(t testing.TB, tool string) []Call {
	t.Helper()
	var out []Call
	for _, c := range readLines[Call](t, filepath.Join(f.Dir, "calls.jsonl")) {
		if tool == "" || c.Tool == tool {
			out = append(out, c)
		}
	}
	return out
}

// Record is one fake journal record.
type Record struct {
	V         int    `json:"v"`
	Job       string `json:"job"`
	Session   string `json:"session"`
	Source    string `json:"source"`
	From      string `json:"from,omitempty"`
	Type      string `json:"type"`
	Seq       int    `json:"seq"`
	TS        string `json:"ts"`
	Message   string `json:"message,omitempty"`
	ResultRef string `json:"result_ref,omitempty"`
}

// Records returns job's journal on target's channel.
func (f *Fakes) Records(t testing.TB, target, job string) []Record {
	t.Helper()
	return readLines[Record](t, journalPath(f.Dir, target, job))
}

// Jobs lists the job ids on target's channel.
func (f *Fakes) Jobs(t testing.TB, target string) []string {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(f.Dir, "rm", target, "*.jsonl"))
	var out []string
	for _, p := range paths {
		out = append(out, strings.TrimSuffix(filepath.Base(p), ".jsonl"))
	}
	return out
}

// AppendRecord writes a record straight to a job's journal, standing in for
// a producer the test does not run (e.g. the agent inside a unit).
func (f *Fakes) AppendRecord(t testing.TB, target, job, typ, message, resultRef string) {
	t.Helper()
	if err := appendRecord(f.Dir, target, job, Record{Source: "test", Type: typ, Message: message, ResultRef: resultRef}); err != nil {
		t.Fatal(err)
	}
}

// SpoolPath is the fake spool path of job on target's channel.
func (f *Fakes) SpoolPath(target, job string) string {
	return filepath.Join(f.Dir, "rm", target, job+".out")
}

// MUCPost is one recorded `troupe muc send`.
type MUCPost struct {
	ID      string `json:"id"`
	Room    string `json:"room"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Source  string `json:"source"`
	From    string `json:"from"`
	User    string `json:"user"`
	Session string `json:"session"`
}

// MUC returns every post, in order.
func (f *Fakes) MUC(t testing.TB) []MUCPost {
	t.Helper()
	return readLines[MUCPost](t, filepath.Join(f.Dir, "muc.jsonl"))
}

// WakeMsg is one recorded `troupe message`.
type WakeMsg struct {
	Target    string `json:"target"`
	From      string `json:"from"`
	Source    string `json:"source"`
	Message   string `json:"message"`
	ResultRef string `json:"result_ref"`
}

// Wakes returns every standalone message sent, in order.
func (f *Fakes) Wakes(t testing.TB) []WakeMsg {
	t.Helper()
	return readLines[WakeMsg](t, filepath.Join(f.Dir, "wakes.jsonl"))
}

// Cancelled lists the job ids passed to `ringmaster cancel`.
func (f *Fakes) Cancelled(t testing.TB) []string {
	t.Helper()
	var out []string
	for _, c := range f.Calls(t, "ringmaster") {
		if len(c.Argv) > 1 && c.Argv[0] == "cancel" {
			out = append(out, c.Argv[1])
		}
	}
	return out
}

// Revoked lists the session keys passed to `troupe mint-revoke`.
func (f *Fakes) Revoked(t testing.TB) []string {
	t.Helper()
	var out []string
	for _, c := range f.Calls(t, "troupe") {
		if len(c.Argv) > 0 && c.Argv[0] == "mint-revoke" {
			out = append(out, flagValue(c.Argv, "--session-key"))
		}
	}
	return out
}

func flagValue(argv []string, name string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == name {
			return argv[i+1]
		}
	}
	return ""
}

func readLines[T any](t testing.TB, path string) []T {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []T
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		out = append(out, v)
	}
	return out
}

func journalPath(dir, target, job string) string {
	return filepath.Join(dir, "rm", target, job+".jsonl")
}

func appendJSONLine(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer fh.Close()
	_, err = fh.Write(append(b, '\n'))
	return err
}

func readRecords(dir, target, job string) ([]Record, error) {
	data, err := os.ReadFile(journalPath(dir, target, job))
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func appendRecord(dir, target, job string, r Record) error {
	recs, _ := readRecords(dir, target, job)
	r.V, r.Job, r.Session, r.Seq = 2, job, target, len(recs)+1
	r.TS = time.Now().UTC().Format(time.RFC3339Nano)
	return appendJSONLine(journalPath(dir, target, job), r)
}

func isTerminal(typ string) bool {
	switch typ {
	case "succeeded", "failed", "aborted", "interrupted", "cancelled":
		return true
	}
	return false
}

func randomHex() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// parseFlags splits "--name value" pairs; names in boolean take no value.
func parseFlags(args []string, boolean ...string) map[string]string {
	isBool := map[string]bool{}
	for _, b := range boolean {
		isBool[b] = true
	}
	out := map[string]string{}
	for i := 0; i < len(args); i++ {
		name, ok := strings.CutPrefix(args[i], "--")
		if !ok {
			continue
		}
		if isBool[name] {
			out[name] = "true"
			continue
		}
		if i+1 < len(args) {
			out[name] = args[i+1]
			i++
		}
	}
	return out
}

func runFake(tool, dir string, args []string) int {
	env := map[string]string{}
	keys := []string{"CLOWN_SESSION_ID", "TROUPE_XMPP_USER", "TROUPE_XMPP_PASSWORD_FILE", "TROUPE_XMPP_DOMAIN"}
	if tool == "moxy" {
		keys = append(keys, "MOXIN_PATH", "MOXY_PARENT_MOXYFILE")
	}
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	_ = appendJSONLine(filepath.Join(dir, "calls.jsonl"), Call{Tool: tool, Argv: args, Env: env})
	verb := "launch"
	switch {
	case tool == "systemd-run":
		verb = "launch"
	case tool == "systemctl":
		// The fake implements only is-system-running, whatever flags
		// (--user) precede it.
		verb = "is-system-running"
	case len(args) > 0:
		verb = args[0]
	}
	if _, err := os.Stat(filepath.Join(dir, "fail", tool+"-"+verb)); err == nil {
		fmt.Fprintf(os.Stderr, "%s %s: injected failure\n", tool, verb)
		return 1
	}
	switch tool {
	case "ringmaster":
		return fakeRingmaster(dir, args)
	case "troupe":
		return fakeTroupe(dir, args)
	case "systemd-run":
		return 0
	case "moxy":
		return fakeMoxyCLI(dir, args)
	case "systemctl":
		return fakeSystemctl(dir)
	}
	fmt.Fprintf(os.Stderr, "unknown fake %q\n", tool)
	return 2
}

func fakeRingmaster(dir string, args []string) int {
	if len(args) == 0 {
		return 2
	}
	verb := args[0]
	switch verb {
	case "start":
		fl := parseFlags(args[1:])
		label := fl["label"]
		if label == "" {
			label = "job"
		}
		job := label + "-" + randomHex()
		if err := appendRecord(dir, fl["target"], job, Record{Source: fl["source"], Type: "started"}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(job)
		return 0
	case "read":
		fl := parseFlags(args[1:], "json")
		recs, err := readRecords(dir, os.Getenv("CLOWN_SESSION_ID"), fl["job"])
		if os.IsNotExist(err) {
			return 0
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		for _, r := range recs {
			b, _ := json.Marshal(r)
			fmt.Println(string(b))
		}
		return 0
	case "version":
		fl := parseFlags(args[1:], "protocol")
		protocol := ringmasterProtocolVersion(dir)
		if fl["protocol"] == "true" {
			fmt.Println(protocol)
			return 0
		}
		fmt.Printf("ringmaster fake\nprotocol v%s\n", protocol)
		return 0
	}
	if len(args) < 2 {
		return 2
	}
	job := args[1]
	fl := parseFlags(args[2:], "json", "on-cancel")
	target := fl["target"]
	switch verb {
	case "progress":
		return exitFor(appendRecord(dir, target, job, Record{Source: "fake", Type: "progress", Message: fl["message"]}))
	case "cancel":
		return exitFor(appendRecord(dir, target, job, Record{Source: "fake", Type: "cancel-requested", Message: fl["message"]}))
	case "done":
		recs, err := readRecords(dir, target, job)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		for _, r := range recs {
			if isTerminal(r.Type) {
				// ringmaster v3 (clown#256): a write refused because the job
				// is already terminal (the reaper or another writer won the
				// race) exits 3, distinct from generic failure, so a caller
				// can tell "already settled" from "the write failed" without
				// parsing stderr.
				fmt.Fprintln(os.Stderr, "ringmaster done: already terminal")
				return 3
			}
		}
		if !isTerminal(fl["state"]) || fl["state"] == "cancelled" {
			fmt.Fprintf(os.Stderr, "ringmaster done: bad state %q\n", fl["state"])
			return 2
		}
		// Validate --state (above) before consuming the race knob (clown#256
		// review finding 10): an invalid-state call must not eat it.
		if raced, err := consumeDoneRaceOnceKnob(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		} else if raced {
			// Simulate the v3 liveness reaper winning the race between this
			// call's read above and its append below: it writes its own
			// interrupted terminal first (clown#256).
			if err := appendRecord(dir, target, job, Record{Source: "fake-reaper", Type: "interrupted", Message: "producer died without writing a terminal record"}); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			fmt.Fprintln(os.Stderr, "ringmaster done: already terminal")
			return 3
		}
		return exitFor(appendRecord(dir, target, job, Record{Source: "fake", Type: fl["state"], Message: fl["message"], ResultRef: fl["result-ref"]}))
	case "status":
		return printStatus(dir, target, job)
	case "spool-path":
		path := filepath.Join(dir, "rm", target, job+".out")
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		fmt.Println(path)
		return 0
	case "wait":
		var deadline time.Time
		if d, err := time.ParseDuration(fl["timeout"]); err == nil && d > 0 {
			deadline = time.Now().Add(d)
		}
		for {
			recs, err := readRecords(dir, target, job)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ringmaster wait: %v\n", err)
				return 1
			}
			for _, r := range recs {
				if isTerminal(r.Type) || (fl["on-cancel"] == "true" && r.Type == "cancel-requested") {
					return printStatus(dir, target, job)
				}
			}
			if !deadline.IsZero() && time.Now().After(deadline) {
				fmt.Fprintln(os.Stderr, "ringmaster wait: timed out; job still running")
				return 1
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	fmt.Fprintf(os.Stderr, "fake ringmaster: unknown verb %q\n", verb)
	return 2
}

func printStatus(dir, target, job string) int {
	recs, err := readRecords(dir, target, job)
	if err != nil || len(recs) == 0 {
		fmt.Fprintf(os.Stderr, "ringmaster status: no journal for %s\n", job)
		return 1
	}
	st := map[string]string{"state": "running", "source": recs[0].Source, "started": recs[0].TS}
	for _, r := range recs {
		if r.Type == "progress" {
			st["progress"] = r.Message
		}
		if isTerminal(r.Type) {
			st["state"], st["ended"] = r.Type, r.TS
		}
	}
	b, _ := json.Marshal(st)
	fmt.Println(string(b))
	return 0
}

func exitFor(err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func fakeTroupe(dir string, args []string) int {
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "mint":
		fl := parseFlags(args[1:])
		key, pw := fl["session-key"], fl["password-file"]
		if pw == "" {
			pw = filepath.Join(dir, "creds", key)
		}
		_ = os.MkdirAll(filepath.Dir(pw), 0o700)
		if err := os.WriteFile(pw, []byte("secret\n"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		b, _ := json.Marshal(map[string]string{"jid": key + "@xmpp.test", "password_file": pw})
		fmt.Println(string(b))
		return 0
	case "mint-revoke":
		fl := parseFlags(args[1:])
		pw := fl["password-file"]
		if pw == "" {
			pw = filepath.Join(dir, "creds", fl["session-key"])
		}
		if _, err := os.Stat(pw); err != nil {
			return 0 // file absent: troupe does not contact the server
		}
		if failKnob(dir, "troupe-mint-revoke-login") {
			// troupe 4c52b3b against Prosody: the SASL login was refused; "no
			// such account" and "wrong password" look the same, and the file
			// is kept.
			fmt.Fprintln(os.Stderr, "troupe mint-revoke: mint: xmpp: negotiate: Unable to authorize you with the authentication credentials you've sent.")
			return 1
		}
		if failKnob(dir, "troupe-mint-revoke-tls") {
			// A negotiation failure that is not a login refusal.
			fmt.Fprintln(os.Stderr, "troupe mint-revoke: xmpp: negotiate: tls: failed to verify certificate")
			return 1
		}
		_ = os.Remove(pw)
		return 0
	case "muc":
		if len(args) < 2 {
			return 2
		}
		if failKnob(dir, "troupe-muc-"+args[1]) {
			fmt.Fprintf(os.Stderr, "troupe muc %s: injected failure\n", args[1])
			return 1
		}
		switch args[1] {
		case "create":
			return fakeMUCCreate(dir, args[2:])
		case "affiliate":
			return fakeMUCAffiliate(dir, args[2:])
		case "send":
		default:
			return 2
		}
		fl := parseFlags(args[2:])
		id := "chat-" + randomHex()
		post := MUCPost{ID: id, Room: fl["room"], Subject: fl["subject"], Body: fl["body"], Source: fl["source"], From: fl["from"], User: os.Getenv("TROUPE_XMPP_USER"), Session: os.Getenv("CLOWN_SESSION_ID")}
		if err := appendJSONLine(filepath.Join(dir, "muc.jsonl"), post); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(id)
		return 0
	case "message":
		fl := parseFlags(args[1:])
		w := WakeMsg{Target: fl["target"], From: fl["from"], Source: fl["source"], Message: fl["message"], ResultRef: fl["result-ref"]}
		if err := appendJSONLine(filepath.Join(dir, "wakes.jsonl"), w); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("message-" + randomHex())
		return 0
	}
	fmt.Fprintf(os.Stderr, "fake troupe: unknown verb %q\n", args[0])
	return 2
}

// FakeEffectiveMoxyfile is what the moxy fake's show-effective-moxyfile
// prints, whatever the directory: a fixed, deterministic canonical doc.
const FakeEffectiveMoxyfile = "# moxy show-effective-moxyfile (fake)\n[[servers]]\nname = \"ring\"\n\n[resolved-moxins]\nring = \"/nix/store/fake-ring/share/moxy/moxins/ring\"\n"

// FakeNarrowsViolations are the two violations the moxy fake's narrows
// reports under the fail/moxy-narrows-widen knob.
var FakeNarrowsViolations = []string{
	"allow-tools: the parent sets allow-tools; the child must set it too",
	"servers.shell: not in the parent",
}

// fakeMoxyCLI is moxy's narrowing CLI (moxy 0c17cea): show-effective-moxyfile
// prints FakeEffectiveMoxyfile; narrows exits 0 unless a knob says
// otherwise — fail/moxy-narrows-widen exits 1 with FakeNarrowsViolations
// (lines, or the --json form; with fail/moxy-narrows-nojson too, the --json
// form is unparseable), fail/moxy-narrows-broken exits 2 with a
// reason on stderr. Both verbs check their path arguments exist.
func fakeMoxyCLI(dir string, args []string) int {
	if len(args) == 0 {
		return 2
	}
	fl := parseFlags(args[1:], "json")
	switch args[0] {
	case "show-effective-moxyfile":
		if fl["dir"] == "" {
			fmt.Fprintln(os.Stderr, "moxy show-effective-moxyfile: --dir is required")
			return 2
		}
		fmt.Print(FakeEffectiveMoxyfile)
		return 0
	case "narrows":
		for _, p := range []string{fl["parent"], fl["child"]} {
			if _, err := os.Stat(p); err != nil {
				fmt.Fprintf(os.Stderr, "moxy narrows: %v\n", err)
				return 2
			}
		}
		switch {
		case failKnob(dir, "moxy-narrows-broken"):
			fmt.Fprintln(os.Stderr, "moxy narrows: child: parsing moxyfile: toml: line 3: expected '='")
			return 2
		case failKnob(dir, "moxy-narrows-widen"):
			if fl["json"] == "true" && failKnob(dir, "moxy-narrows-nojson") {
				fmt.Println("not json")
				return 1
			}
			if fl["json"] == "true" {
				var vs []map[string]string
				for _, l := range FakeNarrowsViolations {
					k, m, _ := strings.Cut(l, ": ")
					vs = append(vs, map[string]string{"key": k, "subject": k, "message": m})
				}
				b, _ := json.Marshal(map[string]any{"narrows": false, "violations": vs})
				fmt.Println(string(b))
			} else {
				fmt.Println(strings.Join(FakeNarrowsViolations, "\n"))
			}
			return 1
		}
		if fl["json"] == "true" {
			fmt.Println(`{"narrows":true,"violations":[]}`)
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "fake moxy: unknown verb %q\n", args[0])
	return 2
}

// fakeRingmasterDefaultProtocol is the fake's default `version --protocol`
// answer, matching the real jobwake.ProtocolVersion this fake otherwise
// emulates (ringmaster v3, clown#256).
const fakeRingmasterDefaultProtocol = "3"

// SetRingmasterProtocolVersion makes the fake ringmaster's `version
// --protocol` report version instead of fakeRingmasterDefaultProtocol, to
// simulate a juggler built against a different protocol than the ringmaster
// binary it execs (clown#256).
func (f *Fakes) SetRingmasterProtocolVersion(t testing.TB, version string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.Dir, "ringmaster-protocol-version"), []byte(version), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ringmasterProtocolVersion reads the knob SetRingmasterProtocolVersion sets,
// or fakeRingmasterDefaultProtocol absent one.
func ringmasterProtocolVersion(dir string) string {
	if b, err := os.ReadFile(filepath.Join(dir, "ringmaster-protocol-version")); err == nil {
		return strings.TrimSpace(string(b))
	}
	return fakeRingmasterDefaultProtocol
}

// RaceDoneReaperOnce makes the fake ringmaster's next `done` call against a
// job with no terminal yet behave as a real producer-vs-reaper race would
// (clown#256, ringmaster v3's liveness reaper, RFC-0018 §2.3): between that
// call's read of the journal and its own append, the reaper's write lands
// first — an `interrupted` terminal with its own message — and the done call
// itself is refused exit 3 (already terminal), exactly as `ExecRingmaster.Done`
// and the exit-wake hook's fallback are written to handle. One-shot: consumed
// by the first `done` call it applies to.
func (f *Fakes) RaceDoneReaperOnce(t testing.TB) {
	t.Helper()
	path := doneRaceOnceKnobPath(f.Dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func doneRaceOnceKnobPath(dir string) string {
	return filepath.Join(dir, "fail", "ringmaster-done-race-once")
}

// consumeDoneRaceOnceKnob reports whether RaceDoneReaperOnce's knob is set,
// removing it so the race applies to only the next matching `done` call.
func consumeDoneRaceOnceKnob(dir string) (bool, error) {
	path := doneRaceOnceKnobPath(dir)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, os.Remove(path)
}

// SetSystemctlState makes the systemctl fake's `is-system-running` report
// state (default "running"): exit 0 for "running", else systemctl's own
// exit 1 with state on stdout, e.g. SetSystemctlState(t, "degraded") or
// SetSystemctlState(t, "maintenance"). Use Fail(t, "systemctl",
// "is-system-running") instead for a check that cannot read any state at
// all (systemctl itself unreachable: exit 1, nothing on stdout).
func (f *Fakes) SetSystemctlState(t testing.TB, state string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.Dir, "systemctl-state"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeSystemctl is `systemctl is-system-running`: prints the knob state set
// by SetSystemctlState (default "running") and exits 0 only for "running",
// mirroring systemctl's own exit code (systemctl(1)).
func fakeSystemctl(dir string) int {
	state := "running"
	if b, err := os.ReadFile(filepath.Join(dir, "systemctl-state")); err == nil {
		state = strings.TrimSpace(string(b))
	}
	fmt.Println(state)
	if state == "running" {
		return 0
	}
	return 1
}

func failKnob(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, "fail", name))
	return err == nil
}

// FakeRoom is one room the troupe fake's `muc create` made: every affiliated
// bare JID and its affiliation (owner, admin, member, outcast).
type FakeRoom struct {
	Room         string            `json:"room"`
	Affiliations map[string]string `json:"affiliations"`
}

// Room returns the fake's state of room, or ok=false if it was never created.
func (f *Fakes) Room(t testing.TB, room string) (FakeRoom, bool) {
	t.Helper()
	r, ok, err := loadRoom(f.Dir, room)
	if err != nil {
		t.Fatal(err)
	}
	return r, ok
}

// With lists the JIDs holding affiliation, sorted.
func (r FakeRoom) With(affiliation string) []string {
	out := []string{}
	for jid, a := range r.Affiliations {
		if a == affiliation {
			out = append(out, jid)
		}
	}
	sort.Strings(out)
	return out
}

func roomPath(dir, room string) string { return filepath.Join(dir, "rooms", room+".json") }

func loadRoom(dir, room string) (FakeRoom, bool, error) {
	data, err := os.ReadFile(roomPath(dir, room))
	if os.IsNotExist(err) {
		return FakeRoom{}, false, nil
	}
	if err != nil {
		return FakeRoom{}, false, err
	}
	var r FakeRoom
	if err := json.Unmarshal(data, &r); err != nil {
		return FakeRoom{}, false, err
	}
	if r.Affiliations == nil {
		r.Affiliations = map[string]string{}
	}
	return r, true, nil
}

func saveRoom(dir string, r FakeRoom) error {
	if err := os.MkdirAll(filepath.Join(dir, "rooms"), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return os.WriteFile(roomPath(dir, r.Room), b, 0o644)
}

// callerJID is the fake's view of who a troupe call acts as.
func callerJID() string {
	user, domain := os.Getenv("TROUPE_XMPP_USER"), os.Getenv("TROUPE_XMPP_DOMAIN")
	if user == "" || domain == "" {
		return ""
	}
	return user + "@" + domain
}

// flagValues collects every value of a repeated --name flag.
func flagValues(args []string, name string) []string {
	var out []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--"+name {
			out = append(out, args[i+1])
			i++
		}
	}
	return out
}

// fakeMUCCreate is troupe 4c52b3b's `muc create`: the caller becomes owner of
// a new persistent room, --owner/--member JIDs are affiliated; idempotent on
// a room the caller owns, exit 1 on one it does not.
func fakeMUCCreate(dir string, args []string) int {
	room, caller := parseFlags(args)["room"], callerJID()
	if room == "" {
		fmt.Fprintln(os.Stderr, "troupe muc create: --room is required")
		return 2
	}
	if caller == "" {
		fmt.Fprintln(os.Stderr, "troupe muc create: no XMPP identity (TROUPE_XMPP_USER/DOMAIN)")
		return 1
	}
	r, exists, err := loadRoom(dir, room)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if exists && r.Affiliations[caller] != "owner" {
		fmt.Fprintf(os.Stderr, "troupe muc create: %s exists and %s does not own it: forbidden\n", room, caller)
		return 1
	}
	if !exists {
		r = FakeRoom{Room: room, Affiliations: map[string]string{caller: "owner"}}
	}
	for _, j := range flagValues(args, "owner") {
		r.Affiliations[j] = "owner"
	}
	for _, j := range flagValues(args, "member") {
		if r.Affiliations[j] != "owner" {
			r.Affiliations[j] = "member"
		}
	}
	if err := saveRoom(dir, r); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	b, _ := json.Marshal(map[string]any{"room": room, "created": !exists, "owners": r.With("owner"), "members": r.With("member")})
	fmt.Println(string(b))
	return 0
}

// fakeMUCAffiliate is troupe 4c52b3b's `muc affiliate`: an owner sets each
// --jid's affiliation (none removes it); the last owner cannot step down.
func fakeMUCAffiliate(dir string, args []string) int {
	fl := parseFlags(args)
	room, aff, caller := fl["room"], fl["affiliation"], callerJID()
	jids := flagValues(args, "jid")
	switch aff {
	case "owner", "admin", "member", "none", "outcast":
	default:
		fmt.Fprintf(os.Stderr, "troupe muc affiliate: bad --affiliation %q\n", aff)
		return 2
	}
	if room == "" || len(jids) == 0 {
		fmt.Fprintln(os.Stderr, "troupe muc affiliate: --room and --jid are required")
		return 2
	}
	r, exists, err := loadRoom(dir, room)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !exists {
		fmt.Fprintf(os.Stderr, "troupe muc affiliate: %s: item-not-found\n", room)
		return 1
	}
	if r.Affiliations[caller] != "owner" {
		fmt.Fprintf(os.Stderr, "troupe muc affiliate: %s does not own %s: forbidden\n", caller, room)
		return 1
	}
	for _, j := range jids {
		if r.Affiliations[j] == "owner" && aff != "owner" && len(r.With("owner")) == 1 {
			fmt.Fprintf(os.Stderr, "troupe muc affiliate: %s is the last owner of %s: conflict\n", j, room)
			return 1
		}
		if aff == "none" {
			delete(r.Affiliations, j)
		} else {
			r.Affiliations[j] = aff
		}
	}
	return exitFor(saveRoom(dir, r))
}
