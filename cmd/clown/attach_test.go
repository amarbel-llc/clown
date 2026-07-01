package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/amarbel-llc/clown/internal/clownfile"
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
