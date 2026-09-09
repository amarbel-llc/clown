package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"code.linenisgreat.com/clown/internal/clownfile"
	"code.linenisgreat.com/ringmaster/pkgs/jobwake"
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

// titleTerminalAvailable reports whether this session has a terminal to title at
// all. It is deliberately the SAME condition the [attach] wrap uses, down to the
// CLOWN_ATTACH_FORCE=1 override (the escape hatch for terminals where detection
// misfires, and the seam the title tests drive), so a session that wraps is a
// session that titles.
//
// Note it asks about stdin/stdout while the write below goes to stderr, so a
// redirected stderr under an otherwise interactive terminal still receives the
// escape bytes. That mismatch predates the emission-point move and is left
// as-is here rather than quietly diverging this gate from the wrap's.
func titleTerminalAvailable() bool {
	return isInteractiveTerminal() || os.Getenv("CLOWN_ATTACH_FORCE") == "1"
}

// gitRepoAndBranch best-effort resolves "<repo-basename>/<branch>" for the
// current working directory, for OSC-2 TITLE DISPLAY ONLY (clown#180): it is
// never fed into flags.groupID / CLOWN_GROUP_ID / presence Decoration, so it
// cannot silently group two unrelated bare clowns that happen to share a repo.
// Returns "" on any failure (not a git repo, git missing). A detached HEAD (no
// current branch) yields just "<repo-basename>". Failing silent mirrors
// internal/clownname.Claim's never-fail-the-launch contract for cosmetic data.
func gitRepoAndBranch() string {
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	repo := filepath.Base(strings.TrimSpace(string(top)))
	if repo == "" || repo == "." || repo == string(filepath.Separator) {
		return ""
	}
	// --show-current is empty on a detached HEAD; that is fine — we degrade to
	// just the repo name rather than failing.
	branch, err := exec.Command("git", "branch", "--show-current").Output()
	if err != nil {
		return repo
	}
	if b := strings.TrimSpace(string(branch)); b != "" {
		return repo + "/" + b
	}
	return repo
}

// titleDisambiguationNeeded reports whether the clown-name ({id}) should appear
// in the OSC-2 title of a session whose title group came from the git fallback —
// true when 2+ live clown sessions share cwd, so the name distinguishes them
// (clown#180, FDR-0015). It keys on the Cwd presence field because the
// git-derived group is never written to Decoration. This is now the ONLY title
// tier that dedups: a real spinclass group always shows the name (clown#230, see
// emitSessionTitle). Best-effort — a presence-read failure degrades to true (show
// the id rather than silently hide information on a read failure).
func titleDisambiguationNeeded(cwd string) bool {
	ps, err := jobwake.ListPresence(time.Now())
	if err != nil {
		return true
	}
	count := 0
	for _, p := range ps {
		if p.Cwd == cwd {
			count++
		}
	}
	return count >= 2
}

// maybeReexecMultiplexer wraps clown in the configured multiplexer per the
// clownfile [attach] table (RFC-0013 §1.3). It returns nil when no wrap applies
// (so the caller proceeds inline); on a successful wrap it runs the multiplexer
// as a child, waits for it, prints the resume hint (outside the mux), and
// os.Exits with the child's code — so like the previous syscall.Exec it never
// returns on success; on a wrap failure it returns the error.
//
// Skip conditions: this is already the inner attached process (attachedID set —
// the loop guard), [attach] is disabled (multiplexer "" / "none"),
// --print-launch-plan is set (below), or — for the
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
	// --print-launch-plan runs inline, always. Two reasons, either sufficient:
	//
	// The wrap re-execs clown inside the multiplexer, and reexecArgv() emits only
	// user/selection-derived flags — so the inner clown would NOT carry
	// --print-launch-plan and would spawn the provider for real. That is the exact
	// outcome the flag's contract rules out, and it fires on the shipped [attach]
	// default whenever stdin and stdout are both ttys (which is why piping the
	// plan into a tool masks it).
	//
	// Even with the flag threaded through, wrapping would be wrong: the plan would
	// be written to the multiplexer's screen rather than to clown's stdout, so
	// nothing could capture it. A machine-readable dump has no business inside a
	// terminal multiplexer.
	if flags.printLaunchPlan {
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

// emitSessionTitle writes this session's OSC-2 window title (RFC-0014 §3.1.1).
//
// It MUST be called AFTER maybeReexecMultiplexer, because that is precisely what
// makes the emitting process the one that owns the terminal being titled: a
// successful wrap never returns (the outer os.Exits into the multiplexer), so the
// only process that reaches here is either the INNER clown running inside the
// multiplexer's pty or an un-wrapped clown running inline.
//
// Emitting from the inner process is the point (clown#231, clown#232). The escape
// sequence lands in the multiplexer's pty, so the mux daemon's terminal model
// holds the title and re-asserts it on attach, session switch, and repaint. The
// previous pre-exec write from the outer process set the OUTER terminal's title
// only, leaving the mux session itself titleless — which is why switching into a
// clown session left the previous session's title on screen, and why the daemon's
// activity view showed no title for any clown session.
//
// It is also why this is NOT gated on the attach mode. The old gate excluded
// ModeSpawn as "a detached-worker launch with no terminal to title", but a spawn
// creates a durable session a human attaches to interactively later — there is a
// terminal, it just arrives after the launch (clown#232). Since a spawn's inner
// clown reaches here with a real pty and its outer never does (no TTY, see
// titleTerminalAvailable), the terminal check alone expresses the intent exactly,
// and every launch mode gets a title.
func emitSessionTitle(cf clownfile.Clownfile, flags parsedFlags) {
	if flags.printLaunchPlan || !titleTerminalAvailable() {
		return
	}

	// {id} in the title prefers the human-ergonomic clown-name (clown#169) over
	// the raw per-instance UUID — readability is the entire point of naming
	// sessions. The mux session name and routing key (flags.identity.Key, used
	// for Resolve/attachIDFlag) are UNAFFECTED: this substitution is
	// display-only. flags.clownName is normally non-empty (Claim never returns
	// ""), but is deliberately left unset for --naked (runWithFlags skips the
	// Claim call there, since a naked launch's monitor/presence path never
	// consumes it) — the fallback covers that case with pre-clown#169 behavior.
	titleID := flags.clownName
	if titleID == "" {
		titleID = flags.identity.Key
	}

	// {group} resolves through a three-tier cascade (clown#180, FDR-0015):
	//   1. the spinclass group-id (flags.groupID) when non-empty;
	//   2. else a best-effort git "<repo>/<branch>" of the cwd, so a bare
	//      clown outside spinclass still shows repo context. This is
	//      TITLE-DISPLAY ONLY — it is never written to flags.groupID /
	//      CLOWN_GROUP_ID / presence Decoration, so two unrelated bare
	//      clowns in one repo do NOT become chat/presence-grouped;
	//   3. else "" (not spinclass, not a git repo).
	titleGroup := flags.groupID
	usingGitFallback := false
	if titleGroup == "" {
		if g := gitRepoAndBranch(); g != "" {
			titleGroup = g
			usingGitFallback = true
		}
	}

	// Tier 1 (a real spinclass group) always shows the clown-name ({id}). It used
	// to dedup on the presence Decoration, but spinclass creates exactly one clown
	// per worktree, so the Decoration scope is 1:1 with a clown BY CONSTRUCTION:
	// the count was always 1 and the clown-name was dropped from every fleet
	// session's title (clown#230). That reverses FDR-0015's dedup-threshold lever,
	// whose own change signal ("users want the id shown even solo") has fired: the
	// title is the surface on which sessions are identified, and bare clown-names
	// collide across concurrent sessions, so the fully-qualified
	// sc/<repo>/<session>/<clown> form must always be complete.
	//
	// Tier 3 (no group at all) suppresses the SEPARATE {id} — not because the
	// clown-name is unwanted there, but because Title's own empty-group fallback
	// already substitutes it for {group}. Forcing {id} too rendered the default
	// sc/{group}/{id} as sc/bozo/bozo (clown#229); dropping it leaves the name
	// exactly once, and a custom template using only {group} still carries it.
	//
	// That reasoning holds only when the template HAS a {group} to fall back —
	// with a {group}-less template (e.g. a bare "{id}") nothing else renders the
	// name, and suppressing {id} would empty the title and emit nothing at all. So
	// the suppression is conditioned on the template actually containing {group},
	// which is the only thing that makes the id redundant here.
	//
	// Tier 2 keeps the dedup: its scope is a working directory, which really can
	// hold several unrelated clowns or exactly one, so the count carries
	// information there. A Getwd failure degrades to always-show-id.
	showID := true
	if titleGroup == "" {
		showID = !strings.Contains(cf.Attach.ResumeTitle, "{group}")
	} else if usingGitFallback {
		if cwd, err := os.Getwd(); err == nil {
			showID = titleDisambiguationNeeded(cwd)
		}
	}

	if title := cf.Attach.Title(titleID, titleGroup, showID); title != "" {
		fmt.Fprintf(os.Stderr, "\033]2;%s\007", title)
	}
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
// poshEnv returns env with POSH_DIR appended when muxBin is posh and the
// caller hasn't already set POSH_DIR themselves. posh resolves its UNIX
// socket base as POSH_DIR > XDG_RUNTIME_DIR/posh > $TMPDIR/posh-<uid> >
// /tmp/posh-<uid>, then builds <base>/<group>/<session-name> and rejects the
// result once it exceeds the 107-byte sun_path limit. A spinclass worktree
// session sets $TMPDIR to a path deep inside the worktree itself, which —
// combined with clown's 36-char per-instance UUID as the session name — can
// push the total past that limit (clown#158). We force POSH_DIR to match
// posh's own tier-2 resolution: $XDG_RUNTIME_DIR/posh when XDG_RUNTIME_DIR is
// set and non-empty (always true on Linux; short, tmpfs-backed, safely under
// the sun_path limit), falling back to /tmp/posh-<uid> only when it isn't (the
// darwin deep-TMPDIR case #158 originally targeted). This keeps clown and bare
// posh list/attach on the same socket base with no env plumbing required
// (clown#190). A no-op for every other configured multiplexer, and for a user
// who already set POSH_DIR explicitly.
func poshEnv(muxBin string, env []string) []string {
	if filepath.Base(muxBin) != "posh" {
		return env
	}
	var xdg string
	for _, kv := range env {
		key, val, _ := strings.Cut(kv, "=")
		switch key {
		case "POSH_DIR":
			return env
		case "XDG_RUNTIME_DIR":
			xdg = val
		}
	}
	if xdg != "" {
		return append(env, "POSH_DIR="+xdg+"/posh")
	}
	return append(env, fmt.Sprintf("POSH_DIR=/tmp/posh-%d", os.Getuid()))
}

func runMultiplexer(muxBin string, argv []string) (int, error) {
	cmd := exec.Command(muxBin, argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = poshEnv(muxBin, os.Environ())

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
	if p.mcpCollapse {
		argv = append(argv, "--mcp-collapse")
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
