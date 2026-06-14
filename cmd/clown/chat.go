package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/amarbel-llc/clown/internal/jobwake"
)

// runChat dispatches `clown chat <send|read|list>` (RFC-0013 §3): the
// clown-owned chat surface that replaces spinclass's chat-send / chat-read /
// chat-list-sessions.
func runChat(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "clown chat: expected a subcommand (send|read|list)")
		return 2
	}
	switch args[0] {
	case "send":
		return chatSend(args[1:])
	case "read":
		return chatRead(args[1:])
	case "list":
		return chatList(args[1:])
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
	var resources stringList
	fs.Var(&resources, "resource", "attach a resource by URI, e.g. madder://blobs/<digest> (repeatable)")
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
	id, err := jobwake.SendChat(*target, from2, *source, *subject, *body, resourcesFromURIs(resources)...)
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
	names := presenceNames()
	for _, m := range msgs {
		fmt.Printf("%s [%s]: %s\n", displaySender(m, names), m.Scope, m.Subject)
		if m.Body != "" {
			fmt.Println(m.Body)
		}
		for _, r := range m.Resources {
			fmt.Printf("  resource: %s\n", r.URI)
		}
	}
	return 0
}

// chatList prints the live chat recipients, grouped by their spinclass session
// (the decoration). It replaces spinclass chat-list-sessions. --json emits one
// JSON presence record per line.
func chatList(args []string) int {
	fs := flag.NewFlagSet("clown chat list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit one JSON object per presence record")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ps, err := jobwake.ListPresence(time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown chat list: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, p := range ps {
			if err := enc.Encode(p); err != nil {
				fmt.Fprintf(os.Stderr, "clown chat list: %v\n", err)
				return 1
			}
		}
		return 0
	}
	groups := map[string][]jobwake.Presence{}
	var order []string
	for _, p := range ps {
		if _, ok := groups[p.Decoration]; !ok {
			order = append(order, p.Decoration)
		}
		groups[p.Decoration] = append(groups[p.Decoration], p)
	}
	sort.Strings(order)
	for _, g := range order {
		label := g
		if label == "" {
			label = "(no session)"
		}
		fmt.Println(label)
		for _, p := range groups[g] {
			desc := p.Description
			if desc == "" {
				desc = "-"
			}
			fmt.Printf("  %s  %s\n", p.SessionKey, desc)
		}
	}
	return 0
}

// presenceNames maps per-instance session keys to their readable description
// (SPINCLASS_DESCRIPTION) from the presence index, for sender enrichment.
func presenceNames() map[string]string {
	m := map[string]string{}
	ps, err := jobwake.ListPresence(time.Now())
	if err != nil {
		return m
	}
	for _, p := range ps {
		if p.Description != "" {
			m[p.SessionKey] = p.Description
		}
	}
	return m
}

// displaySender renders the sender of a chat message: the presence description
// (with the key) when known, else the raw `from` key, else the source label.
func displaySender(m jobwake.ChatMessage, names map[string]string) string {
	if m.From == "" {
		return m.Source
	}
	if n, ok := names[m.From]; ok {
		return n + " (" + m.From + ")"
	}
	return m.From
}

// resourcesFromURIs builds resource attachments from bare URIs (the CLI form;
// the MCP surface carries the richer {uri,digest,mediaType,size} objects). Used
// by chat send and job message/done (clown#112).
func resourcesFromURIs(uris []string) []jobwake.Resource {
	if len(uris) == 0 {
		return nil
	}
	out := make([]jobwake.Resource, 0, len(uris))
	for _, u := range uris {
		out = append(out, jobwake.Resource{URI: u})
	}
	return out
}
