package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"code.linenisgreat.com/clown/internal/alfa/profilerules"
)

// runCapabilityProfile dispatches `clown capability-profile`, the authoring
// side of RFC 0019: the rules a capability profile must pass before anyone
// blesses it.
func runCapabilityProfile(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "clown capability-profile: expected a subcommand (check)")
		return 2
	}
	switch args[0] {
	case "check":
		return capabilityProfileCheck(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "clown capability-profile: unknown subcommand %q\n", args[0])
		return 2
	}
}

// capabilityProfileCheck prints every RFC 0019 finding for one file, one per
// line as "<severity> <node>: <message>" (the message starts with its rule
// id), and exits 1 when any finding refuses, 0 otherwise. --patch checks the
// file as an act's body (ValidatePatch) instead of a profile.
func capabilityProfileCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("clown capability-profile check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	patch := fs.Bool("patch", false, "check the file as an act's patch body, not a profile")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: clown capability-profile check [--patch] FILE")
		return 2
	}
	data, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "clown capability-profile check: %v\n", err)
		return 2
	}
	var findings []profilerules.Finding
	if *patch {
		findings = profilerules.ValidatePatch(string(data))
	} else {
		findings = profilerules.Validate(string(data))
	}
	code := 0
	for _, f := range findings {
		node := f.Node
		if node == "" {
			node = "-"
		}
		fmt.Fprintf(stdout, "%s %s: %s\n", f.Severity, node, f.Message)
		if f.Severity == profilerules.Refuse {
			code = 1
		}
	}
	return code
}
