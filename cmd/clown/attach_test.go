package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/clownfile"
)

func TestExtractAttachID(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantID  string
		wantArg []string
	}{
		{"absent", []string{"--provider", "claude"}, "", []string{"--provider", "claude"}},
		{"space form", []string{"--clown-attach-id", "k1", "resume"}, "k1", []string{"resume"}},
		{"equals form", []string{"--clown-attach-id=k2", "--", "hi"}, "k2", []string{"--", "hi"}},
		{"interleaved", []string{"--provider", "claude", "--clown-attach-id", "k3", "--", "x"}, "k3", []string{"--provider", "claude", "--", "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, rest := extractAttachID(tc.args)
			if id != tc.wantID {
				t.Errorf("id = %q, want %q", id, tc.wantID)
			}
			if !reflect.DeepEqual(rest, tc.wantArg) {
				t.Errorf("rest = %v, want %v", rest, tc.wantArg)
			}
		})
	}
}

func TestExtractAttachSpawn(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantSpawn bool
		wantArg   []string
	}{
		{"absent", []string{"--provider", "claude"}, false, []string{"--provider", "claude"}},
		{"equals form", []string{"--clown-attach=spawn", "--", "hi"}, true, []string{"--", "hi"}},
		{"space form", []string{"--clown-attach", "spawn", "resume"}, true, []string{"resume"}},
		{"other mode stripped, not spawn", []string{"--clown-attach=start", "x"}, false, []string{"x"}},
		{"does not swallow --clown-attach-id", []string{"--clown-attach-id", "k", "y"}, false, []string{"--clown-attach-id", "k", "y"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spawn, rest := extractAttachSpawn(tc.args)
			if spawn != tc.wantSpawn {
				t.Errorf("spawn = %v, want %v", spawn, tc.wantSpawn)
			}
			if !reflect.DeepEqual(rest, tc.wantArg) {
				t.Errorf("rest = %v, want %v", rest, tc.wantArg)
			}
		})
	}
}

// resolveSessionIdentity pins to attachedID (the [attach] re-exec id) at top
// precedence over the env-derived key (clown#145 / RFC-0013 §1.3).
func TestResolveSessionIdentityHonorsAttachID(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "ambient-key")
	prev := attachedID
	attachedID = "pinned-attach-id"
	t.Cleanup(func() { attachedID = prev })

	if got := resolveSessionIdentity(); got.Key != "pinned-attach-id" {
		t.Fatalf("identity.Key = %q, want the pinned attach id", got.Key)
	}
}

// Loop guard: the inner attached process (attachedID set) MUST NOT re-wrap, even
// with the multiplexer enabled and the TTY check forced (clown#145). Returns nil
// (skip) before any exec.
func TestMaybeReexecSkipsWhenAttached(t *testing.T) {
	prev := attachedID
	attachedID = "already-inside"
	t.Cleanup(func() { attachedID = prev })
	t.Setenv("CLOWN_ATTACH_FORCE", "1")

	cf := clownfile.Clownfile{Attach: clownfile.Attach{
		Multiplexer: "zmx",
		Start:       []string{"zmx", "attach", "{id}", "{entry}"},
	}}
	if err := maybeReexecMultiplexer(cf, parsedFlags{}, clownfile.ModeStart); err != nil {
		t.Fatalf("loop guard: want nil (skip), got %v", err)
	}
}

// Disabled [attach] (absent / "none") runs inline: the gate returns nil without
// execing.
func TestMaybeReexecSkipsWhenDisabled(t *testing.T) {
	prev := attachedID
	attachedID = ""
	t.Cleanup(func() { attachedID = prev })

	if err := maybeReexecMultiplexer(clownfile.Clownfile{}, parsedFlags{}, clownfile.ModeStart); err != nil {
		t.Fatalf("absent [attach]: want nil (skip), got %v", err)
	}
	none := clownfile.Clownfile{Attach: clownfile.Attach{Multiplexer: "none"}}
	if err := maybeReexecMultiplexer(none, parsedFlags{}, clownfile.ModeStart); err != nil {
		t.Fatalf(`multiplexer "none": want nil (skip), got %v`, err)
	}
}

// A configured-but-uninstalled multiplexer degrades to inline rather than
// failing, so the burned-in default clownfile is safe on hosts without the mux
// (clown#146). argv[0] is a binary guaranteed not to exist, so exec.LookPath
// fails deterministically; maybeReexecMultiplexer must return nil (run inline).
func TestMaybeReexecDegradesWhenMuxAbsent(t *testing.T) {
	prev := attachedID
	attachedID = ""
	t.Cleanup(func() { attachedID = prev })
	t.Setenv("CLOWN_ATTACH_FORCE", "1")

	cf := clownfile.Clownfile{Attach: clownfile.Attach{
		Multiplexer: "zmx",
		Start:       []string{"clown-nonexistent-mux-xyz-do-not-install", "{id}", "{entry}"},
	}}
	if err := maybeReexecMultiplexer(cf, parsedFlags{}, clownfile.ModeStart); err != nil {
		t.Fatalf("absent multiplexer: want nil (degrade to inline), got %v", err)
	}
}

// TTY gate: ModeSpawn is exempt (a detached worker is always non-interactive, so
// it MUST resolve its template regardless of TTY — RFC-0014 §5.1, clown#161),
// while ModeStart under the same no-TTY/no-force conditions runs inline. Both
// cases point at a nonexistent mux binary so the gate decision is observable
// without actually execing: ModeStart returns nil at the gate (before LookPath),
// and ModeSpawn falls through the gate to the mux-absent degrade path (also nil)
// — so nil alone can't distinguish them. We instead assert the gate is passed by
// spawn via a resolvable template element: a template with a surviving unknown
// placeholder makes Resolve error, which is only reached AFTER the gate. So
// ModeSpawn surfaces the Resolve error (gate passed) while ModeStart returns nil
// (gated out before Resolve).
func TestMaybeReexecSpawnBypassesTTYGate(t *testing.T) {
	prev := attachedID
	attachedID = ""
	t.Cleanup(func() { attachedID = prev })
	// Deliberately NOT setting CLOWN_ATTACH_FORCE, and bats/go test has no PTY, so
	// isInteractiveTerminal() is false — the real spawn context.
	t.Setenv("CLOWN_ATTACH_FORCE", "")

	// A surviving {bogus} placeholder makes Attach.Resolve error — but Resolve is
	// only reached if the TTY gate is passed. Reuse the same template for both
	// modes so the ONLY variable is the gate.
	tmpl := []string{"zmx", "attach", "{bogus}", "{entry}"}
	cf := clownfile.Clownfile{Attach: clownfile.Attach{
		Multiplexer: "zmx",
		Start:       tmpl,
		Spawn:       tmpl,
	}}

	// ModeStart: gated out before Resolve → nil, no error surfaced.
	if err := maybeReexecMultiplexer(cf, parsedFlags{}, clownfile.ModeStart); err != nil {
		t.Fatalf("ModeStart no-TTY: want nil (gated out), got %v", err)
	}
	// ModeSpawn: gate bypassed → Resolve runs and rejects {bogus}, surfacing an
	// error. That error IS the proof the gate was passed.
	if err := maybeReexecMultiplexer(cf, parsedFlags{}, clownfile.ModeSpawn); err == nil {
		t.Fatal("ModeSpawn no-TTY: want the Resolve error (gate bypassed), got nil (gated out)")
	}
}

// captureTitle runs the real emitSessionTitle with os.Stderr redirected to a
// pipe and returns exactly what it wrote — the actual emitted escape sequence,
// not a re-derivation of the production logic.
func captureTitle(t *testing.T, cf clownfile.Clownfile, flags parsedFlags) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	emitSessionTitle(cf, flags)
	os.Stderr = origStderr
	w.Close()
	captured, _ := io.ReadAll(r)
	return string(captured)
}

// The title must be emitted by the INNER attached clown — the process running
// inside the multiplexer's pty — so the mux daemon's terminal model holds it and
// re-asserts it on attach, switch, and repaint (clown#231, clown#232). That
// process is exactly the one the re-wrap loop guard turns away, so this asserts
// both halves together: maybeReexecMultiplexer declines to wrap, and a title is
// emitted anyway. Before the fix the guard suppressed the title with the wrap,
// and a spawned worker (whose only clown IS the inner one) got no title at all.
func TestEmitSessionTitleFromInnerAttachedProcess(t *testing.T) {
	prev := attachedID
	attachedID = "already-inside"
	t.Cleanup(func() { attachedID = prev })
	t.Setenv("CLOWN_ATTACH_FORCE", "1")
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	cf := clownfile.Clownfile{Attach: clownfile.Attach{
		Multiplexer: "posh",
		Start:       []string{"posh", "attach", "--create", "{id}", "{entry}"},
		ResumeTitle: "sc/{group}/{id}",
	}}
	flags := parsedFlags{
		clownName: "bozo",
		groupID:   "clown/loud-rowan",
		identity:  sessionIdentity{Key: "raw-uuid"},
	}

	if err := maybeReexecMultiplexer(cf, flags, clownfile.ModeStart); err != nil {
		t.Fatalf("inner process must not re-wrap: want nil, got %v", err)
	}
	if got, want := captureTitle(t, cf, flags), "\033]2;sc/clown/loud-rowan/bozo\007"; !strings.Contains(got, want) {
		t.Fatalf("inner-process title = %q, want it to contain %q", got, want)
	}
}

// A real spinclass group ALWAYS shows the clown-name (clown#230), even though
// this is the only live session in the group. spinclass creates one clown per
// worktree, so the old 2+-live-sessions dedup was unsatisfiable by construction
// and stripped the name from every fleet session's title. The presence dir holds
// exactly one record in this group, which is precisely the case that used to
// suppress it.
func TestEmitSessionTitleSpinclassGroupAlwaysShowsClownName(t *testing.T) {
	t.Setenv("CLOWN_ATTACH_FORCE", "1")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	registerPresenceFixture(t, "inst-a", "clown/loud-rowan", "A")

	cf := clownfile.Clownfile{Attach: clownfile.Attach{
		Multiplexer: "posh",
		ResumeTitle: "sc/{group}/{id}",
	}}
	flags := parsedFlags{
		clownName: "grock",
		groupID:   "clown/loud-rowan",
		identity:  sessionIdentity{Key: "raw-uuid"},
	}

	if got, want := captureTitle(t, cf, flags), "\033]2;sc/clown/loud-rowan/grock\007"; !strings.Contains(got, want) {
		t.Fatalf("solo spinclass title = %q, want it to contain %q (the fully-qualified id)", got, want)
	}
}

// No terminal to title (no TTY, no CLOWN_ATTACH_FORCE) ⇒ no emission, so a
// non-interactive run never sprays OSC control bytes into a redirected stderr.
func TestEmitSessionTitleSkippedWithoutTerminal(t *testing.T) {
	t.Setenv("CLOWN_ATTACH_FORCE", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	cf := clownfile.Clownfile{Attach: clownfile.Attach{
		Multiplexer: "posh",
		ResumeTitle: "sc/{group}/{id}",
	}}
	flags := parsedFlags{clownName: "bozo", groupID: "clown/loud-rowan"}

	// go test runs with no PTY, so isInteractiveTerminal() is false here.
	if got := captureTitle(t, cf, flags); got != "" {
		t.Fatalf("non-interactive emission = %q, want no output", got)
	}
}

// clown-name preference (clown#169): the OSC-2 title's {id} should resolve to
// the human-ergonomic clown-name rather than the raw per-instance UUID.
//
// This template has no {group}, so it also pins the other half of clown#229's
// tier-3 rule: with nothing for {group}'s fallback to render, {id} must survive
// or the title would be empty and no OSC-2 sequence would be emitted at all.
func TestEmitSessionTitlePrefersClownName(t *testing.T) {
	t.Setenv("CLOWN_ATTACH_FORCE", "1")

	// Run outside any git repo so gitRepoAndBranch() returns "" and titleGroup
	// stays empty — the tier-3 "always show id" case. This test's flags carry no
	// groupID, so without this the git-fallback tier would activate (this test
	// binary's cwd is always inside the clown repo) and suppress {id} via
	// titleDisambiguationNeeded's LIVE jobwake presence lookup — making the
	// assertion below depend on ambient presence state (how many other clown
	// sessions happen to share this cwd right now) rather than this test's own
	// inputs (clown#186).
	t.Chdir(hostTempDir(t))

	cf := clownfile.Clownfile{Attach: clownfile.Attach{
		Multiplexer: "zmx",
		ResumeTitle: "{id}",
	}}
	flags := parsedFlags{clownName: "bozo", identity: sessionIdentity{Key: "raw-uuid-1234"}}

	captured := captureTitle(t, cf, flags)
	if !strings.Contains(captured, "\033]2;bozo\007") {
		t.Fatalf("emitted title = %q, want the OSC-2 sequence for the clown-name %q, not the raw UUID", captured, "bozo")
	}
	if strings.Contains(captured, "raw-uuid-1234") {
		t.Fatalf("emitted title leaked the raw UUID instead of preferring the clown-name: %q", captured)
	}
}

// Tier 3 (no spinclass group, no git repo) with the SHIPPED default template:
// the clown-name must appear exactly ONCE. Title's {group} fallback already
// substitutes the name for an empty group, so the caller must not also force the
// separate {id} — doing so rendered "sc/bozo/bozo" (clown#229). The neighbouring
// tier-3 test uses a bare "{id}" template, where no {group} fallback fires at
// all, so it cannot see this; the clownfile-level test passes showID=false by
// hand rather than exercising the caller's own policy.
func TestEmitSessionTitleNoGroupShowsClownNameOnce(t *testing.T) {
	t.Setenv("CLOWN_ATTACH_FORCE", "1")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// Outside any git repo so gitRepoAndBranch() returns "" and titleGroup stays
	// empty — the real tier-3 shape rather than a simulated one.
	t.Chdir(hostTempDir(t))

	cf := clownfile.Clownfile{Attach: clownfile.Attach{
		Multiplexer: "zmx",
		ResumeTitle: "sc/{group}/{id}",
	}}
	flags := parsedFlags{clownName: "bozo", identity: sessionIdentity{Key: "raw-uuid"}}

	captured := captureTitle(t, cf, flags)
	if want := "\033]2;sc/bozo\007"; !strings.Contains(captured, want) {
		t.Fatalf("tier-3 default-template title = %q, want it to contain %q", captured, want)
	}
	if strings.Contains(captured, "bozo/bozo") {
		t.Fatalf("tier-3 title duplicated the clown-name: %q", captured)
	}
}

// The git-repo/branch fallback tier keeps its solo dedup (clown#180, FDR-0015):
// outside spinclass (empty groupID) with no sibling presence records the title
// collapses to "sc/<repo>/<branch>", dropping the clown-name that would
// disambiguate nothing. Only the spinclass tier's dedup was dropped (clown#230).
func TestEmitSessionTitleGitFallbackSolo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	repoBranch := gitRepoAndBranch()
	if repoBranch == "" {
		t.Skip("not inside a git worktree; git-fallback path not exercisable here")
	}

	t.Setenv("CLOWN_ATTACH_FORCE", "1")
	// Isolate presence so the solo-dedup count is deterministic (no sibling
	// sessions from the ambient state dir), and ensure no group-id is set.
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	cf := clownfile.Clownfile{Attach: clownfile.Attach{
		Multiplexer: "zmx",
		ResumeTitle: "sc/{group}/{id}",
	}}
	// groupID empty → git fallback; clownName present but solo → dropped.
	flags := parsedFlags{clownName: "bozo", identity: sessionIdentity{Key: "raw-uuid"}}

	captured := captureTitle(t, cf, flags)
	want := "\033]2;sc/" + repoBranch + "\007"
	if !strings.Contains(captured, want) {
		t.Fatalf("emitted title = %q, want it to contain %q (git fallback, solo id dropped)", captured, want)
	}
	if strings.Contains(captured, "/bozo\007") {
		t.Fatalf("solo session leaked the redundant clown-name: %q", captured)
	}
}

// runMultiplexer runs the mux as a child (not syscall.Exec), so clown survives
// to print the resume hint outside the mux. It must forward argv[1:] to the child
// (argv[0] is the ignored template name) and propagate the child's exit code.
func TestRunMultiplexer(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not on PATH: %v", err)
	}
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	stub := filepath.Join(dir, "mux")
	// The stub records the args it received and exits non-zero so we can assert
	// both argv forwarding and exit-code propagation in one run.
	script := "#!" + sh + "\nprintf '%s\\n' \"$@\" > " + argvFile + "\nexit 7\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	code, err := runMultiplexer(stub, []string{"mux", "attach", "sess"})
	if err != nil {
		t.Fatalf("runMultiplexer: %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7 (child's code propagated)", code)
	}
	got, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("reading child argv: %v", err)
	}
	if want := "attach\nsess\n"; string(got) != want {
		t.Errorf("child argv = %q, want %q (argv[1:] forwarded, argv[0] dropped)", got, want)
	}
}

// poshEnv forces POSH_DIR to match posh's own tier-2 resolution: XDG_RUNTIME_DIR/posh
// when XDG_RUNTIME_DIR is set (always on Linux), /tmp/posh-<uid> otherwise (clown#158,
// clown#190). The value is read from the env SLICE, not os.Getenv.
func TestPoshEnv(t *testing.T) {
	t.Run("posh with XDG_RUNTIME_DIR set gets XDG-based POSH_DIR", func(t *testing.T) {
		env := []string{"PATH=/bin", "XDG_RUNTIME_DIR=/run/user/1000", "TMPDIR=/very/deep/worktree/.tmp"}
		got := poshEnv("/nix/store/xyz-posh/bin/posh", env)
		want := "POSH_DIR=/run/user/1000/posh"
		if len(got) != len(env)+1 || got[len(got)-1] != want {
			t.Errorf("poshEnv() = %v, want %v appended with %q", got, env, want)
		}
	})

	t.Run("posh with no POSH_DIR and no XDG_RUNTIME_DIR falls back to /tmp/posh-<uid>", func(t *testing.T) {
		env := []string{"PATH=/bin", "TMPDIR=/very/deep/worktree/.tmp"}
		got := poshEnv("/nix/store/xyz-posh/bin/posh", env)
		want := fmt.Sprintf("POSH_DIR=/tmp/posh-%d", os.Getuid())
		if len(got) != len(env)+1 || got[len(got)-1] != want {
			t.Errorf("poshEnv() = %v, want %v appended with %q", got, env, want)
		}
	})

	t.Run("posh with POSH_DIR already set is left alone", func(t *testing.T) {
		env := []string{"PATH=/bin", "POSH_DIR=/custom/posh"}
		got := poshEnv("posh", env)
		if !reflect.DeepEqual(got, env) {
			t.Errorf("poshEnv() = %v, want unchanged %v (caller's POSH_DIR wins)", got, env)
		}
	})

	t.Run("non-posh multiplexer is left alone", func(t *testing.T) {
		env := []string{"PATH=/bin", "TMPDIR=/very/deep/worktree/.tmp"}
		got := poshEnv("/usr/bin/tmux", env)
		if !reflect.DeepEqual(got, env) {
			t.Errorf("poshEnv() = %v, want unchanged %v (only posh is affected)", got, env)
		}
	})
}

// A binary that cannot be started surfaces the error (and code 1), rather than
// os.Exit'ing — the caller (maybeReexecMultiplexer) turns it into a fatal wrap
// error. (The mux-absent case is caught earlier by exec.LookPath; this covers a
// resolved-but-unstartable path.)
func TestRunMultiplexerStartError(t *testing.T) {
	code, err := runMultiplexer(filepath.Join(t.TempDir(), "does-not-exist"), []string{"mux"})
	if err == nil {
		t.Fatal("want a start error for a nonexistent binary, got nil")
	}
	if code != 1 {
		t.Errorf("code = %d, want 1 on start failure", code)
	}
}

// reexecArgv reconstructs the RESOLVED clown argv spliced into the mux {entry}
// (clown#160). The invariant that kills the double dialog: the inner argv always
// carries an explicit --provider (suppresses the profile picker) and, for a
// resume, the injected --resume/--session-id in the forwarded tail (suppresses
// the resume picker and selects the resume template).
func TestReexecArgv(t *testing.T) {
	cases := []struct {
		name string
		in   parsedFlags
		want []string
	}{
		{
			// resume subcommand: launchResume appends --resume <id>, then
			// decideClaudeSession leaves it and identity.Key = <id>.
			name: "resume",
			in: parsedFlags{
				provider:         "claude",
				providerExplicit: true,
				forwarded:        []string{"--resume", "sess-abc"},
			},
			want: []string{"--provider", "claude", "--", "--resume", "sess-abc"},
		},
		{
			// profile pick resolves a provider even though the user never typed
			// --provider; the fresh-start --session-id injection rides in forwarded.
			name: "profile pick, fresh start",
			in: parsedFlags{
				provider:         "claude",
				providerExplicit: true,
				forwarded:        []string{"--session-id", "minted-uuid"},
			},
			want: []string{"--provider", "claude", "--", "--session-id", "minted-uuid"},
		},
		{
			// clown -- --resume x: forwarded carries the user's --resume verbatim.
			name: "dash-dash resume",
			in: parsedFlags{
				provider:         "claude",
				providerExplicit: true,
				forwarded:        []string{"--resume", "x"},
			},
			want: []string{"--provider", "claude", "--", "--resume", "x"},
		},
		{
			// Profile pick (picker or --profile): both --provider and --profile are
			// emitted so the inner re-resolves the SAME profile (its backend/model/
			// env/URL), not just the bare provider — otherwise opencode/crush lose
			// their profile config across the re-exec (clown#160).
			name: "profile carried with provider",
			in: parsedFlags{
				provider: "opencode",
				profile:  "crush-gateway",
			},
			want: []string{"--provider", "opencode", "--profile", "crush-gateway"},
		},
		{
			// Non-selection top-level flags survive the re-exec.
			name: "flags survive",
			in: parsedFlags{
				provider:        "codex",
				tent:            true,
				verbose:         true,
				extraPluginDirs: []string{"/a", "/b"},
			},
			want: []string{"--provider", "codex", "--tent", "--verbose", "--plugin-dir", "/a", "--plugin-dir", "/b"},
		},
		{
			// --cheap-context must survive the re-exec: the default posh
			// multiplexer wrap re-launches the inner clown from reexecArgv,
			// not raw os.Args, so a flag missing here is silently dropped
			// for every interactive session (caught live: the picker never
			// rendered under the default [attach] wrap).
			name: "cheap-context survives",
			in: parsedFlags{
				provider:     "claude",
				cheapContext: true,
			},
			want: []string{"--provider", "claude", "--cheap-context"},
		},
		{
			// --mcp-collapse must survive the re-exec for the SAME reason as
			// --cheap-context: the default posh multiplexer wrap re-launches the
			// inner clown from reexecArgv, so an omission here silently disables
			// collapse on every normal launch (clown#211-adjacent bug: the flag
			// parsed and applied fine in-process but never reached the inner one).
			name: "mcp-collapse survives",
			in: parsedFlags{
				provider:    "claude",
				mcpCollapse: true,
			},
			want: []string{"--provider", "claude", "--mcp-collapse"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.reexecArgv()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("reexecArgv() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestReexecArgvBoolFlagsRoundTrip guards the WHOLE CLASS of the
// "--mcp-collapse silently dropped across the multiplexer self-wrap" bug:
// reexecArgv is a hand-maintained flag-echo list, so any NEW top-level bool
// flag someone forgets to add there vanishes on the inner clown when the
// default posh [attach] wrap re-launches from reexecArgv (not raw os.Args).
// This test sets every reexecArgv-emitted bool flag true, round-trips through
// reexecArgv -> parseFlags, and asserts each survives. A future dropped flag
// fails here loudly instead of silently no-op'ing at launch.
//
// Flags NOT emitted by reexecArgv are intentionally excluded: printLaunchPlan
// (diagnostic), version/help (early-return in run before the wrap), and the
// noPassDevshell/passDevshell pair (asserted separately below so the mutually
// exclusive devshell opt-in/opt-out don't clobber each other).
func TestReexecArgvBoolFlagsRoundTrip(t *testing.T) {
	// mcpCollapse is the mandatory anchor: this is the exact flag whose omission
	// from reexecArgv was the bug. If Fix A is reverted, the round-trip below
	// leaves out.mcpCollapse false and this test fails.
	in := parsedFlags{
		provider:             "claude",
		providerExplicit:     true,
		naked:                true,
		skipFailed:           true,
		cheapContext:         true,
		mcpCollapse:          true,
		disableClownProtocol: true,
		tent:                 true,
		verbose:              true,
	}

	out, err := parseFlags(in.reexecArgv())
	if err != nil {
		t.Fatalf("parseFlags(reexecArgv()) errored: %v", err)
	}

	checks := []struct {
		name string
		got  bool
	}{
		{"naked", out.naked},
		{"skipFailed", out.skipFailed},
		{"cheapContext", out.cheapContext},
		{"mcpCollapse", out.mcpCollapse},
		{"disableClownProtocol", out.disableClownProtocol},
		{"tent", out.tent},
		{"verbose", out.verbose},
	}
	for _, c := range checks {
		if !c.got {
			t.Errorf("bool flag %s did not survive the reexecArgv round-trip — is it emitted in reexecArgv()?", c.name)
		}
	}

	// Negative: a launch WITHOUT --mcp-collapse must NOT emit it, so the
	// default (non-collapse) reexec path is byte-identical to before.
	bare := parsedFlags{provider: "claude", providerExplicit: true}
	for _, a := range bare.reexecArgv() {
		if a == "--mcp-collapse" {
			t.Fatal("reexecArgv() emitted --mcp-collapse without mcpCollapse set — the default path must not collapse")
		}
	}
	if got, _ := parseFlags(bare.reexecArgv()); got.mcpCollapse {
		t.Fatal("round-tripped bare flags set mcpCollapse — default launch must not enable collapse")
	}
}

// hostTempDir returns a fresh directory guaranteed to sit outside any git
// repository, on both linux and darwin — unlike t.TempDir(), which honors
// $TMPDIR and so can resolve INSIDE a repo when $TMPDIR itself is nested
// under one. A spinclass worktree session sets $TMPDIR to exactly that kind
// of path (deep inside the worktree itself), which defeats any test that
// needs a genuinely repo-free directory (clown#185, clown#186; the same
// "don't trust the inherited $TMPDIR" lesson as poshEnv's clown#158 fix,
// above).
//
// /tmp is a real, world-writable directory on both platforms regardless of
// $TMPDIR: stable on linux, and on darwin a symlink to /private/tmp — distinct
// from the per-process $TMPDIR under /private/var/folders/.../T/ that
// t.TempDir() would otherwise honor.
func hostTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "clown-test-*")
	if err != nil {
		t.Skipf("cannot create a scratch dir under /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// gitRepoAndBranch resolves "<repo>/<branch>" for the OSC-2 title fallback
// (clown#180). Inside a real git worktree (this test's own repo) it returns a
// non-empty "<repo>/<branch>"; inside a non-git directory it returns "" so the
// title degrades to the true no-group tier. It reads the PROCESS cwd, so the
// non-git case chdir's into a bare temp dir.
func TestGitRepoAndBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}

	// This test binary runs inside the clown git worktree, so the helper must
	// resolve a repo, and (unless HEAD is detached) a "<repo>/<branch>".
	if got := gitRepoAndBranch(); got == "" {
		t.Error("gitRepoAndBranch() inside a git worktree = \"\", want a non-empty repo(/branch)")
	} else if !strings.Contains(got, "/") {
		// A detached HEAD legitimately yields just "<repo>"; a normal checkout
		// has a branch. The test worktree is on a branch, so expect the slash.
		t.Logf("gitRepoAndBranch() = %q (no branch segment — detached HEAD?)", got)
	}

	// A directory with no .git anywhere above it resolves to "".
	t.Chdir(hostTempDir(t))
	if got := gitRepoAndBranch(); got != "" {
		t.Errorf("gitRepoAndBranch() in a non-git dir = %q, want \"\"", got)
	}
}

// titleDisambiguationNeeded decides whether the clown-name {id} appears in a
// git-fallback title: only when 2+ live sessions share the cwd (clown#180). It
// keys on presence Cwd rather than Decoration, since the git-derived group is
// never written to Decoration. The fixtures all register from this test
// process's cwd, so two of them share a Cwd and trip the dedup.
func TestTitleDisambiguationNeededByCwd(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// One session in this cwd (group-id empty — the bare-clown case) → false.
	registerPresenceFixture(t, "inst-a", "", "A")
	if titleDisambiguationNeeded(wd) {
		t.Error("single session in cwd: want false")
	}
	// A cwd with no matching session at all → still false.
	if titleDisambiguationNeeded(filepath.Join(wd, "no-such-dir")) {
		t.Error("no session in cwd: want false")
	}

	// A second session registered from the same cwd → true.
	registerPresenceFixture(t, "inst-b", "", "B")
	if !titleDisambiguationNeeded(wd) {
		t.Error("two sessions sharing a cwd: want true")
	}
}
