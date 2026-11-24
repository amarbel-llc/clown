package agentscope

import (
	"fmt"
	"strings"
)

// RedactSetenv returns argv with the value of every environment assignment
// removed, so a log line names what a unit gets without its values. The start
// log of every launch goes through it: the full unit passes ANTHROPIC_ and
// CLAUDE_CODE_ credentials when the model proxy is off, and a view session
// carries the operator's own environment (a token in NIX_CONFIG once reached a
// job log).
//
// It covers systemd-run's three spellings of an assignment, none of which may
// reopen the leak when a later change picks another:
//
//	--setenv=K=V   -E=K=V   (one element)
//	--setenv K=V   -E K=V   (two elements: the value is the next element)
//	--property=Environment=K=V [K2=V2 ...]   (the whole list is values)
//
// A result keeps the keys and replaces each value with <redacted>. The input
// slice is not modified.
func RedactSetenv(argv []string) []string {
	out := make([]string, len(argv))
	copy(out, argv)
	for i := 0; i < len(out); i++ {
		a := out[i]
		switch {
		case a == "--setenv" || a == "-E":
			if i+1 < len(out) {
				out[i+1] = redactAssignment(out[i+1])
				i++
			}
		case strings.HasPrefix(a, "--setenv="):
			out[i] = "--setenv=" + redactAssignment(strings.TrimPrefix(a, "--setenv="))
		case strings.HasPrefix(a, "-E="):
			out[i] = "-E=" + redactAssignment(strings.TrimPrefix(a, "-E="))
		case strings.HasPrefix(a, "--property=Environment="):
			out[i] = "--property=Environment=<redacted>"
		case a == "--property" || a == "-p":
			if i+1 < len(out) && strings.HasPrefix(out[i+1], "Environment=") {
				out[i+1] = "Environment=<redacted>"
				i++
			}
		}
	}
	return out
}

// redactAssignment turns K=V into K=<redacted> (the key may not hold "=").
func redactAssignment(kv string) string {
	k, _, _ := strings.Cut(kv, "=")
	return k + "=<redacted>"
}

// LoggedArgv is what the launcher logs of a systemd-run command line: the
// environment values redacted, and the provider's own tail (the last
// providerArgs elements, claude's --settings among them) reduced to its
// binary and a count, since it can carry settings and prompts.
func LoggedArgv(argv []string, providerArgs int) []string {
	if providerArgs <= 0 || providerArgs > len(argv) {
		return RedactSetenv(argv)
	}
	head := RedactSetenv(argv[:len(argv)-providerArgs])
	prov := argv[len(argv)-providerArgs:]
	return append(head, prov[0], fmt.Sprintf("<%d more args>", len(prov)-1))
}
