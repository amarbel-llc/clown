package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/amarbel-llc/clown/internal/jobwake"
)

// runChat dispatches `clown chat <send|read>` (RFC-0013 §3): the clown-owned
// chat surface that replaces spinclass's chat-send/chat-read. chat-list arrives
// with the presence index (P3b).
func runChat(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "clown chat: expected a subcommand (send|read)")
		return 2
	}
	switch args[0] {
	case "send":
		return chatSend(args[1:])
	case "read":
		return chatRead(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "clown chat: unknown subcommand %q\n", args[0])
		return 2
	}
}

// chatSend emits a chat message: a one-line subject (the wake) plus an optional
// full body (stored in the spool for chat read). target is a session key, a
// SPINCLASS_SESSION_ID group, or "*" for broadcast.
func chatSend(args []string) int {
	fs := flag.NewFlagSet("clown chat send", flag.ContinueOnError)
	target := fs.String("target", "", "recipient: a session key, a SPINCLASS_SESSION_ID group, or * for broadcast")
	from := fs.String("from", "", "sender session key (default: this session)")
	source := fs.String("source", "", "emitting source label")
	subject := fs.String("subject", "", "one-line subject (the wake notification)")
	body := fs.String("body", "", "full message body (recovered by chat read)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *target == "" {
		fmt.Fprintln(os.Stderr, "clown chat send: --target is required")
		return 2
	}
	if *subject == "" {
		fmt.Fprintln(os.Stderr, "clown chat send: --subject is required")
		return 2
	}
	from2 := *from
	if from2 == "" {
		from2 = jobwake.SessionKey()
	}
	id, err := jobwake.SendChat(*target, from2, *source, *subject, *body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown chat send: %v\n", err)
		return 1
	}
	fmt.Println(id)
	return 0
}

// chatRead returns chat messages addressed to this session (its own, group, and
// broadcast channels) newer than the read cursor, advancing the cursor unless
// --peek. --json emits one JSON object per message.
func chatRead(args []string) int {
	fs := flag.NewFlagSet("clown chat read", flag.ContinueOnError)
	peek := fs.Bool("peek", false, "do not advance the read cursor")
	asJSON := fs.Bool("json", false, "emit one JSON object per message")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	msgs, err := jobwake.ReadChat(*peek)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown chat read: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, m := range msgs {
			if err := enc.Encode(m); err != nil {
				fmt.Fprintf(os.Stderr, "clown chat read: %v\n", err)
				return 1
			}
		}
		return 0
	}
	for _, m := range msgs {
		fmt.Printf("%s [%s]: %s\n", chatSender(m), m.Scope, m.Subject)
		if m.Body != "" {
			fmt.Println(m.Body)
		}
	}
	return 0
}

// chatSender renders the displayable sender of a chat message: the explicit
// `from` session key when present, else the source label. Readable-name
// enrichment via the presence index lands with P3b.
func chatSender(m jobwake.ChatMessage) string {
	if m.From != "" {
		return m.From
	}
	return m.Source
}
