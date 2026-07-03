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

// jobWakeupDisabled reports whether the job-wakeup facility is switched off via
// CLOWN_DISABLE_JOB_WAKEUP=1 (RFC-0009 §8). Gates the emit-only `message` verb.
func jobWakeupDisabled() bool {
	return os.Getenv("CLOWN_DISABLE_JOB_WAKEUP") == "1"
}

// troupeSend emits a chat message: a one-line subject (the wake) plus an
// optional full body (stored in the spool for troupe read). target is a session
// key, a group-id group, or "*" for broadcast. The message may be given either
// as a single --message in git-commit format (summary line, blank line, body —
// troupe splits it) or as the explicit --subject/--body pair.
func troupeSend(args []string) int {
	fs := flag.NewFlagSet("troupe send", flag.ContinueOnError)
	target := fs.String("target", "", "recipient: a session key, a group-id group, or * for broadcast")
	from := fs.String("from", "", "sender session key (default: this session)")
	source := fs.String("source", "", "emitting source label")
	message := fs.String("message", "", "git-commit format: a one-line summary, a blank line, then the body")
	subject := fs.String("subject", "", "one-line subject (the wake notification); alternative to --message")
	body := fs.String("body", "", "full message body (recovered by troupe read); pairs with --subject")
	var resources stringList
	fs.Var(&resources, "resource", "attach a resource by URI, e.g. madder://blobs/<digest> (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *target == "" {
		fmt.Fprintln(os.Stderr, "troupe send: --target is required")
		return 2
	}
	subj, bdy := *subject, *body
	switch {
	case *message != "":
		if *subject != "" || *body != "" {
			fmt.Fprintln(os.Stderr, "troupe send: --message is mutually exclusive with --subject/--body")
			return 2
		}
		var err error
		if subj, bdy, err = jobwake.SplitMessage(*message); err != nil {
			fmt.Fprintf(os.Stderr, "troupe send: %v\n", err)
			return 2
		}
	case *subject == "":
		fmt.Fprintln(os.Stderr, "troupe send: --message or --subject is required")
		return 2
	}
	from2 := *from
	if from2 == "" {
		from2 = jobwake.SessionKey()
	}
	id, err := jobwake.SendChat(*target, from2, *source, subj, bdy, jobwake.ResourcesFromURIs(resources)...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "troupe send: %v\n", err)
		return 1
	}
	fmt.Println(id)
	return 0
}

// troupeRead returns chat messages addressed to this session (its own, group,
// and broadcast channels) newer than the read cursor, advancing the cursor
// unless --peek. --json emits one JSON object per message.
func troupeRead(args []string) int {
	fs := flag.NewFlagSet("troupe read", flag.ContinueOnError)
	peek := fs.Bool("peek", false, "do not advance the read cursor")
	asJSON := fs.Bool("json", false, "emit one JSON object per message")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	msgs, err := jobwake.ReadChat(*peek)
	if err != nil {
		fmt.Fprintf(os.Stderr, "troupe read: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, m := range msgs {
			if err := enc.Encode(m); err != nil {
				fmt.Fprintf(os.Stderr, "troupe read: %v\n", err)
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

// troupeList prints the live chat recipients, grouped by their spinclass session
// (the decoration). --json emits one JSON presence record per line.
func troupeList(args []string) int {
	fs := flag.NewFlagSet("troupe list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit one JSON object per presence record")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ps, err := jobwake.ListPresence(time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "troupe list: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, p := range ps {
			if err := enc.Encode(p); err != nil {
				fmt.Fprintf(os.Stderr, "troupe list: %v\n", err)
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

// troupeMessage emits a standalone single-record waking `message` job (RFC-0009
// §4, §8; promoted from `clown job message`, RFC-0015 §3). --target is required
// and may be the reserved broadcast key '*'; --message is required and must be
// non-empty (usage error exit 2 otherwise). --from is the optional sender
// session key rendered in the notification line. Prints the generated job id.
func troupeMessage(args []string) int {
	fs := flag.NewFlagSet("troupe message", flag.ContinueOnError)
	target := fs.String("target", "", "target session key, or '*' for broadcast")
	from := fs.String("from", "", "sender session key")
	source := fs.String("source", "", "emitting plugin label")
	message := fs.String("message", "", "message body")
	resultRef := fs.String("result-ref", "", "opaque result pointer")
	var resources stringList
	fs.Var(&resources, "resource", "attach a resource by URI, e.g. madder://blobs/<digest> (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *target == "" {
		fmt.Fprintln(os.Stderr, "troupe message: --target is required (a session key or '*')")
		return 2
	}
	if *message == "" {
		fmt.Fprintln(os.Stderr, "troupe message: --message is required and must be non-empty")
		return 2
	}
	id, err := jobwake.Message(*target, *source, *from, *message, *resultRef, jobwake.ResourcesFromURIs(resources)...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "troupe message: %v\n", err)
		return 1
	}
	fmt.Println(id)
	return 0
}

// presenceNames maps per-instance session keys to their readable description
// (CLOWN_GROUP_DESCRIPTION) from the presence index, for sender enrichment.
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

// stringList is a repeatable string flag (e.g. --resource A --resource B).
type stringList []string

func (s *stringList) String() string { return fmt.Sprint([]string(*s)) }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}
