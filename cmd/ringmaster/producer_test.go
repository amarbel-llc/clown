package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/amarbel-llc/clown/internal/jobwake"
)

// producerEnv isolates the journal + a short runtime dir per test. The runtime
// dir must be short (AF_UNIX sun_path is ~108 bytes) so live monitor nudges
// bind; the deep worktree .tmp would overflow it, so we use /tmp directly.
func producerEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, err := os.MkdirTemp("/tmp", "clown-jobtest-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rt) })
	t.Setenv("XDG_RUNTIME_DIR", rt)
	t.Setenv("CLOWN_SESSION_ID", "repo/branch")
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
}

func trimTrailingNewline(s string) string {
	return strings.TrimRight(s, "\n")
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

func TestNotificationLine(t *testing.T) {
	tests := []struct {
		name string
		rec  jobwake.Record
		want string
	}{
		{
			name: "message and result_ref",
			rec:  jobwake.Record{Source: "moxy", Job: "build-3f2a", Type: jobwake.TypeSucceeded, Message: "nix build ok", ResultRef: "moxy job-read --job build-3f2a"},
			want: "[clown-job] moxy build-3f2a succeeded: nix build ok · moxy job-read --job build-3f2a",
		},
		{
			name: "message no result_ref",
			rec:  jobwake.Record{Source: "spinclass", Job: "merge-1", Type: jobwake.TypeFailed, Message: "conflict"},
			want: "[clown-job] spinclass merge-1 failed: conflict",
		},
		{
			name: "no message omits colon",
			rec:  jobwake.Record{Source: "moxy", Job: "j1", Type: jobwake.TypeCancelled},
			want: "[clown-job] moxy j1 cancelled",
		},
		{
			name: "no message but result_ref",
			rec:  jobwake.Record{Source: "moxy", Job: "j2", Type: jobwake.TypeInterrupted, ResultRef: "ref"},
			want: "[clown-job] moxy j2 interrupted · ref",
		},
		{
			name: "embedded newline in message is stripped to space",
			rec:  jobwake.Record{Source: "s", Job: "j3", Type: jobwake.TypeSucceeded, Message: "line1\nline2"},
			want: "[clown-job] s j3 succeeded: line1 line2",
		},
		{
			name: "from inserted before the colon",
			rec:  jobwake.Record{Source: "spinclass", Job: "msg-1a2b", Type: jobwake.TypeMessage, From: "clown/other", Message: "ping"},
			want: "[clown-job] spinclass msg-1a2b message from clown/other: ping",
		},
		{
			name: "from without message omits colon",
			rec:  jobwake.Record{Source: "spinclass", Job: "msg-2c3d", Type: jobwake.TypeMessage, From: "clown/other"},
			want: "[clown-job] spinclass msg-2c3d message from clown/other",
		},
		{
			name: "from with message and result_ref",
			rec:  jobwake.Record{Source: "spinclass", Job: "msg-4e5f", Type: jobwake.TypeMessage, From: "clown/other", Message: "ping", ResultRef: "ref"},
			want: "[clown-job] spinclass msg-4e5f message from clown/other: ping · ref",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := notificationLine(tc.rec); got != tc.want {
				t.Fatalf("notificationLine() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The notification line carries a one-line resource-count hint (clown#112);
// the URIs come from the pull side.
func TestNotificationLineResources(t *testing.T) {
	r := jobwake.Record{
		Source: "s", Job: "j", Type: jobwake.TypeMessage, Message: "hi",
		Resources: []jobwake.Resource{{URI: "madder://blobs/x"}, {URI: "madder://blobs/y"}},
	}
	if line := notificationLine(r); !strings.Contains(line, "2 resource(s)") {
		t.Fatalf("notification line must hint the resource count, got %q", line)
	}
}

// whoami --json reports the resolved {sessionKey, channelId, source} for the
// current session (clown#135 / RFC-0012 §1).
func TestRingmasterWhoamiJSON(t *testing.T) {
	producerEnv(t) // CLOWN_SESSION_ID = "repo/branch"
	got := captureStdout(t, func() { ringmasterWhoami([]string{"--json"}) })
	var id struct {
		SessionKey string `json:"sessionKey"`
		ChannelID  string `json:"channelId"`
		Source     string `json:"source"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(got)), &id); err != nil {
		t.Fatalf("whoami --json not valid JSON: %v\n%s", err, got)
	}
	if id.SessionKey != "repo/branch" {
		t.Errorf("sessionKey = %q, want repo/branch", id.SessionKey)
	}
	if id.Source != "CLOWN_SESSION_ID" {
		t.Errorf("source = %q, want CLOWN_SESSION_ID", id.Source)
	}
	if id.ChannelID != jobwake.ChannelID("repo/branch") {
		t.Errorf("channelId = %q, want %q", id.ChannelID, jobwake.ChannelID("repo/branch"))
	}
}

// whoami reports the per-instance routing key AND the group decoration + group
// channel derived from the group-id (CLOWN_GROUP_ID, RFC-0014 §2).
func TestRingmasterWhoamiReportsKeyAndGroup(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "instance-key")
	t.Setenv("CLOWN_GROUP_ID", "repo/branch")
	got := captureStdout(t, func() { ringmasterWhoami([]string{"--json"}) })
	var id struct {
		SessionKey     string `json:"sessionKey"`
		ChannelID      string `json:"channelId"`
		Source         string `json:"source"`
		Decoration     string `json:"decoration"`
		GroupChannelID string `json:"groupChannelId"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(got)), &id); err != nil {
		t.Fatalf("whoami --json: %v\n%s", err, got)
	}
	if id.SessionKey != "instance-key" || id.Source != "CLOWN_SESSION_ID" {
		t.Fatalf("got key (%q, %q), want (instance-key, CLOWN_SESSION_ID)", id.SessionKey, id.Source)
	}
	if id.Decoration != "repo/branch" {
		t.Errorf("decoration = %q, want repo/branch", id.Decoration)
	}
	if id.GroupChannelID != jobwake.ChannelID("repo/branch") {
		t.Errorf("groupChannelId = %q, want %q", id.GroupChannelID, jobwake.ChannelID("repo/branch"))
	}
}

func TestRingmasterStartPrintsIDAndWritesRecord(t *testing.T) {
	producerEnv(t)
	out := captureStdout(t, func() { ringmasterStart([]string{"--source", "moxy", "--label", "build"}) })
	id := trimTrailingNewline(out)
	if id == "" {
		t.Fatal("start printed no id")
	}
	recs, err := jobwake.ReadJob(jobwake.ChannelID("repo/branch"), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Type != jobwake.TypeStarted || recs[0].Source != "moxy" || recs[0].Seq != 0 {
		t.Fatalf("want one started seq0 moxy record, got %+v", recs)
	}
}

func TestRingmasterDoneWritesTerminalRecord(t *testing.T) {
	producerEnv(t)
	out := captureStdout(t, func() { ringmasterStart([]string{"--source", "s"}) })
	id := trimTrailingNewline(out)
	if code := ringmasterDone([]string{id, "--state", "succeeded", "--message", "ok", "--result-ref", "ref"}); code != 0 {
		t.Fatalf("done exit = %d, want 0", code)
	}
	recs, err := jobwake.ReadJob(jobwake.ChannelID("repo/branch"), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[1].Type != jobwake.TypeSucceeded || recs[1].ResultRef != "ref" {
		t.Fatalf("bad terminal record: %+v", recs)
	}
}

func TestRingmasterDoneBadStateExitsNonZero(t *testing.T) {
	producerEnv(t)
	out := captureStdout(t, func() { ringmasterStart([]string{"--source", "s"}) })
	id := trimTrailingNewline(out)
	if code := ringmasterDone([]string{id, "--state", "wat"}); code == 0 {
		t.Fatal("done with invalid state must exit non-zero")
	}
}

// `ringmaster done --resource <uri>` attaches the resource to the terminal record.
func TestRingmasterDoneCarriesResource(t *testing.T) {
	producerEnv(t)
	out := captureStdout(t, func() { ringmasterStart([]string{"--source", "s"}) })
	id := trimTrailingNewline(out)
	if code := ringmasterDone([]string{id, "--state", "succeeded", "--resource", "madder://blobs/z"}); code != 0 {
		t.Fatalf("done exit = %d, want 0", code)
	}
	recs, err := jobwake.ReadJob(jobwake.ChannelID("repo/branch"), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || len(recs[1].Resources) != 1 || recs[1].Resources[0].URI != "madder://blobs/z" {
		t.Fatalf("terminal record must carry the resource, got %+v", recs)
	}
}

func TestRingmasterDoneSecondTerminalExitsNonZero(t *testing.T) {
	producerEnv(t)
	out := captureStdout(t, func() { ringmasterStart([]string{"--source", "s"}) })
	id := trimTrailingNewline(out)
	if code := ringmasterDone([]string{id, "--state", "succeeded"}); code != 0 {
		t.Fatalf("first done exit = %d, want 0", code)
	}
	if code := ringmasterDone([]string{id, "--state", "failed"}); code == 0 {
		t.Fatal("second terminal done must exit non-zero")
	}
}

func TestRingmasterDoneMissingJobArgExits2(t *testing.T) {
	producerEnv(t)
	if code := ringmasterDone([]string{"--state", "succeeded"}); code != 2 {
		t.Fatalf("done with no job id exit = %d, want 2", code)
	}
}

func TestRingmasterDoneMissingStateExits2(t *testing.T) {
	producerEnv(t)
	out := captureStdout(t, func() { ringmasterStart([]string{"--source", "s"}) })
	id := trimTrailingNewline(out)
	if code := ringmasterDone([]string{id}); code != 2 {
		t.Fatalf("done with no --state exit = %d, want 2", code)
	}
}

// --target threads through done: a job started+done --target <other> lands its
// records in <other>'s channel, not the current session's.
func TestRingmasterDoneTargetWakesTargetSession(t *testing.T) {
	producerEnv(t) // current session is "repo/branch"
	out := captureStdout(t, func() {
		ringmasterStart([]string{"--target", "other-session", "--source", "moxy", "--label", "build"})
	})
	id := trimTrailingNewline(out)

	if code := ringmasterDone([]string{id, "--target", "other-session", "--state", "succeeded"}); code != 0 {
		t.Fatalf("cross-session done exit = %d, want 0", code)
	}

	recs, err := jobwake.ReadJob(jobwake.ChannelID("other-session"), id)
	if err != nil {
		t.Fatalf("reading target channel: %v", err)
	}
	if len(recs) != 2 || recs[1].Type != jobwake.TypeSucceeded {
		t.Fatalf("want started+succeeded in target channel, got %+v", recs)
	}
	if _, err := jobwake.ReadJob(jobwake.ChannelID("repo/branch"), id); !os.IsNotExist(err) {
		t.Fatalf("current session must not have the cross-session job; err = %v", err)
	}
}

func TestRingmasterProgressIsJournalOnly(t *testing.T) {
	producerEnv(t)
	out := captureStdout(t, func() { ringmasterStart([]string{"--source", "s"}) })
	id := trimTrailingNewline(out)
	if code := ringmasterProgress([]string{id, "--message", "halfway"}); code != 0 {
		t.Fatalf("progress exit = %d, want 0", code)
	}
	recs, _ := jobwake.ReadJob(jobwake.ChannelID("repo/branch"), id)
	if len(recs) != 2 || recs[1].Type != jobwake.TypeProgress || recs[1].Message != "halfway" {
		t.Fatalf("bad progress record: %+v", recs)
	}
}

func TestRingmasterReadJobDetailEmitsFullStream(t *testing.T) {
	producerEnv(t)
	out := captureStdout(t, func() { ringmasterStart([]string{"--source", "moxy", "--label", "build"}) })
	id := trimTrailingNewline(out)
	_ = ringmasterProgress([]string{id, "--message", "halfway"})
	_ = ringmasterDone([]string{id, "--state", "succeeded", "--message", "done"})

	got := captureStdout(t, func() { ringmasterRead([]string{"--job", id}) })
	lines := nonEmptyLines(got)
	if len(lines) != 3 {
		t.Fatalf("want 3 lines for the job stream, got %d: %q", len(lines), got)
	}
}

func TestRingmasterReadJSONDetail(t *testing.T) {
	producerEnv(t)
	out := captureStdout(t, func() { ringmasterStart([]string{"--source", "moxy"}) })
	id := trimTrailingNewline(out)
	_ = ringmasterDone([]string{id, "--state", "succeeded"})
	got := captureStdout(t, func() { ringmasterRead([]string{"--job", id, "--json"}) })
	lines := nonEmptyLines(got)
	if len(lines) != 2 {
		t.Fatalf("want 2 NDJSON lines, got %d: %q", len(lines), got)
	}
	for _, ln := range lines {
		if ln[0] != '{' {
			t.Fatalf("expected JSON object per line, got %q", ln)
		}
	}
}

func TestRingmasterReadChannelWakingFilter(t *testing.T) {
	producerEnv(t)
	out := captureStdout(t, func() { ringmasterStart([]string{"--source", "moxy", "--label", "done-job"}) })
	doneID := trimTrailingNewline(out)
	_ = ringmasterDone([]string{doneID, "--state", "succeeded"})
	out = captureStdout(t, func() { ringmasterStart([]string{"--source", "moxy", "--label", "running-job"}) })
	_ = trimTrailingNewline(out)

	got := captureStdout(t, func() { ringmasterRead(nil) })
	lines := nonEmptyLines(got)
	if len(lines) != 1 {
		t.Fatalf("channel read must return only the one waking event, got %d: %q", len(lines), got)
	}
}

// The dispatch kill-switch: `start` no-ops with exit 0 and writes nothing when
// CLOWN_DISABLE_JOB_WAKEUP=1 (RFC-0009 §8).
func TestRingmasterStartDisabledIsNoOp(t *testing.T) {
	producerEnv(t)
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "1")
	if code := run([]string{"start", "--source", "s"}); code != 0 {
		t.Fatalf("disabled start exit = %d, want 0", code)
	}
	entries, err := os.ReadDir(jobwake.JournalDir(jobwake.ChannelID("repo/branch")))
	if err == nil && len(entries) > 0 {
		t.Fatalf("disabled start must not write journal, found %d entries", len(entries))
	}
}

// monitor --once replays the unacked terminal event and exits 0; a second run
// (ack now persisted) emits nothing.
func TestRingmasterMonitorOnceReplaysThenExits(t *testing.T) {
	producerEnv(t)
	out := captureStdout(t, func() { ringmasterStart([]string{"--source", "moxy", "--label", "build"}) })
	id := trimTrailingNewline(out)
	if code := ringmasterDone([]string{id, "--state", "succeeded", "--message", "ok"}); code != 0 {
		t.Fatalf("done exit = %d, want 0", code)
	}

	var code int
	first := captureStdout(t, func() { code = ringmasterMonitor([]string{"--once"}) })
	if code != 0 {
		t.Fatalf("monitor --once exit = %d, want 0", code)
	}
	want := "[clown-job] moxy " + id + " succeeded: ok"
	if trimTrailingNewline(first) != want {
		t.Fatalf("monitor --once output = %q, want %q", first, want)
	}

	second := captureStdout(t, func() { ringmasterMonitor([]string{"--once"}) })
	if second != "" {
		t.Fatalf("second --once must emit nothing (acked), got %q", second)
	}
}

func TestRingmasterMonitorDisabledExitsZeroImmediately(t *testing.T) {
	producerEnv(t)
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "1")
	if code := ringmasterMonitor(nil); code != 0 {
		t.Fatalf("disabled monitor exit = %d, want 0", code)
	}
}

// A directed message (emitted via the jobwake API, as troupe message would)
// surfaces through monitor --once with the §9 from-rendering, exactly once.
func TestRingmasterMonitorEmitsMessageFromLine(t *testing.T) {
	producerEnv(t) // session is "repo/branch"
	id, err := jobwake.Message("repo/branch", "spinclass", "clown/other", "ping", "")
	if err != nil {
		t.Fatal(err)
	}

	first := captureStdout(t, func() { ringmasterMonitor([]string{"--once"}) })
	want := "[clown-job] spinclass " + id + " message from clown/other: ping"
	if trimTrailingNewline(first) != want {
		t.Fatalf("monitor --once output = %q, want %q", first, want)
	}

	second := captureStdout(t, func() { ringmasterMonitor([]string{"--once"}) })
	if second != "" {
		t.Fatalf("second --once must emit nothing (acked), got %q", second)
	}
}

// `ringmaster wait <id> --json` on an already-terminal job prints its status
// with the terminal state (clown#154).
func TestRingmasterWaitReturnsStatusOnTerminal(t *testing.T) {
	producerEnv(t)
	id, err := jobwake.Start(jobwake.StartOpts{Source: "moxy", Label: "build"})
	if err != nil {
		t.Fatal(err)
	}
	if err := jobwake.Done("", id, jobwake.TypeSucceeded, "all good", "madder://blobs/x"); err != nil {
		t.Fatal(err)
	}
	var code int
	out := captureStdout(t, func() { code = run([]string{"wait", id, "--json"}) })
	if code != 0 {
		t.Fatalf("wait exit = %d, want 0", code)
	}
	var st jobwake.Status
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &st); err != nil {
		t.Fatalf("wait --json not valid JSON: %v\n%s", err, out)
	}
	if st.State != jobwake.TypeSucceeded {
		t.Fatalf("status state = %q, want succeeded", st.State)
	}
}

func TestRingmasterWaitMissingIDExits2(t *testing.T) {
	producerEnv(t)
	if code := run([]string{"wait"}); code != 2 {
		t.Fatalf("wait without id exit = %d, want 2", code)
	}
}

func TestRingmasterWaitUnknownJobExits1(t *testing.T) {
	producerEnv(t)
	if code := run([]string{"wait", "nope-1a2b"}); code != 1 {
		t.Fatalf("wait on unknown job exit = %d, want 1", code)
	}
}

func TestRingmasterWaitTimeoutExits1(t *testing.T) {
	producerEnv(t)
	id, err := jobwake.Start(jobwake.StartOpts{Source: "moxy"})
	if err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"wait", id, "--timeout", "50ms"}); code != 1 {
		t.Fatalf("wait timeout exit = %d, want 1", code)
	}
}
