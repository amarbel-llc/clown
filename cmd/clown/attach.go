package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/amarbel-llc/clown/internal/clownfile"
)

// attachIDFlag is the hidden clown flag that pins the per-instance id across the
// [attach] multiplexer re-exec (RFC-0013 §1.3 rule 3). The outer clown bakes it
// into the inner invocation; its presence on the inner is the loop guard (the
// inner does NOT re-wrap) and its value is the inner's routing key. It is an
// ARG, not an env var, so the key never enters the ambient environment the
// provider subtree would inherit (reuses the clown#136 threading).
const attachIDFlag = "--clown-attach-id"

// attachModeFlag selects the [attach] mode for a fresh launch (RFC-0014 §5).
// Only "spawn" (a detached-worker launch) is acted on today — start/resume are
// auto-detected from the forwarded args — but any value is stripped so it never
// reaches a downstream parser. The orchestrator (e.g. `sc spawn`) passes
// --clown-attach=spawn.
const attachModeFlag = "--clown-attach"

// attachSpawn records --clown-attach=spawn from the outer invocation: the
// detached-worker launch signal (RFC-0014 §5). Like attachedID it is captured
// and stripped in run() before any flag parsing.
var attachSpawn bool

// attachedID is the pinned per-instance id when this clown is the inner process
// of an [attach] re-exec (set from attachIDFlag by extractAttachID in run()).
// Empty means this is the outer/un-attached process. Package-level because the
// value is consumed across run()'s several dispatch paths (the main provider
// path and the resume subcommand path both reach runWithFlags).
var attachedID string

// extractAttachID removes the attachIDFlag (and its value) from args and returns
// the captured id plus the cleaned args. Supports both "--clown-attach-id v" and
// "--clown-attach-id=v". Stripping it before subcommand dispatch / parseFlags
// keeps every downstream arg parser from choking on the internal flag.
func extractAttachID(args []string) (id string, rest []string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == attachIDFlag {
			if i+1 < len(args) {
				id = args[i+1]
				i++ // consume the value
			}
			continue
		}
		if v, ok := stringsCutPrefix(a, attachIDFlag+"="); ok {
			id = v
			continue
		}
		rest = append(rest, a)
	}
	return id, rest
}

// extractAttachSpawn removes the attachModeFlag (and its value) from args and
// reports whether spawn mode was selected (RFC-0014 §5). Supports both
// "--clown-attach spawn" and "--clown-attach=spawn". It must run AFTER
// extractAttachID so the longer "--clown-attach-id" is already gone; it matches
// only the exact "--clown-attach" token or "--clown-attach=" prefix, so it would
// not swallow "--clown-attach-id" regardless of order.
func extractAttachSpawn(args []string) (spawn bool, rest []string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == attachModeFlag {
			if i+1 < len(args) {
				spawn = spawn || args[i+1] == "spawn"
				i++ // consume the value
			}
			continue
		}
		if v, ok := stringsCutPrefix(a, attachModeFlag+"="); ok {
			spawn = spawn || v == "spawn"
			continue
		}
		rest = append(rest, a)
	}
	return spawn, rest
}

// stringsCutPrefix is strings.CutPrefix (Go 1.20+) inlined to avoid widening
// imports here; returns the remainder and whether the prefix matched.
func stringsCutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return "", false
}

// maybeReexecMultiplexer wraps clown in the configured multiplexer per the
// clownfile [attach] table (RFC-0013 §1.3). It returns nil when no wrap applies
// (so the caller proceeds inline); on a successful wrap it syscall.Execs the
// multiplexer and never returns; on a wrap failure it returns the error.
//
// Skip conditions: this is already the inner attached process (attachedID set —
// the loop guard), [attach] is disabled (multiplexer "" / "none"), or there is
// no interactive terminal (a multiplexer needs a TTY; non-interactive runs
// inline). CLOWN_ATTACH_FORCE=1 overrides the TTY check — an escape hatch for
// unusual terminals where detection misfires, and the test seam.
// mode is the resolved attach mode (ModeStart / ModeResume); the caller computes
// it from the ORIGINAL forwarded args, before decideClaudeSession injects
// --session-id (which would otherwise misdetect a fresh launch as a resume).
func maybeReexecMultiplexer(cf clownfile.Clownfile, flags parsedFlags, mode clownfile.AttachMode) error {
	if attachedID != "" || !cf.Attach.Enabled() {
		return nil
	}
	if !isInteractiveTerminal() && os.Getenv("CLOWN_ATTACH_FORCE") != "1" {
		return nil
	}

	id := flags.identity.Key

	// Replay this exact invocation inside the multiplexer, with the id pinned via
	// the hidden flag so the inner clown adopts the same routing key (== the mux
	// session name) and skips re-wrapping. os.Args is the OUTER argv (no attach
	// flag yet); os.Environ() carries no CLOWN_SESSION_ID (clown#136), so the key
	// flows only through the arg.
	bin := clownExePath()
	if bin == "" {
		bin = os.Args[0]
	}
	entry := append([]string{bin, attachIDFlag, id}, os.Args[1:]...)

	argv, err := cf.Attach.Resolve(mode, id, entry)
	if err != nil {
		return err
	}

	if mode == clownfile.ModeResume {
		if title := cf.Attach.Title(id, flags.groupID); title != "" {
			// OSC-2 window title; best-effort, before handing the terminal to the mux.
			fmt.Fprintf(os.Stderr, "\033]2;%s\007", title)
		}
	}

	muxBin, err := exec.LookPath(argv[0])
	if err != nil {
		// The configured multiplexer is not installed. Since [attach] ships as a
		// burned-in default (RFC-0013 §1.3), an absent mux must NOT break clown on
		// hosts without it — degrade to running inline rather than failing. A
		// found-but-unexecutable mux (the syscall.Exec error below) is still fatal.
		return nil
	}
	if err := syscall.Exec(muxBin, argv, os.Environ()); err != nil {
		return fmt.Errorf("clownfile [attach]: exec %s: %w", muxBin, err)
	}
	return nil // unreachable on success
}
