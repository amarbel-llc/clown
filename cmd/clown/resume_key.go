// resume_key.go — key mode for `clown resume` (clown#192 step 2).
//
// `clown resume repo/worktree[/clown-name]` resolves a session by its
// spinclass-shaped key instead of a URI or the $PWD picker. Live sessions
// win: if a presence record's group (Decoration) matches the key, we print
// an attach hint and stop — attaching to a live session beats forking a
// second conversation. Only when nothing live matches do we fall back to
// the recorded (dead) claude conversations via sessions.FilterByKey.
package main

import (
	"fmt"
	"os"
	"time"

	"code.linenisgreat.com/ringmaster/jobwake"

	"code.linenisgreat.com/clown/internal/sessions"
)

// poshAttachHint derives the one-line attach command for a live session.
// Kept as a single seam on purpose: when spinclass#241 renames posh
// sessions to repo/worktree keys, upgrading the hint from
// `posh attach <uuid>` to `posh <repo>/<worktree>` is a one-liner here.
func poshAttachHint(p jobwake.Presence) string {
	return "posh attach " + p.SessionKey
}

// resumeByKey handles `clown resume <repo>/<worktree>[/<clown-name>]`.
func resumeByKey(args resumeArgs) int {
	// LIVE check first. Presence read failures are best-effort: warn and
	// continue to dead resolution rather than blocking the resume.
	ps, err := jobwake.ListPresence(time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: presence check: %v (continuing to recorded conversations)\n", err)
		ps = nil
	}

	var live []jobwake.Presence
	for _, p := range ps {
		if p.Decoration == args.key {
			live = append(live, p)
		}
	}
	if args.keyName != "" && len(live) > 0 {
		var named []jobwake.Presence
		for _, p := range live {
			if p.ClownName == args.keyName {
				named = append(named, p)
			}
		}
		if len(named) == 0 {
			// The named conversation may be a dead one — say so and keep
			// going to dead resolution instead of stopping here.
			fmt.Fprintf(os.Stderr, "clown: live sessions exist for %q but none named %q; checking recorded conversations\n", args.key, args.keyName)
		}
		live = named
	}
	if len(live) > 0 {
		for _, p := range live {
			name := p.ClownName
			if name == "" {
				name = "unnamed"
			}
			fmt.Fprintf(os.Stderr, "clown: session %s is LIVE (%s) — attach with: %s\n", args.key, name, poshAttachHint(p))
		}
		return 0
	}

	// DEAD: most recent recorded conversation for the key.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}
	all, err := sessions.ListClaudeSessions(homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: listing claude sessions: %v\n", err)
		return 1
	}
	matches := sessions.FilterByKey(all, args.key)
	if len(matches) == 0 {
		fmt.Fprintf(os.Stderr, "clown: no resumable claude sessions for key %q\n", args.key)
		return 1
	}
	if args.keyName != "" {
		// clown#192 step 3: the session-names sidecar records the clown-name
		// each conversation wore while live; the third key segment selects
		// the newest conversation recorded under that name.
		ids := make([]string, len(matches))
		for i, s := range matches {
			ids[i] = s.ID
		}
		names := sessions.NamesFor(ids)
		var named []sessions.Session
		for _, s := range matches {
			if names[s.ID] == args.keyName {
				named = append(named, s)
			}
		}
		if len(named) == 0 {
			fmt.Fprintf(os.Stderr, "clown: no resumable claude sessions for key %q named %q\n", args.key, args.keyName)
			return 1
		}
		matches = named
	}
	s := matches[0]

	// Materialization: key mode resumes from anywhere by design, so
	// instead of resumeByURI's mismatch confirm we move INTO the recorded
	// directory when it still exists — the conversation resumes where it
	// lived (launchResume/runWithFlags exec the provider in the process
	// cwd). A gone directory falls through to the current one with a
	// note; claude rebuilds context as best it can.
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}
	if s.CWD != "" && !sessions.SameDir(s.CWD, cwd, "") {
		if st, statErr := os.Stat(s.CWD); statErr == nil && st.IsDir() {
			if chdirErr := os.Chdir(s.CWD); chdirErr != nil {
				fmt.Fprintf(os.Stderr, "clown: cannot enter recorded directory %q: %v; resuming from %s\n", s.CWD, chdirErr, cwd)
			}
		} else {
			fmt.Fprintf(os.Stderr, "clown: recorded directory %q is gone; resuming from %s\n", s.CWD, cwd)
		}
	}
	return resumeSingle(s, args)
}
