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
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// extractTurnText collects only the LAST turn's assistant text blocks: a
// genuine user prompt (string content, or any block array that is not purely
// tool_result — an image-only paste counts) resets the collection; tool_result
// user entries, meta entries, sidechain entries, thinking and tool_use blocks
// never contribute.
func TestExtractTurnText(t *testing.T) {
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
	got, err := extractTurnText(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "mid-turn note\n\nfinal answer"
	if got != want {
		t.Fatalf("extracted text = %q, want %q", got, want)
	}
}

func TestExtractTurnTextEmptyTurn(t *testing.T) {
	path := writeTranscript(
		t,
		`{"type":"user","message":{"content":"earlier prompt"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"earlier reply"}]}}`,
		`{"type":"user","message":{"content":"latest prompt"}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{}}]}}`,
	)
	got, err := extractTurnText(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("turn with no text blocks must extract empty, got %q", got)
	}
}

// teeSubjectBody: subject is the first non-empty line (rune-capped), body the
// verbatim text (byte-capped rune-safe with a truncation marker).
func TestTeeSubjectBody(t *testing.T) {
	subj, body := teeSubjectBody("Fixed the bug.\n\nDetails follow.")
	if subj != "Fixed the bug." {
		t.Fatalf("subject = %q", subj)
	}
	if body != "Fixed the bug.\n\nDetails follow." {
		t.Fatalf("body must be the verbatim reply, got %q", body)
	}

	longLine := strings.Repeat("ä", 300)
	subj, _ = teeSubjectBody(longLine)
	if r := []rune(subj); len(r) != teeSubjectMaxRunes || r[len(r)-1] != '…' {
		t.Fatalf("long subject must be capped at %d runes with an ellipsis, got %d runes", teeSubjectMaxRunes, len(r))
	}

	huge := strings.Repeat("ü", teeBodyMaxBytes) // 2 bytes per rune: over the cap
	_, body = teeSubjectBody(huge)
	if len(body) > teeBodyMaxBytes+64 {
		t.Fatalf("body not truncated: %d bytes", len(body))
	}
	if !strings.HasSuffix(body, "[clown-hook-tee: reply truncated]") {
		t.Fatal("truncated body must carry the truncation marker")
	}
	if !utf8.ValidString(body) {
		t.Fatal("truncation must not split a rune")
	}
}

// run gates on the xmpp-native transport env and, when eligible, spawns the
// detached `troupe muc send` with the derived room and the turn's reply as
// --subject/--body. The troupe binary is stubbed with a script recording argv.
func TestRunSpawnsMucSend(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	stub := filepath.Join(dir, "troupe")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvFile + "\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TROUPE_TRANSPORT", "xmpp-native")
	t.Setenv("TROUPE_XMPP_PASSWORD_FILE", "/run/troupe/x.pass")
	t.Setenv("TROUPE_XMPP_DOMAIN", "flac.xmpp.starbrandshoes.com")
	t.Setenv("TROUPE_XMPP_ROOMS", "fleet@muc.starbrandshoes.com=all")
	t.Setenv("CLOWN_GROUP_ID", "clown/rare-redwood")
	t.Setenv("CLOWN_HOOK_DEBUG_LOG", "")

	transcript := writeTranscript(
		t,
		`{"type":"user","message":{"content":"do the thing"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Done: the thing.\nSecond line."}]}}`,
	)
	stdin := strings.NewReader(`{"transcript_path":` + strconvQuote(transcript) + `}`)
	if err := run(stdin, stub); err != nil {
		t.Fatal(err)
	}

	// The send is detached; poll briefly for the stub's argv record.
	var argv string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(argvFile); err == nil && len(b) > 0 {
			argv = string(b)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if argv == "" {
		t.Fatal("stub troupe binary was never invoked")
	}
	// The stub prints one argv element per line, so the body's own newline
	// makes it span two lines — assert on the raw newline-joined record.
	for _, want := range []string{
		"muc\nsend\n",
		"--room\nclown.rare-redwood@muc.starbrandshoes.com\n",
		"--subject\nDone: the thing.\n",
		"--body\nDone: the thing.\nSecond line.\n",
	} {
		if !strings.Contains(argv, want) {
			t.Fatalf("argv missing %q:\n%s", want, argv)
		}
	}
}

// Ineligible sessions (wrong transport, no credential) and empty turns spawn
// nothing.
func TestRunGates(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	stub := filepath.Join(dir, "troupe")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvFile + "\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
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
	t.Run("empty turn spawns nothing", func(t *testing.T) {
		t.Setenv("TROUPE_TRANSPORT", "xmpp-native")
		t.Setenv("TROUPE_XMPP_PASSWORD_FILE", "/run/troupe/x.pass")
		t.Setenv("TROUPE_XMPP_DOMAIN", "flac.xmpp.starbrandshoes.com")
		t.Setenv("TROUPE_XMPP_ROOMS", "")
		t.Setenv("CLOWN_GROUP_ID", "clown/rare-redwood")
		if err := run(strings.NewReader(stdinJSON), stub); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		if _, err := os.Stat(argvFile); !os.IsNotExist(err) {
			t.Fatal("no send may be spawned for a turn with no reply text")
		}
	})
}

// strconvQuote JSON-quotes a test path for embedding in hook-input JSON.
func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
