package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// deriveWorktreeRoom mirrors troupe's receiver.DeriveRooms per-worktree entry
// (troupe#24): <repo>.<worktree>@<rooms-domain>, anchored on the first static
// room's domainpart, falling back to rooms.<zone>. These cases track troupe's
// own derive_test.go so a drift between the two implementations fails here.
func TestDeriveWorktreeRoom(t *testing.T) {
	cases := []struct {
		name                          string
		groupKey, c2sDomain, roomsEnv string
		want                          string
		wantErr                       bool
	}{
		{
			name:     "anchored on the first static room's component",
			groupKey: "troupe/rich-elder", c2sDomain: "flac.xmpp.starbrandshoes.com",
			roomsEnv: "fleet@muc.starbrandshoes.com=all",
			want:     "troupe.rich-elder@muc.starbrandshoes.com",
		},
		{
			name:     "policy suffix and whitespace tolerated, first entry wins",
			groupKey: "clown/rare-redwood", c2sDomain: "flac.xmpp.starbrandshoes.com",
			roomsEnv: " fleet@muc.example.com = all , dev@other.example.com",
			want:     "clown.rare-redwood@muc.example.com",
		},
		{
			name:     "no static rooms falls back to rooms.<zone>",
			groupKey: "troupe/rich-elder", c2sDomain: "flac.xmpp.starbrandshoes.com",
			roomsEnv: "",
			want:     "troupe.rich-elder@rooms.xmpp.starbrandshoes.com",
		},
		{
			name:     "dotted repo rejected",
			groupKey: "tro.upe/rich-elder", c2sDomain: "flac.xmpp.starbrandshoes.com",
			wantErr: true,
		},
		{
			name:     "dotted worktree rejected",
			groupKey: "troupe/rich.elder", c2sDomain: "flac.xmpp.starbrandshoes.com",
			wantErr: true,
		},
		{
			name:     "malformed group key rejected",
			groupKey: "no-slash", c2sDomain: "flac.xmpp.starbrandshoes.com",
			wantErr: true,
		},
		{
			name:     "empty worktree rejected",
			groupKey: "repo/", c2sDomain: "flac.xmpp.starbrandshoes.com",
			wantErr: true,
		},
		{
			name:     "bare c2s domain rejected without a static anchor",
			groupKey: "troupe/rich-elder", c2sDomain: "localhost",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := deriveWorktreeRoom(tc.groupKey, tc.c2sDomain, tc.roomsEnv)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("room = %q, want %q", got, tc.want)
			}
		})
	}
}

// writeTranscript writes lines as a JSONL transcript file and returns its path.
func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscriptTo(t, path, lines...)
	return path
}

// writeTranscriptTo (re)writes lines to path as JSONL, for tests that grow a
// transcript across multiple run() calls.
func writeTranscriptTo(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Bootstrap mode (no cursor): only the LAST turn's assistant text posts — a
// genuine user prompt (string content, or any block array that is not purely
// tool_result — an image-only paste counts) resets the collection; tool_result
// user entries, meta entries, sidechain entries, thinking and tool_use blocks
// never contribute. The consumed offset lands at end-of-file.
func TestExtractSinceBootstrap(t *testing.T) {
	path := writeTranscript(
		t,
		`{"type":"user","message":{"content":"first prompt"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"stale reply from an earlier turn"}]}}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"second prompt"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"also stale"}]}}`,
		`{"type":"user","message":{"content":[{"type":"image","source":{"type":"base64"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"mid-turn note"},{"type":"tool_use","name":"Read","input":{}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"tool output — not a prompt"}]}}`,
		`{"type":"user","isMeta":true,"message":{"content":"system reminder — not a prompt"}}`,
		`{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"text","text":"subagent text — excluded"}]}}`,
		`not even json`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"final answer"}]}}`,
	)
	got, consumed, err := extractSince(path, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "mid-turn note\n\nfinal answer"
	if got != want {
		t.Fatalf("extracted text = %q, want %q", got, want)
	}
	if size := fileSize(t, path); consumed != size {
		t.Fatalf("consumed = %d, want file size %d", consumed, size)
	}
}

// Cursor mode: every main-line assistant text block from the offset on
// contributes and user prompts do NOT reset — the clown#224 property that lets
// a late-flushed final message ride along with the next post.
func TestExtractSinceCursorDoesNotResetAtPrompts(t *testing.T) {
	first := `{"type":"user","message":{"content":"first prompt"}}`
	path := writeTranscript(
		t,
		first,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"late-flushed final block"}]}}`,
		`{"type":"user","message":{"content":"second prompt"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"next turn's text"}]}}`,
	)
	offset := int64(len(first) + 1) // just past the first prompt line
	got, consumed, err := extractSince(path, offset, true)
	if err != nil {
		t.Fatal(err)
	}
	want := "late-flushed final block\n\nnext turn's text"
	if got != want {
		t.Fatalf("extracted text = %q, want %q", got, want)
	}
	if size := fileSize(t, path); consumed != size {
		t.Fatalf("consumed = %d, want file size %d", consumed, size)
	}
}

// A trailing line with no newline that is not valid JSON is a torn in-flight
// append: it must be left unconsumed so the next post picks it up whole. A
// complete-JSON trailing line without its newline IS consumed.
func TestExtractSinceTornTail(t *testing.T) {
	complete := `{"type":"assistant","message":{"content":[{"type":"text","text":"whole"}]}}`
	torn := `{"type":"assistant","message":{"content":[{"type":"text","te`
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(complete+"\n"+torn), 0o600); err != nil {
		t.Fatal(err)
	}
	got, consumed, err := extractSince(path, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "whole" {
		t.Fatalf("extracted text = %q, want %q", got, "whole")
	}
	if want := int64(len(complete) + 1); consumed != want {
		t.Fatalf("consumed = %d, want %d (torn tail unconsumed)", consumed, want)
	}

	unterminated := `{"type":"assistant","message":{"content":[{"type":"text","text":"tail"}]}}`
	if err := os.WriteFile(path, []byte(complete+"\n"+unterminated), 0o600); err != nil {
		t.Fatal(err)
	}
	got, consumed, err = extractSince(path, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := "whole\n\ntail"; got != want {
		t.Fatalf("extracted text = %q, want %q", got, want)
	}
	if size := fileSize(t, path); consumed != size {
		t.Fatalf("consumed = %d, want file size %d (valid unterminated tail consumed)", consumed, size)
	}
}

// A cursor beyond EOF (transcript replaced or truncated) falls back to
// bootstrap mode: last turn only, from offset 0.
func TestExtractSinceOffsetBeyondEOF(t *testing.T) {
	path := writeTranscript(
		t,
		`{"type":"user","message":{"content":"old prompt"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"old reply"}]}}`,
		`{"type":"user","message":{"content":"new prompt"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"new reply"}]}}`,
	)
	got, consumed, err := extractSince(path, 1<<40, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "new reply" {
		t.Fatalf("extracted text = %q, want %q", got, "new reply")
	}
	if size := fileSize(t, path); consumed != size {
		t.Fatalf("consumed = %d, want file size %d", consumed, size)
	}
}

// teeSubjectBody cuts at the first blank line so troupe's wire rendering
// (subject + "\n\n" + body) reconstructs the reply byte-for-byte — no
// duplicated first line (clown#224).
func TestTeeSubjectBody(t *testing.T) {
	reply := "Fixed the bug.\n\nDetails follow.\nMore details."
	subj, body := teeSubjectBody(reply)
	if subj != "Fixed the bug." {
		t.Fatalf("subject = %q", subj)
	}
	if body != "Details follow.\nMore details." {
		t.Fatalf("body = %q", body)
	}
	if rejoined := subj + "\n\n" + body; rejoined != reply {
		t.Fatalf("wire reconstruction = %q, want the verbatim reply %q", rejoined, reply)
	}

	// No blank line: the whole reply travels as the subject, empty body —
	// joinMessage then emits the subject alone, still byte-for-byte.
	subj, body = teeSubjectBody("Done: the thing.\nSecond line.")
	if subj != "Done: the thing.\nSecond line." || body != "" {
		t.Fatalf("no-blank-line split = (%q, %q)", subj, body)
	}

	huge := strings.Repeat("ü", teeBodyMaxBytes) // 2 bytes per rune: over the cap
	subj, body = teeSubjectBody(huge)
	rejoined := subj + "\n\n" + body
	if len(rejoined) > teeBodyMaxBytes+64 {
		t.Fatalf("not truncated: %d bytes", len(rejoined))
	}
	if !strings.HasSuffix(body, "[clown-hook-tee: reply truncated]") {
		t.Fatal("truncated reply must carry the truncation marker")
	}
	if !utf8.ValidString(rejoined) {
		t.Fatal("truncation must not split a rune")
	}
}

// fastSettle zeroes the settle-wait so run() tests don't pay real wall time.
func fastSettle(t *testing.T) {
	t.Helper()
	q, m, p := settleQuiet, settleMax, settlePoll
	settleQuiet, settleMax, settlePoll = 0, 0, 0
	t.Cleanup(func() { settleQuiet, settleMax, settlePoll = q, m, p })
}

// writeStubTroupe writes a shell stub that records its argv (one element per
// line) and returns (stubPath, argvPath).
func writeStubTroupe(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	stub := filepath.Join(dir, "troupe")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvFile + "\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return stub, argvFile
}

// waitArgv polls for the detached stub's argv record.
func waitArgv(t *testing.T, argvFile string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(argvFile); err == nil && len(b) > 0 {
			return string(b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stub troupe binary was never invoked")
	return ""
}

func teeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TROUPE_TRANSPORT", "xmpp-native")
	t.Setenv("TROUPE_XMPP_PASSWORD_FILE", "/run/troupe/x.pass")
	t.Setenv("TROUPE_XMPP_DOMAIN", "flac.xmpp.starbrandshoes.com")
	t.Setenv("TROUPE_XMPP_ROOMS", "fleet@muc.starbrandshoes.com=all")
	t.Setenv("CLOWN_GROUP_ID", "clown/rare-redwood")
	t.Setenv("CLOWN_HOOK_DEBUG_LOG", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // cursor state stays inside the test
}

// run gates on the xmpp-native transport env and, when eligible, spawns the
// detached `troupe muc send` with the derived room and the reply split at its
// first blank line into --subject/--body. The cursor is persisted at the
// consumed offset.
func TestRunSpawnsMucSend(t *testing.T) {
	fastSettle(t)
	teeEnv(t)
	stub, argvFile := writeStubTroupe(t)

	transcript := writeTranscript(
		t,
		`{"type":"user","message":{"content":"do the thing"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Done: the thing.\n\nSecond paragraph."}]}}`,
	)
	stdin := strings.NewReader(`{"transcript_path":` + strconvQuote(transcript) + `}`)
	if err := run(stdin, stub); err != nil {
		t.Fatal(err)
	}

	argv := waitArgv(t, argvFile)
	// The stub prints one argv element per line — assert on the raw
	// newline-joined record.
	for _, want := range []string{
		"muc\nsend\n",
		"--room\nclown.rare-redwood@muc.starbrandshoes.com\n",
		"--subject\nDone: the thing.\n--body\nSecond paragraph.\n",
	} {
		if !strings.Contains(argv, want) {
			t.Fatalf("argv missing %q:\n%s", want, argv)
		}
	}
	if got, ok := loadCursor(transcript); !ok || got != fileSize(t, transcript) {
		t.Fatalf("cursor after post = (%d, %v), want (%d, true)", got, ok, fileSize(t, transcript))
	}
}

// The clown#224 regression: the turn's final assistant message is flushed to
// the transcript only AFTER the Stop hook has read it. The cursor must carry
// that late block into the NEXT post (no reset at the intervening prompt) and
// must never repeat text a post already carried.
func TestRunCursorCarriesLateFlushedFinalBlock(t *testing.T) {
	fastSettle(t)
	teeEnv(t)
	stub, argvFile := writeStubTroupe(t)

	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	prompt1 := `{"type":"user","message":{"content":"do the thing"}}`
	interim := `{"type":"assistant","message":{"content":[{"type":"text","text":"Interim status."}]}}`
	writeTranscriptTo(t, transcript, prompt1, interim)

	stdinJSON := `{"transcript_path":` + strconvQuote(transcript) + `}`
	if err := run(strings.NewReader(stdinJSON), stub); err != nil {
		t.Fatal(err)
	}
	if argv := waitArgv(t, argvFile); !strings.Contains(argv, "--subject\nInterim status.\n") {
		t.Fatalf("first post must carry the flushed interim text:\n%s", argv)
	}
	if err := os.Remove(argvFile); err != nil {
		t.Fatal(err)
	}

	// The final block lands after the first Stop already ran; then the next
	// turn happens and its Stop fires.
	writeTranscriptTo(
		t, transcript,
		prompt1,
		interim,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Final answer, flushed late."}]}}`,
		`{"type":"user","message":{"content":"next task"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Next turn's reply."}]}}`,
	)
	if err := run(strings.NewReader(stdinJSON), stub); err != nil {
		t.Fatal(err)
	}
	argv := waitArgv(t, argvFile)
	if !strings.Contains(argv, "--subject\nFinal answer, flushed late.\n--body\nNext turn's reply.\n") {
		t.Fatalf("second post must carry the late final block plus the new turn:\n%s", argv)
	}
	if strings.Contains(argv, "Interim status.") {
		t.Fatalf("second post repeats text the first post already carried:\n%s", argv)
	}
}

// Ineligible sessions (wrong transport, no credential) and empty turns spawn
// nothing; an empty turn still initializes the cursor.
func TestRunGates(t *testing.T) {
	fastSettle(t)
	stub, argvFile := writeStubTroupe(t)
	transcript := writeTranscript(
		t,
		`{"type":"user","message":{"content":"do the thing"}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{}}]}}`,
	)
	stdinJSON := `{"transcript_path":` + strconvQuote(transcript) + `}`

	t.Run("wrong transport", func(t *testing.T) {
		t.Setenv("TROUPE_TRANSPORT", "local")
		t.Setenv("TROUPE_XMPP_PASSWORD_FILE", "/run/troupe/x.pass")
		if err := run(strings.NewReader(stdinJSON), stub); err == nil {
			t.Fatal("want an (ignored) gate error on the local transport")
		}
	})
	t.Run("no credential", func(t *testing.T) {
		t.Setenv("TROUPE_TRANSPORT", "xmpp-native")
		t.Setenv("TROUPE_XMPP_PASSWORD_FILE", "")
		if err := run(strings.NewReader(stdinJSON), stub); err == nil {
			t.Fatal("want an (ignored) gate error without a minted credential")
		}
	})
	t.Run("empty turn spawns nothing but initializes the cursor", func(t *testing.T) {
		teeEnv(t)
		t.Setenv("TROUPE_XMPP_ROOMS", "")
		if err := run(strings.NewReader(stdinJSON), stub); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		if _, err := os.Stat(argvFile); !os.IsNotExist(err) {
			t.Fatal("no send may be spawned for a turn with no reply text")
		}
		if got, ok := loadCursor(transcript); !ok || got != fileSize(t, transcript) {
			t.Fatalf("cursor after empty turn = (%d, %v), want (%d, true)", got, ok, fileSize(t, transcript))
		}
	})
}

// fileSize returns path's size, failing the test on error.
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// strconvQuote JSON-quotes a test path for embedding in hook-input JSON.
func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
