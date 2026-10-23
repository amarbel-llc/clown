package main

import (
	"fmt"

	"code.linenisgreat.com/clown/internal/sessions"
)

// profileGate is what checkLaunchProfile decides from (FDR 0022 slice 2,
// clown#295).
type profileGate struct {
	// resumeID is the existing session this launch resumes (claude
	// --resume/-r, which every `clown resume` form passes); "" for a fresh
	// launch.
	resumeID string
	// requested is the profile the launcher asked for by --profile or
	// CLOWN_PROFILE. A profile restored from the session record, a clownfile
	// pin or the build default is not a request.
	requested string
	// profileEnvSet reports a non-empty CLOWN_PROFILE in the environment.
	profileEnvSet bool
	// approvedDigest is CLOWN_PROFILE_DIGEST, "" when unset.
	approvedDigest string
	// launch is the applied profile's pin (launchProfilePin); nil when the
	// launch applies no profile.
	launch *sessions.ProfilePin
}

// checkLaunchProfile refuses a launch whose profile would change a
// session's recorded profile without approval, and reports whether the
// launcher approved the profile it runs with.
//
// What is trusted: CLOWN_PROFILE_DIGEST is the launcher's (spinclass's)
// pinned expectation. clown only checks that the profile it resolved has
// exactly that digest; it does not see or verify the grant behind it
// (decision 1). Whoever can set the environment of a clown launch can
// therefore approve a transition; the authority lives in spinclass, which
// re-pins only after verifying an operator grant.
//
// Rules, in order:
//   - CLOWN_PROFILE_DIGEST set: the applied profile must resolve to exactly
//     that digest (fresh launch or resume). A match is an approval.
//   - Fresh launch otherwise: unchanged.
//   - Resume with CLOWN_PROFILE set but no CLOWN_PROFILE_DIGEST: refused
//     (decision 8).
//   - Resume of a session with a recorded pin: the applied profile must
//     equal it, name and digest (an unpinnable record never matches).
//     `clown resume --profile X` naming the pinned profile is a no-op;
//     anything else, including resuming without a profile, is refused
//     (decisions 2, 3).
//   - Resume of a session with no pin: an explicit --profile/CLOWN_PROFILE
//     is a transition needing approval; a defaulted profile is allowed, as
//     before pinning existed.
//
// recorded is sessions.ProfileOf (a parameter for tests); its error fails
// the resume closed.
func checkLaunchProfile(g profileGate, recorded func(id string) (sessions.ProfilePin, bool, error)) (approved bool, err error) {
	if g.approvedDigest != "" {
		switch {
		case g.launch == nil:
			return false, fmt.Errorf("CLOWN_PROFILE_DIGEST is set (%s) but no profile is applied; refusing", g.approvedDigest)
		case g.launch.Digest == sessions.UnpinnableDigest:
			return false, fmt.Errorf("profile %q cannot be pinned (pkg/profilelookup cannot vouch for its registry), so it cannot match CLOWN_PROFILE_DIGEST %s; refusing", g.launch.Name, g.approvedDigest)
		case g.launch.Digest != g.approvedDigest:
			return false, fmt.Errorf("profile %q resolves to digest %s, not the CLOWN_PROFILE_DIGEST %s the launcher pinned; refusing", g.launch.Name, g.launch.Digest, g.approvedDigest)
		}
		return true, nil
	}
	if g.resumeID == "" {
		return false, nil
	}
	if g.profileEnvSet {
		return false, fmt.Errorf("CLOWN_PROFILE is set for a resume of session %s without CLOWN_PROFILE_DIGEST; refusing (a launcher that names a profile must pin its digest)", g.resumeID)
	}
	existing, ok, err := recorded(g.resumeID)
	if err != nil {
		return false, fmt.Errorf("cannot read the profile session %s was launched with: %w; refusing to resume", g.resumeID, err)
	}
	if !ok {
		if g.requested != "" {
			return false, fmt.Errorf("session %s recorded no profile; resuming it under profile %q%s is a profile change that needs approval (CLOWN_PROFILE_DIGEST from the launcher); refusing", g.resumeID, g.requested, digestSuffix(g.launch))
		}
		return false, nil
	}
	if g.launch == nil {
		return false, fmt.Errorf("session %s was launched with profile %q (digest %s); refusing to resume it without that profile", g.resumeID, existing.Name, existing.Digest)
	}
	if *g.launch != existing || existing.Digest == sessions.UnpinnableDigest {
		return false, fmt.Errorf("session %s was launched with profile %q (digest %s); this launch applies profile %q (digest %s); a profile change needs approval (CLOWN_PROFILE_DIGEST from the launcher); refusing", g.resumeID, existing.Name, existing.Digest, g.launch.Name, g.launch.Digest)
	}
	return false, nil
}

func digestSuffix(p *sessions.ProfilePin) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf(" (digest %s)", p.Digest)
}
