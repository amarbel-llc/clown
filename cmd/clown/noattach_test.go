package main

import (
	"slices"
	"testing"

	"code.linenisgreat.com/clown/internal/clownfile"
)

// clown#159 review follow-ups.

// `clown resume --no-attach` is accepted and carried into the resumed
// launch, whose wrap it then skips.
func TestResumeAcceptsNoAttach(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	args, err := parseResumeArgs([]string{"--no-attach", "clown://claude/" + id})
	if err != nil {
		t.Fatal(err)
	}
	if !args.noAttach {
		t.Fatal("resume --no-attach not parsed")
	}
	f := resumeLaunchFlags(id, args, "", "", "")
	if !f.noAttach {
		t.Fatal("resumeLaunchFlags dropped --no-attach")
	}
	prev := attachedID
	attachedID = ""
	t.Cleanup(func() { attachedID = prev })
	t.Setenv("CLOWN_ATTACH_FORCE", "1")
	tmpl := []string{"zmx", "attach", "{bogus}", "{entry}"}
	cf := clownfile.Clownfile{Attach: clownfile.Attach{Multiplexer: "zmx", Resume: tmpl}}
	if err := maybeReexecMultiplexer(cf, f, clownfile.ModeResume); err != nil {
		t.Fatalf("resume with --no-attach: want nil (inline), got %v", err)
	}
	// Without it, the resume still wraps (the start-mode behaviour is the
	// same as before: the flag is opt-in).
	plain, err := parseResumeArgs([]string{"clown://claude/" + id})
	if err != nil {
		t.Fatal(err)
	}
	if err := maybeReexecMultiplexer(cf, resumeLaunchFlags(id, plain, "", "", ""), clownfile.ModeResume); err == nil {
		t.Fatal("resume without --no-attach: want the wrap attempted (Resolve error), got nil")
	}
}

// The restart relaunch carries --no-attach once, among clown's own flags
// (before `--`), so the relaunched frontend does not come back wrapped.
func TestRestartArgvCarriesNoAttach(t *testing.T) {
	f := parsedFlags{provider: "claude", providerExplicit: true, noAttach: true, identity: sessionIdentity{Key: "k"}}
	argv := restartArgv(f, "")
	sep := slices.Index(argv, "--")
	if sep < 0 {
		t.Fatalf("argv %q has no --", argv)
	}
	n := 0
	for _, a := range argv[:sep] {
		if a == "--no-attach" {
			n++
		}
	}
	if n != 1 || slices.Contains(argv[sep:], "--no-attach") {
		t.Fatalf("argv %q: want --no-attach exactly once, before --", argv)
	}
	if bare := restartArgv(parsedFlags{provider: "claude", identity: sessionIdentity{Key: "k"}}, ""); slices.Contains(bare, "--no-attach") {
		t.Fatalf("argv %q carries --no-attach without the flag", bare)
	}
}

// After `--` the flag is the provider's, untouched.
func TestParseFlagsNoAttachAfterSeparatorIsForwarded(t *testing.T) {
	p, err := parseFlags([]string{"--", "--no-attach"})
	if err != nil {
		t.Fatal(err)
	}
	if p.noAttach || !slices.Equal(p.forwarded, []string{"--no-attach"}) {
		t.Fatalf("noAttach %v forwarded %q; want false and [--no-attach]", p.noAttach, p.forwarded)
	}
}

// --help still answers when the contradictory pair is given.
func TestHelpPrecedesTheAttachConflict(t *testing.T) {
	prevID, prevSpawn, prevLaunch := attachedID, attachSpawn, launchWithFlags
	t.Cleanup(func() { attachedID, attachSpawn, launchWithFlags = prevID, prevSpawn, prevLaunch })
	launchWithFlags = func(parsedFlags) int { t.Fatal("launched"); return 1 }
	for _, args := range [][]string{
		{"--clown-attach=spawn", "--no-attach", "--help"},
		{"--no-attach", "-h", "--clown-attach", "spawn"},
	} {
		if code := run(args); code != 0 {
			t.Fatalf("run(%q) = %d, want 0", args, code)
		}
	}
}
