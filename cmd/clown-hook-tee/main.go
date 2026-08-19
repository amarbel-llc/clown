// clown-hook-tee is a Claude Code Stop hook handler: the clown-side half of
// the session output tee (troupe#21). At the end of each turn it extracts the
// turn's user-visible assistant reply text from the transcript (text blocks
// only — no tool calls, no thinking, no transcript firehose) and posts it
// verbatim into the session's per-worktree MUC channel via `troupe muc send`,
// so a human or another agent can watch the session's replies live in a room.
//
// Ratified contract (troupe#21):
//
//   - Scope: assistant reply text ONLY, one message per turn (the Stop hook is
//     the per-reply boundary the harness exposes).
//   - The post goes out under the session's OWN nick — the default muc-send
//     nick resolution (TROUPE_XMPP_NICK, else the session key from
//     CLOWN_SESSION_ID). The troupe receiver suppresses self-echo by SelfNick
//     match, so the tee's own posts never wake the session. Do NOT invent a
//     distinct sender nick: a different nick breaks that suppression and
//     re-introduces self-wake.
//   - The target room is the per-worktree channel
//     <repo>.<worktree>@<rooms-domain>, derived from the session identity
//     exactly as troupe#24's receiver.DeriveRooms does: repo/worktree from
//     CLOWN_GROUP_ID ("<repo>/<worktree>", dot-joined), rooms-domain anchored
//     on the domainpart of the first static TROUPE_XMPP_ROOMS entry, falling
//     back to rooms.<zone> from TROUPE_XMPP_DOMAIN's <host>.<zone> shape.
//     That derivation is troupe-internal, so the small pure function is
//     mirrored here; drift between the two would land the tee in a room the
//     session's receiver doesn't watch (visible, not harmful).
//
// The hook is best-effort and must never disturb the session: every failure
// (missing env, unreadable transcript, malformed group key, spawn error) exits
// 0 silently — a Stop-hook exit 2 would BLOCK the agent from stopping, and any
// other non-zero would surface noise. The send itself is spawned detached
// (fire-and-forget, its own session via Setsid) so a slow or down XMPP server
// never stalls the turn boundary; a dropped tee message is an acceptable cost
// for a watch surface. Rate is bounded structurally (one post per turn) and
// size by teeBodyMaxBytes.
//
// Wire-up: clown's synthesized clown-builtin-jobs plugin registers this binary
// as a Stop hook (hooks/hooks.json, discovered via --plugin-dir) when the
// session runs the xmpp-native transport with a minted credential. The command
// carries a scoped `env CLOWN_SESSION_ID=<key>` prefix (clown#136 — clown does
// not export the key ambiently) so the muc-send child resolves the session's
// own nick, and `--troupe <path>` (buildcfg.TroupePath) names the binary to
// shell out to. See cmd/clown/jobmonitor.go's teeHookCommand.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unicode/utf8"
)

const (
	// teeBodyMaxBytes bounds the posted body. A reply longer than this is
	// truncated (rune-safe) with a marker — the room is a watch surface, not
	// durable storage, and an unbounded body risks stanza-size rejection.
	teeBodyMaxBytes = 16 * 1024
	// teeSubjectMaxRunes bounds the one-line subject (the first line of the
	// reply); the full first line is still present verbatim in the body.
	teeSubjectMaxRunes = 120
)

// hookInput is the Stop-hook event payload this handler consumes. Claude Code
// also sends session_id and stop_hook_active; only the transcript path matters
// here.
type hookInput struct {
	TranscriptPath string `json:"transcript_path"`
}

func main() {
	troupeBin := flag.String("troupe", "troupe", "path to the troupe binary to shell `muc send` out to")
	flag.Parse()
	// Best-effort by contract: never exit non-zero (2 would block the stop),
	// never write stdout (a Stop hook's stdout is parsed for decisions).
	if err := run(os.Stdin, *troupeBin); err != nil {
		debugLog("error", err.Error())
	}
	os.Exit(0)
}

// run reads the Stop event from stdin and, when the session is tee-eligible,
// spawns the detached muc-send post. Every ineligibility is an error only for
// the debug log; main discards it.
func run(stdin io.Reader, troupeBin string) error {
	if os.Getenv("TROUPE_TRANSPORT") != "xmpp-native" || os.Getenv("TROUPE_XMPP_PASSWORD_FILE") == "" {
		return errors.New("session is not on the xmpp-native transport with a minted credential")
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}
	var in hookInput
	if err := json.Unmarshal(data, &in); err != nil {
		return fmt.Errorf("parsing hook input: %w", err)
	}
	if in.TranscriptPath == "" {
		return errors.New("hook input carries no transcript_path")
	}
	room, err := deriveWorktreeRoom(
		os.Getenv("CLOWN_GROUP_ID"),
		os.Getenv("TROUPE_XMPP_DOMAIN"),
		os.Getenv("TROUPE_XMPP_ROOMS"),
	)
	if err != nil {
		return err
	}
	text, err := extractTurnText(in.TranscriptPath)
	if err != nil {
		return err
	}
	if text == "" {
		return nil // a turn with no visible reply text: nothing to tee
	}
	subject, body := teeSubjectBody(text)
	return spawnSend(troupeBin, room, subject, body)
}

// deriveWorktreeRoom computes the per-worktree channel JID
// <repo>.<worktree>@<rooms-domain>, mirroring troupe's receiver.DeriveRooms
// (troupe#24) so the tee lands in the room the session's receiver joins:
// groupKey is "<repo>/<worktree>" (CLOWN_GROUP_ID), c2sDomain the c2s vhost
// ("<host>.<zone>"), roomsEnv the static TROUPE_XMPP_ROOMS list whose first
// entry's domainpart anchors the rooms component (else rooms.<zone>). A '.'
// inside repo/worktree is rejected rather than mangled — dot is the localpart
// component separator, and both names are dot-forbidden upstream.
func deriveWorktreeRoom(groupKey, c2sDomain, roomsEnv string) (string, error) {
	repo, worktree, ok := strings.Cut(groupKey, "/")
	if !ok || repo == "" || worktree == "" {
		return "", fmt.Errorf("group key %q is not <repo>/<worktree>", groupKey)
	}
	if strings.Contains(repo, ".") || strings.Contains(worktree, ".") {
		return "", fmt.Errorf("group key %q has a dotted component (forbidden — dot is the localpart separator)", groupKey)
	}
	host, zone, ok := strings.Cut(c2sDomain, ".")
	if !ok || host == "" {
		return "", fmt.Errorf("c2s domain %q has no <host>.<zone> shape", c2sDomain)
	}
	roomsDomain := ""
	for _, entry := range strings.Split(roomsEnv, ",") {
		jid, _, _ := strings.Cut(strings.TrimSpace(entry), "=")
		if _, dom, ok := strings.Cut(strings.TrimSpace(jid), "@"); ok && dom != "" {
			roomsDomain = dom
			break
		}
	}
	if roomsDomain == "" {
		if zone == "" {
			return "", fmt.Errorf("no rooms domain — no static room to anchor and no zone in %q", c2sDomain)
		}
		roomsDomain = "rooms." + zone
	}
	return repo + "." + worktree + "@" + roomsDomain, nil
}

// transcriptEntry is the slice of a Claude Code transcript JSONL line the tee
// needs: the entry type, the sidechain/meta markers, and the message content.
type transcriptEntry struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// extractTurnText returns the just-finished turn's user-visible assistant text:
// every text block of every main-line assistant entry after the LAST genuine
// user prompt, joined with blank lines. One forward pass, resetting the
// collection at each prompt boundary, so memory holds at most one turn's text.
// Sidechain (subagent) entries and meta user entries (tool results,
// system-reminder injections) never reset or contribute. Unparseable lines are
// skipped — the transcript is an internal format that may grow entry kinds.
func extractTurnText(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var parts []string
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, readErr := r.ReadBytes('\n')
		if len(line) > 0 {
			var e transcriptEntry
			if json.Unmarshal(line, &e) == nil && !e.IsSidechain {
				switch e.Type {
				case "user":
					if !e.IsMeta && isUserPrompt(e.Message.Content) {
						parts = parts[:0] // a new turn begins: drop the previous turn's text
					}
				case "assistant":
					parts = append(parts, assistantText(e.Message.Content)...)
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n")), nil
}

// isUserPrompt reports whether a user entry's content is a genuine prompt — a
// plain string, or a block array carrying anything other than tool_result
// blocks (text, but also e.g. an image-only paste, which must still start a
// new turn or the next reply would concatenate stale text from the previous
// one) — as opposed to a pure tool_result carrier, which is also recorded as
// type "user" but does not start a new turn.
func isUserPrompt(content json.RawMessage) bool {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return strings.TrimSpace(s) != ""
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			return true
		}
	}
	return false
}

// assistantText returns the non-empty text blocks of an assistant entry's
// content array. Tool-use and thinking blocks are dropped — the tee's scope is
// the user-visible reply text only.
func assistantText(content json.RawMessage) []string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			out = append(out, b.Text)
		}
	}
	return out
}

// teeSubjectBody splits the reply into the muc-send --subject/--body pair: the
// subject is the reply's first non-empty line (capped, it becomes the one-line
// summary a room shows), the body the full verbatim reply (capped rune-safe
// with a truncation marker). --subject/--body is used instead of --message
// because an arbitrary reply does not satisfy the git-commit blank-line rule
// chat.SplitMessage enforces.
func teeSubjectBody(text string) (subject, body string) {
	for _, line := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			subject = s
			break
		}
	}
	if r := []rune(subject); len(r) > teeSubjectMaxRunes {
		subject = string(r[:teeSubjectMaxRunes-1]) + "…"
	}
	body = text
	if len(body) > teeBodyMaxBytes {
		cut := teeBodyMaxBytes
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = body[:cut] + "\n\n[clown-hook-tee: reply truncated]"
	}
	return subject, body
}

// spawnSend launches `troupe muc send` detached — own session, output to the
// debug log or /dev/null — and returns without waiting, so the Stop hook exits
// immediately and an unreachable XMPP server never stalls the turn boundary.
// The child inherits this process's env (the scoped CLOWN_SESSION_ID plus the
// ambient TROUPE_XMPP_* coordinates), which is what resolves the session's own
// nick and minted credential.
func spawnSend(troupeBin, room, subject, body string) error {
	cmd := exec.Command(
		troupeBin, "muc", "send",
		"--room", room,
		"--subject", subject,
		"--body", body,
		"--source", "clown-hook-tee",
	)
	sink, err := debugSink()
	if err != nil {
		return err
	}
	defer sink.Close()
	cmd.Stdout = sink
	cmd.Stderr = sink
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s muc send: %w", troupeBin, err)
	}
	return cmd.Process.Release()
}

// debugSink returns the muc-send child's output destination: the
// CLOWN_HOOK_DEBUG_LOG file when set (matching clown-hook-allow's debug
// mechanism), else /dev/null.
func debugSink() (*os.File, error) {
	if path := os.Getenv("CLOWN_HOOK_DEBUG_LOG"); path != "" {
		return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	}
	return os.OpenFile(os.DevNull, os.O_WRONLY, 0)
}

// debugLog appends a tagged line to $CLOWN_HOOK_DEBUG_LOG when the env var is
// set. The tee is silent by contract, so this is the only failure surface.
func debugLog(tag, payload string) {
	path := os.Getenv("CLOWN_HOOK_DEBUG_LOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[clown-hook-tee %s] %s\n", tag, payload)
}
