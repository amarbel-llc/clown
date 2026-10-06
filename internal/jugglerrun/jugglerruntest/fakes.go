// Package jugglerruntest provides stateful fakes of the ringmaster, troupe
// and systemd-run binaries for tests of internal/jugglerrun and cmd/juggler.
//
// Each fake is a /bin/sh wrapper that re-execs the running test binary with
// JUGGLERRUNTEST_FAKE=<tool>; a test package opts in by calling
// RunFakeIfRequested first thing in its TestMain. Every invocation's argv and
// identity environment is recorded to <dir>/calls.jsonl. The ringmaster fake
// keeps a per-target journal on disk, so start/progress/done/read/status/
// spool-path/wait behave like the real verbs; the troupe fake records MUC
// posts and wakes; the systemd-run fake records and succeeds. A file
// <dir>/fail/<tool>-<verb> makes that verb exit 1.
package jugglerruntest

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
}

// Install writes the three wrappers into a fresh temp dir.
func Install(t testing.TB) *Fakes {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := &Fakes{Dir: dir}
	for tool, dst := range map[string]*string{"ringmaster": &f.Ringmaster, "troupe": &f.Troupe, "systemd-run": &f.SystemdRun} {
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
	for _, k := range []string{"CLOWN_SESSION_ID", "TROUPE_XMPP_USER", "TROUPE_XMPP_PASSWORD_FILE", "TROUPE_XMPP_DOMAIN"} {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	_ = appendJSONLine(filepath.Join(dir, "calls.jsonl"), Call{Tool: tool, Argv: args, Env: env})
	verb := "launch"
	if tool != "systemd-run" && len(args) > 0 {
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
				fmt.Fprintf(os.Stderr, "ringmaster done: job %s already has a terminal record\n", job)
				return 1
			}
		}
		if !isTerminal(fl["state"]) || fl["state"] == "cancelled" {
			fmt.Fprintf(os.Stderr, "ringmaster done: bad state %q\n", fl["state"])
			return 2
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
		_ = os.Remove(pw)
		return 0
	case "muc":
		if len(args) < 2 || args[1] != "send" {
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
