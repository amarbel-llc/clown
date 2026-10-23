package main

import (
	"fmt"
	"os"

	"code.linenisgreat.com/clown/internal/sessions"
)

// profileLookup is sessions.ProfileOf's shape, a parameter for tests.
type profileLookup func(id string) (sessions.ProfilePin, bool, error)

// profileGate is what checkLaunchProfile decides from (FDR 0022 slice 2,
// clown#295).
type profileGate struct {
	// resumeID is the existing session this launch continues
	// (gatedSessionID); "" for a fresh launch.
	resumeID string
	// requested is the profile the launcher asked for by --profile or
	// CLOWN_PROFILE. A profile restored from the session record, a clownfile
	// pin or the build default is not a request.
	requested string
	// profileEnvSet reports a non-empty CLOWN_PROFILE in the environment.
	profileEnvSet bool
	// approvedDigest is CLOWN_PROFILE_DIGEST (consumeApprovedDigest), ""
	// when unset.
	approvedDigest string
	// launch is the applied profile's pin (launchProfilePin); nil when the
	// launch applies no profile.
	launch *sessions.ProfilePin
}

// checkLaunchProfile refuses a launch whose profile would change a
// session's recorded profile without approval, and reports whether the
// launcher approved the profile it runs with.
//
// What this does and does not protect: it guards against operator mistakes
// and silent drift (an edited profiles.toml, a clown upgrade). It does not
// stop an agent that can run clown with an environment of its choosing, or
// write clown's state dir ($XDG_STATE_HOME/clown/session-names.jsonl) or
// profiles.toml: such an agent can set CLOWN_PROFILE_DIGEST itself or edit
// the pin. CLOWN_PROFILE_DIGEST is the launcher's pinned expectation; clown
// checks only that the profile it resolved has exactly that digest and never
// sees or verifies a grant (decision 1). "Only an operator grant re-pins"
// holds only when spinclass is the sole launcher, with a scrubbed
// environment and a clown state dir the agent cannot write (agent-scope
// unit). The approval is also not bound to a session id yet.
//
// Rules, in order:
//   - CLOWN_PROFILE_DIGEST set: the applied profile must resolve to exactly
//     that digest (fresh launch or resume). A match is an approval.
//   - Fresh launch otherwise: unchanged.
//   - Resume with CLOWN_PROFILE set but no CLOWN_PROFILE_DIGEST: refused
//     (decision 8), unless it names the session's pin exactly (the inner
//     clown of an [attach] re-exec, whose outer consumed the digest after
//     recording the approved pin).
//   - Resume of a session with a recorded pin: the applied profile must
//     equal it, name and digest (an unpinnable record never matches).
//     `clown resume --profile X` naming the pinned profile is a no-op;
//     anything else, including resuming without a profile, is refused
//     (decisions 2, 3).
//   - Resume of a session with no pin: an explicit --profile/CLOWN_PROFILE
//     is a transition needing approval; a defaulted profile is allowed and
//     pinned on first use (trust on first use), early, like an approved pin.
//
// Approved and trust-on-first-use pins are recorded by the time the
// provider runs (before any [attach] re-exec; not under
// --print-launch-plan). An approved launch's records (launchRecordFor) skip
// the pin comparison for both identity.Key and the claude id, so approval
// is not bound to a session id.
//
// Not seen: claude --continue/-c, a bare --resume (claude's own picker) and
// --from-pr resume a conversation whose id clown never learns, so they are
// fresh launches here and the session's pin is neither checked nor updated.
//
// recorded's error fails the resume closed. Since every fresh claude launch
// carries a --session-id (decideClaudeSession), an unreadable session-names
// sidecar blocks fresh claude launches too (gatedSessionID): availability
// now depends on that file being readable.
func checkLaunchProfile(g profileGate, recorded profileLookup) (profileDecision, error) {
	if g.approvedDigest != "" {
		switch {
		case g.launch == nil:
			return profileDecision{}, fmt.Errorf("CLOWN_PROFILE_DIGEST is set (%s) but no profile is applied; refusing", g.approvedDigest)
		case g.launch.Digest == sessions.UnpinnableDigest:
			return profileDecision{}, fmt.Errorf("profile %q cannot be pinned (pkg/profilelookup cannot vouch for its registry), so it cannot match CLOWN_PROFILE_DIGEST %s; refusing", g.launch.Name, g.approvedDigest)
		case g.launch.Digest != g.approvedDigest:
			return profileDecision{}, fmt.Errorf("profile %q resolves to digest %s, not the CLOWN_PROFILE_DIGEST %s the launcher pinned; refusing", g.launch.Name, g.launch.Digest, g.approvedDigest)
		}
		d := profileDecision{approved: true}
		if g.resumeID != "" {
			d.pinEarly = g.launch
		}
		return d, nil
	}
	if g.resumeID == "" {
		return profileDecision{}, nil
	}
	existing, ok, err := recorded(g.resumeID)
	if err != nil {
		return profileDecision{}, fmt.Errorf("cannot read the profile session %s was launched with: %w; refusing to resume", g.resumeID, err)
	}
	matchesPin := ok && g.launch != nil && *g.launch == existing && existing.Digest != sessions.UnpinnableDigest
	if g.profileEnvSet && !matchesPin {
		return profileDecision{}, fmt.Errorf("CLOWN_PROFILE is set for a resume of session %s without CLOWN_PROFILE_DIGEST and does not name the session's pin; refusing (a launcher that changes a profile must pin its digest)", g.resumeID)
	}
	if !ok {
		if g.requested != "" {
			return profileDecision{}, fmt.Errorf("session %s recorded no profile; resuming it under profile %q%s is a profile change that needs approval (CLOWN_PROFILE_DIGEST from the launcher); refusing", g.resumeID, g.requested, digestSuffix(g.launch))
		}
		// Trust on first use: pin before any [attach] re-exec, so the inner
		// clown finds the session pinned to what it applies. An unpinnable
		// profile is not pinned early (its pin would never match).
		if g.launch != nil && g.launch.Digest != sessions.UnpinnableDigest {
			return profileDecision{pinEarly: g.launch}, nil
		}
		return profileDecision{}, nil
	}
	if g.launch == nil {
		return profileDecision{}, fmt.Errorf("session %s was launched with profile %q (digest %s); refusing to resume it without that profile", g.resumeID, existing.Name, existing.Digest)
	}
	if !matchesPin {
		return profileDecision{}, fmt.Errorf("session %s was launched with profile %q (digest %s); this launch applies profile %q (digest %s); a profile change needs approval (CLOWN_PROFILE_DIGEST from the launcher); refusing", g.resumeID, existing.Name, existing.Digest, g.launch.Name, g.launch.Digest)
	}
	return profileDecision{}, nil
}

// profileDecision is what checkLaunchProfile allowed.
type profileDecision struct {
	// approved: the launcher's CLOWN_PROFILE_DIGEST matched the applied
	// profile.
	approved bool
	// pinEarly is the pin to record for the gated session before any
	// [attach] re-exec (an approved pin, or a trust-on-first-use pin); nil
	// for none. The write must succeed or the launch is refused.
	pinEarly *sessions.ProfilePin
}

// newProfileGate builds the gate for a launch. flagProfile is --profile or
// CLOWN_PROFILE as parseFlags read them (before clownfile/default
// resolution). attachInner marks the inner clown of an [attach] re-exec: its
// --profile comes from the outer's reexecArgv, which emits the resolved name
// even when it was defaulted, and the outer already ran this gate, so the
// inner does not count it as a request (rules that compare against a pin
// still apply).
func newProfileGate(flags parsedFlags, flagProfile, approvedDigest string, launch *sessions.ProfilePin, attachInner bool, recorded profileLookup) profileGate {
	g := profileGate{
		profileEnvSet:  os.Getenv("CLOWN_PROFILE") != "",
		approvedDigest: approvedDigest,
		launch:         launch,
	}
	if !flags.profileFromRecord && !attachInner {
		g.requested = flagProfile
	}
	if flags.provider == "claude" {
		g.resumeID = gatedSessionID(flags.forwarded, recorded)
	}
	return g
}

// gatedSessionID returns the existing session a claude launch continues, for
// checkLaunchProfile: the --resume/-r id (every `clown resume` form passes
// one), else a --session-id that already carries a pin or whose record
// cannot be read. Call it after decideClaudeSession, which turns
// CLOWN_SESSION_ID/CLAUDE_SESSION_ID (identity.Key) into an injected
// --session-id, so `clown -- --session-id <pinned>` and
// CLOWN_SESSION_ID=<pinned> are gated like a resume. A --session-id with no
// pin is a fresh launch. --continue/-c, bare --resume and --from-pr carry no
// id and are not seen (see checkLaunchProfile).
func gatedSessionID(forwarded []string, recorded profileLookup) string {
	if id := claudeFlagValue(forwarded, "--resume", "-r"); id != "" {
		return id
	}
	id := claudeFlagValue(forwarded, "--session-id")
	if id == "" {
		return ""
	}
	if _, ok, err := recorded(id); ok || err != nil {
		return id
	}
	return ""
}

// launchRecordFor returns base (a session-names record for base.SessionID)
// carrying launch's pin, unless that would replace a different pin the
// session already has without approval, or the existing pin cannot be read;
// then the record carries no profile fields (a name-only record never
// changes a pin). This is the write-side guard behind checkLaunchProfile,
// for ids the gate did not check (identity.Key when it differs from the
// claude id).
func launchRecordFor(base sessions.NameRecord, launch *sessions.ProfilePin, approved bool, recorded profileLookup) sessions.NameRecord {
	base.Profile, base.ProfileDigest = "", ""
	if launch == nil {
		return base
	}
	if !approved {
		existing, ok, err := recorded(base.SessionID)
		if err != nil || (ok && existing != *launch) {
			return base
		}
	}
	base.Profile, base.ProfileDigest = launch.Name, launch.Digest
	return base
}

// consumeApprovedDigest returns CLOWN_PROFILE_DIGEST and removes it from
// this process's environment, so the provider, an [attach] re-exec child and
// any nested clown never inherit the launcher's approval (an agent in the
// session could otherwise re-pin another session with it). The approved pin
// is recorded before any re-exec, so the inner clown needs no digest.
// CLOWN_PROFILE is left in place.
func consumeApprovedDigest() string {
	d := os.Getenv("CLOWN_PROFILE_DIGEST")
	_ = os.Unsetenv("CLOWN_PROFILE_DIGEST")
	return d
}

func digestSuffix(p *sessions.ProfilePin) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf(" (digest %s)", p.Digest)
}
