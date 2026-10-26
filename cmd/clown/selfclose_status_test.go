package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/sessionstatus"
)

// The hung-session status file (clown#322): the idle listener writes the
// frontend's facts for spinclass to classify a session.

func readStatusFile(t *testing.T, principal string) (sessionstatus.Status, bool) {
	t.Helper()
	path, err := sessionstatus.Path(principal)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return sessionstatus.Status{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var st sessionstatus.Status
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("status: %v\n%s", err, b)
	}
	return st, true
}

func startStatusListener(t *testing.T) (sock string, busy *atomic.Bool, runs *atomic.Int32, l *idleListener) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	w, err := sessionstatus.Open("p-1", "repo/b", func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	w.Start()
	sock = filepath.Join(shortSocketDir(t), "k.sock")
	busy, runs = &atomic.Bool{}, &atomic.Int32{}
	l, err = startIdleListenerTracking(sock, busy, w, func() { runs.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	return sock, busy, runs, l
}

func sendDatagram(t *testing.T, sock string, b []byte) {
	t.Helper()
	c, err := net.Dial("unixgram", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write(b); err != nil {
		t.Fatal(err)
	}
}

const hookStdin = `{"session_id":"sess-9","transcript_path":"/home/u/.claude/projects/w/sess-9.jsonl","cwd":"/w","hook_event_name":"UserPromptSubmit","prompt":"hi"}`

// A busy hook carrying its stdin payload, then the Stop poke: turn_running
// follows, turn_started_at is kept at idle, last_hook_at moves, and the
// transcript path and claude session id come from the payload.
func TestIdleListenerWritesTheStatusFile(t *testing.T) {
	sock, busy, runs, _ := startStatusListener(t)
	if st, ok := readStatusFile(t, "p-1"); !ok || st.TurnRunning || st.LastHookAt != "" {
		t.Fatalf("start write: %+v %v", st, ok)
	}
	if code := idleNotify([]string{"--busy", sock}, strings.NewReader(hookStdin), true); code != 0 {
		t.Fatalf("exit %d", code)
	}
	waitFor(t, "the busy mark", busy.Load)
	var started sessionstatus.Status
	waitFor(t, "a busy status", func() bool {
		started, _ = readStatusFile(t, "p-1")
		return started.TurnRunning
	})
	if started.TranscriptPath != "/home/u/.claude/projects/w/sess-9.jsonl" || started.ClaudeSessionID != "sess-9" || started.TurnStartedAt == "" || started.LastHookAt == "" {
		t.Fatalf("busy status: %+v", started)
	}
	time.Sleep(2 * time.Millisecond)
	pokeIdle(t, sock)
	waitFor(t, "the idle run", func() bool { return runs.Load() == 1 })
	var idle sessionstatus.Status
	waitFor(t, "an idle status", func() bool {
		idle, _ = readStatusFile(t, "p-1")
		return !idle.TurnRunning
	})
	if idle.TurnStartedAt != started.TurnStartedAt || idle.LastHookAt <= started.LastHookAt || idle.TranscriptPath != started.TranscriptPath {
		t.Fatalf("idle status %+v after %+v", idle, started)
	}
}

// The old one-byte busy datagram (an idle-notify that sends no payload) still
// marks the turn running and records no transcript.
func TestOneByteBusyDatagramStillWorks(t *testing.T) {
	sock, busy, runs, _ := startStatusListener(t)
	sendDatagram(t, sock, []byte{turnStartedDatagram})
	waitFor(t, "the busy mark", busy.Load)
	waitFor(t, "a busy status", func() bool { st, _ := readStatusFile(t, "p-1"); return st.TurnRunning })
	if st, _ := readStatusFile(t, "p-1"); st.TranscriptPath != "" || st.ClaudeSessionID != "" {
		t.Fatalf("no payload, yet %+v", st)
	}
	if runs.Load() != 0 {
		t.Fatal("a turn start ran the idle work")
	}
}

// A busy datagram with a garbage or oversized payload is still a turn start;
// the payload is dropped.
func TestBusyDatagramWithAGarbagePayload(t *testing.T) {
	for name, payload := range map[string][]byte{
		"garbage":   []byte("not json"),
		"oversized": []byte(`{"transcript_path":"/` + strings.Repeat("x", 70000) + `"}`),
		"relative":  []byte(`{"transcript_path":"rel/x.jsonl"}`),
	} {
		t.Run(name, func(t *testing.T) {
			sock, busy, runs, _ := startStatusListener(t)
			sendDatagram(t, sock, append([]byte{turnStartedDatagram}, payload...))
			waitFor(t, "the busy mark", busy.Load)
			waitFor(t, "a busy status", func() bool { st, _ := readStatusFile(t, "p-1"); return st.TurnRunning })
			if st, _ := readStatusFile(t, "p-1"); st.TranscriptPath != "" {
				t.Fatalf("payload taken: %+v", st)
			}
			if runs.Load() != 0 {
				t.Fatal("a turn start ran the idle work")
			}
		})
	}
}

// receiveOne binds a plain unixgram socket (standing in for a frontend) and
// returns the first datagram it gets.
func receiveOne(t *testing.T, send func(sock string)) []byte {
	t.Helper()
	sock := filepath.Join(shortSocketDir(t), "k.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send(sock)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1<<17)
	n, _, err := conn.ReadFromUnix(buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf[:n]
}

// Without the frontend's flag (an older frontend wrote the hook), idle-notify
// sends the one byte an old listener understands, whatever is on stdin.
func TestIdleNotifyWithoutTheFlagSendsOneByte(t *testing.T) {
	got := receiveOne(t, func(sock string) {
		idleNotify([]string{"--busy", sock}, strings.NewReader(hookStdin), false)
	})
	if string(got) != "B" {
		t.Fatalf("datagram %q, want the single busy byte", got)
	}
	got = receiveOne(t, func(sock string) { idleNotify([]string{sock}, strings.NewReader(hookStdin), true) })
	if len(got) != 1 || got[0] == turnStartedDatagram {
		t.Fatalf("the idle poke must stay one non-busy byte, got %q", got)
	}
}

// With the flag, the busy datagram is 'B' plus the two fields, nothing else
// from stdin (the prompt never leaves the hook).
func TestIdleNotifySendsOnlyTheTwoFields(t *testing.T) {
	got := receiveOne(t, func(sock string) {
		idleNotify([]string{"--busy", sock}, strings.NewReader(hookStdin), true)
	})
	if len(got) < 2 || got[0] != turnStartedDatagram {
		t.Fatalf("datagram %q", got)
	}
	if strings.Contains(string(got), "prompt") || strings.Contains(string(got), "hi\"") {
		t.Fatalf("the prompt leaked into the datagram: %q", got)
	}
	p, ok := sessionstatus.ParsePayload(got[1:])
	if !ok || p.SessionID != "sess-9" || p.TranscriptPath != "/home/u/.claude/projects/w/sess-9.jsonl" {
		t.Fatalf("payload %q -> %+v %v", got[1:], p, ok)
	}
}

// A huge prompt after the two fields does not stop them being read; the read
// is bounded either way.
func TestIdleNotifyReadsTheFieldsBeforeAHugePrompt(t *testing.T) {
	stdin := `{"session_id":"s","transcript_path":"/t.jsonl","prompt":"` + strings.Repeat("p", 4<<20) + `"}`
	got := receiveOne(t, func(sock string) {
		idleNotify([]string{"--busy", sock}, strings.NewReader(stdin), true)
	})
	if p, ok := sessionstatus.ParsePayload(got[1:]); !ok || p.TranscriptPath != "/t.jsonl" || p.SessionID != "s" {
		t.Fatalf("payload %q", got)
	}
}

// Absent, garbage, or never-closing stdin: the busy byte still goes out, and
// promptly.
func TestIdleNotifyToleratesABadStdin(t *testing.T) {
	never, w := io.Pipe()
	t.Cleanup(func() { w.Close() })
	for name, stdin := range map[string]io.Reader{
		"absent":        nil,
		"empty":         strings.NewReader(""),
		"garbage":       strings.NewReader("\x00\x01 not json {"),
		"never closing": never,
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			got := receiveOne(t, func(sock string) { idleNotify([]string{"--busy", sock}, stdin, true) })
			if string(got) != "B" {
				t.Fatalf("datagram %q, want the bare busy byte", got)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatalf("idle-notify took %v", time.Since(start))
			}
		})
	}
}

// The frontend tells its own idle-notify that it understands a payload: the
// flag rides in the UserPromptSubmit hook's command line only.
func TestSynthIdlePluginDirFlagsThePayloadOnTheBusyHook(t *testing.T) {
	dir, err := synthIdlePluginDir(testStagingRoot(t), "/nix/store/c/bin/clown", "/run/user/1001/clown/idle/k.sock")
	if err != nil {
		t.Fatal(err)
	}
	hb, err := os.ReadFile(filepath.Join(dir, "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(hb, &cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Hooks["UserPromptSubmit"][0].Hooks[0].Command; got != idleNotifyPayloadEnv+"=1 /nix/store/c/bin/clown idle-notify --busy /run/user/1001/clown/idle/k.sock" {
		t.Fatalf("busy hook %q", got)
	}
	if got := cfg.Hooks["Stop"][0].Hooks[0].Command; strings.Contains(got, idleNotifyPayloadEnv) {
		t.Fatalf("the Stop hook reads no stdin: %q", got)
	}
}

// Arming writes the start status; the writer takes the unit and main pid when
// they are known; the clean-exit closer removes the file.
func TestArmSelfCloseWritesAndRemovesTheStatusFile(t *testing.T) {
	bin, _ := fakeSpinclass(t, t.TempDir(), "0")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPINCLASS_SESSION_ID", "repo/b")
	t.Setenv("XDG_RUNTIME_DIR", shortSocketDir(t))
	t.Setenv("PATH", filepath.Dir(bin))
	t.Setenv("CLOWN_DISABLE_SELF_CLOSE", "")
	f := parsedFlags{provider: "claude"}
	f.identity.Key = "p-arm"

	dir, _, _, status, closeFn := armSelfClose(f, testStagingRoot(t), nil, nil, nil)
	if dir == "" || status == nil {
		t.Fatal("expected to arm with a status writer")
	}
	st, ok := readStatusFile(t, "p-arm")
	if !ok || st.Principal != "p-arm" || st.SpinclassSessionKey != "repo/b" || st.FrontendPID != os.Getpid() || st.TurnRunning {
		t.Fatalf("start status %+v %v", st, ok)
	}
	status.SetUnit("clown-agent-1.service")
	status.SetMainPID(777)
	if st, _ := readStatusFile(t, "p-arm"); st.Unit != "clown-agent-1.service" || st.MainPID != 777 {
		t.Fatalf("after the unit started: %+v", st)
	}
	closeFn()
	if _, ok := readStatusFile(t, "p-arm"); ok {
		t.Fatal("a clean exit must remove the status file")
	}
}

// Not armed: no writer, no file.
func TestUnarmedLaunchWritesNoStatusFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPINCLASS_SESSION_ID", "")
	f := parsedFlags{provider: "claude"}
	f.identity.Key = "p-off"
	_, _, _, status, closeFn := armSelfClose(f, testStagingRoot(t), nil, nil, nil)
	defer closeFn()
	if status != nil {
		t.Fatal("an unarmed launch must have no status writer")
	}
	if _, ok := readStatusFile(t, "p-off"); ok {
		t.Fatal("an unarmed launch wrote a status file")
	}
}
