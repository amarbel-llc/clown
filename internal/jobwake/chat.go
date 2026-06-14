package jobwake

import (
	"os"
	"path/filepath"
	"sort"
)

// ChatMessage is a chat message returned by ReadChat: the record metadata plus
// the full body recovered from the message's spool (RFC-0013 §3).
type ChatMessage struct {
	Job       string     `json:"job"`
	From      string     `json:"from,omitempty"`
	Source    string     `json:"source"`
	Scope     string     `json:"scope"` // "direct" | "group" | "broadcast"
	Subject   string     `json:"subject,omitempty"`
	Body      string     `json:"body,omitempty"`
	Resources []Resource `json:"resources,omitempty"` // by-reference attachments (clown#112)
	TS        string     `json:"ts"`
}

// chatCursorPath is the per-reader chat read cursor for a channel — DISTINCT
// from the monitor's wake ack (.ack.json / .ack-<reader>.json) so a pull read
// never consumes a wake or vice-versa (RFC-0013 §3.1). The `.chat-cursor-`
// prefix keeps it inside scanWaking's dotfile skip, so it is never parsed as a
// job journal.
func chatCursorPath(channelID, readerID string) string {
	return filepath.Join(JournalDir(channelID), ".chat-cursor-"+readerID+".json")
}

// ReadChat returns chat messages newer than the reader's per-channel cursor
// across the reader's own, group, and broadcast channels (RFC-0013 §3.2), each
// body recovered from its spool, oldest first. Unless peek, it advances the
// per-channel cursors after reading. Unlike the wake monitor it INCLUDES the
// reader's own sent messages (this is conversation history, not a wake). The
// reader is the current session (SessionKey()).
func ReadChat(peek bool) ([]ChatMessage, error) {
	readerCID := ChannelID(SessionKey())
	var out []ChatMessage
	seen := map[string]bool{} // a channel that coincides with another is read once
	read := func(cid, scope string) error {
		if cid == "" || seen[cid] {
			return nil
		}
		seen[cid] = true
		msgs, err := readChatChannel(cid, readerCID, scope, peek)
		if err != nil {
			return err
		}
		out = append(out, msgs...)
		return nil
	}
	if err := read(readerCID, "direct"); err != nil {
		return nil, err
	}
	if err := read(GroupChannel(), "group"); err != nil {
		return nil, err
	}
	if err := read(ChannelID(BroadcastKey), "broadcast"); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}

// readChatChannel returns the unread TypeChat messages on one channel for the
// reader, advancing the reader's chat cursor (unless peek) after reading. Each
// body is recovered from the message's spool; a missing spool yields an empty
// body rather than an error (at-least-once, RFC-0009 §10).
func readChatChannel(cid, readerCID, scope string, peek bool) ([]ChatMessage, error) {
	cursorPath := chatCursorPath(cid, readerCID)
	a := loadAckPath(cursorPath)
	waking, err := scanWaking(cid)
	if err != nil {
		return nil, err
	}
	var out []ChatMessage
	advanced := false
	for _, r := range waking {
		if r.Type != TypeChat {
			continue
		}
		if prev, ok := a.Acked[r.Job]; ok && r.Seq <= prev {
			continue
		}
		body := ""
		if b, rerr := os.ReadFile(SpoolFile(cid, r.Job)); rerr == nil {
			body = string(b)
		}
		out = append(out, ChatMessage{Job: r.Job, From: r.From, Source: r.Source,
			Scope: scope, Subject: r.Message, Body: body, Resources: r.Resources, TS: r.TS})
		a.Acked[r.Job] = r.Seq
		advanced = true
	}
	if !peek && advanced {
		if err := saveAckPath(cursorPath, a); err != nil {
			return nil, err
		}
	}
	return out, nil
}
