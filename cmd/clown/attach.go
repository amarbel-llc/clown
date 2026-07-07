package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
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
// (so the caller proceeds inline); on a successful wrap it runs the multiplexer
// as a child, waits for it, prints the resume hint (outside the mux), and
// os.Exits with the child's code — so like the previous syscall.Exec it never
// returns on success; on a wrap failure it returns the error.
//
// Skip conditions: this is already the inner attached process (attachedID set —
// the loop guard), [attach] is disabled (multiplexer "" / "none"), or — for the
// interactive start/resume modes only — there is no interactive terminal (an
// interactive attach needs a TTY; non-interactive runs inline). ModeSpawn is
// exempt from the TTY gate: it is a detached-worker launch that is always
// non-interactive, so it MUST resolve its template regardless (RFC-0014 §5.1).
// CLOWN_ATTACH_FORCE=1 overrides the TTY check for start/resume — an escape
// hatch for unusual terminals where detection misfires, and the test seam.
// mode is the resolved attach mode (ModeStart / ModeResume); the caller computes
// it from the ORIGINAL forwarded args, before decideClaudeSession injects
// --session-id (which would otherwise misdetect a fresh launch as a resume).
func maybeReexecMultiplexer(cf clownfile.Clownfile, flags parsedFlags, mode clownfile.AttachMode) error {
	if attachedID != "" || !cf.Attach.Enabled() {
		return nil
	}
	// Spawn is a detached-worker launch (RFC-0014 §5): non-interactive by
	// definition (the orchestrator runs clown with /dev/null stdio), so the
	// interactive-TTY gate does NOT apply — clown MUST still resolve the
	// [attach].spawn template (§5.1) so the worker detaches. Start/resume are
	// interactive attaches and keep the gate; CLOWN_ATTACH_FORCE overrides it for
	// unusual terminals where detection misfires, and as the test seam.
	if mode != clownfile.ModeSpawn &&
		!isInteractiveTerminal() && os.Getenv("CLOWN_ATTACH_FORCE") != "1" {
		return nil
	}

	id := flags.identity.Key

	// Replay the RESOLVED invocation inside the multiplexer, with the id pinned
	// via the hidden flag so the inner clown adopts the same routing key (== the
	// mux session name) and skips re-wrapping. The entry is rebuilt from the
	// resolved flags (flags.reexecArgv), NOT os.Args, so that any interactive
	// selection already performed in the OUTER process (the `resume` subcommand's
	// picker/confirm, the profile picker) is baked into the inner argv as an
	// explicit --provider and, for a resume, an injected --resume/--session-id.
	// Those are exactly the inner-picker suppression conditions, so the inner
	// process renders each selection UI zero more times — the wrap covers the
	// resolved provider session, not the pre-launch selection (clown#160).
	// os.Environ() carries no CLOWN_SESSION_ID (clown#136), so the key flows only
	// through the arg.
	bin := clownExePath()
	if bin == "" {
		bin = os.Args[0]
	}
	entry := append([]string{bin, attachIDFlag, id}, flags.reexecArgv()...)

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
		// found-but-unexecutable mux (the runMultiplexer error below) is still fatal.
		return nil
	}

	// Run the multiplexer as a CHILD and wait, rather than syscall.Exec'ing into
	// it. clown surviving the session is what lets the OUTER process emit the
	// resume hint AFTER the mux tears down — printing it from the inner clown
	// lands it inside the mux, where the immediate teardown wipes it before the
	// user can read it. It also turns the outer from a dead-end into a seam for
	// any future post-session work (cleanup, presence dereg).
	code, err := runMultiplexer(muxBin, argv)
	if err != nil {
		return fmt.Errorf("clownfile [attach]: run %s: %w", muxBin, err)
	}
	// Emit the resume hint here, in the outer terminal, now that the mux has torn
	// down and restored the screen. Skipped for spawn (a detached worker with
	// /dev/null stdio) and when there is no id to print. The inner clown suppresses
	// its own print (attachedID != "" in runClaude), so this is the only emitter.
	if mode != clownfile.ModeSpawn && flags.resumeHintID != "" {
		printResumeHint(flags.resumeHintID)
	}
	os.Exit(code)
	return nil // unreachable on success
}

// runMultiplexer runs the resolved multiplexer argv as a child process, inherits
// clown's stdio, and waits for it to exit, returning the child's exit code. It is
// the run-as-child alternative to syscall.Exec: clown stays alive across the
// session so maybeReexecMultiplexer can print the resume hint OUTSIDE the mux
// once the child is gone.
//
// argv[0] is the resolved binary name (already looked up by the caller); the
// child is launched with argv[1:] as its args. Termination signals (SIGTERM /
// SIGINT) are forwarded to the child, mirroring runProvider. A raw-mode TUI (posh
// / claude) reads keyboard Ctrl-C as a byte rather than a signal, so those never
// reach here — only an external kill does, making forwarding the right response
// with no double-delivery. SIGWINCH is intentionally left alone: its default
// disposition is ignore, and the kernel already delivers it to the child as the
// terminal's foreground process group, which repaints itself.
func runMultiplexer(muxBin string, argv []string) (int, error) {
	cmd := exec.Command(muxBin, argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	// Stop notifications and close the channel on return so the forwarding
	// goroutine exits (matters for the unit test, which does not os.Exit after).
	defer func() {
		signal.Stop(sigCh)
		close(sigCh)
	}()

	if err := cmd.Start(); err != nil {
		return 1, err
	}
	go func() {
		for sig := range sigCh {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(sig)
			}
		}
	}()

	err := cmd.Wait()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

// reexecArgv reconstructs a canonical clown argv from the RESOLVED flags, for the
// [attach] multiplexer re-exec's {entry} (clown#160). It is deliberately NOT
// os.Args: replaying the raw outer argv would replay a `resume` subcommand or a
// picker-less bare `clown`, so the inner process would re-run the selection UI
// the outer already ran. Emitting the resolved flags instead — an explicit
// --provider, and (via flags.forwarded) the --resume/--session-id injected by
// decideClaudeSession — makes the inner process take the non-interactive branch
// of every selector.
//
// Only user/selection-derived top-level flags are emitted. Runtime-derived
// fields with no flag source (backend, ptyOpts, identity, groupID, resumeHintID)
// are intentionally omitted: the inner clown re-derives them deterministically
// from the clownfile in runWithFlags. version/help never reach the wrap (they
// early-return in run), so they are not emitted either.
func (p parsedFlags) reexecArgv() []string {
	var argv []string
	// Provider: always explicit on the inner so the profile picker is suppressed.
	// After selection the provider is resolved even when the user did not type
	// --provider (a profile pick sets it), so emit it unconditionally.
	if p.provider != "" {
		argv = append(argv, "--provider", p.provider)
	}
	if p.profile != "" {
		argv = append(argv, "--profile", p.profile)
	}
	if p.naked {
		argv = append(argv, "--naked")
	}
	if p.skipFailed {
		argv = append(argv, "--skip-failed")
	}
	if p.cheapContext {
		argv = append(argv, "--cheap-context")
	}
	if p.disableClownProtocol {
		argv = append(argv, "--disable-clown-protocol")
	}
	if p.tent {
		argv = append(argv, "--tent")
	}
	if p.passDevshell {
		argv = append(argv, "--tent-pass-devshell")
	}
	if p.noPassDevshell {
		argv = append(argv, "--no-tent-pass-devshell")
	}
	if p.verbose {
		argv = append(argv, "--verbose")
	}
	for _, d := range p.extraPluginDirs {
		argv = append(argv, "--plugin-dir", d)
	}
	// Forwarded provider args last, behind the -- separator. This carries the
	// --resume/--session-id decideClaudeSession injected, which selects the
	// resume attach template on the inner and suppresses the resume picker.
	if len(p.forwarded) > 0 {
		argv = append(argv, "--")
		argv = append(argv, p.forwarded...)
	}
	return argv
}
