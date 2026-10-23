package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"code.linenisgreat.com/clown/internal/sessions"
	"code.linenisgreat.com/clown/pkg/profilelookup"
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
	// approvedDigest is CLOWN_PROFILE_DIGEST (consumeLauncherProfileEnv),
	// "" when unset.
	approvedDigest string
	// approvedSession is CLOWN_PROFILE_SESSION (consumeLauncherProfileEnv),
	// "" when unset: the one session the launcher's approval is for.
	approvedSession string
	// launch is the applied profile's pin (launchProfilePin); nil when the
	// launch applies no profile.
	launch *sessions.ProfilePin
	// unseenResume names a claude resume form whose session id clown cannot
	// see (unseenResumeForm); "" when none.
	unseenResume string
	// resumeValue is the --resume/-r value as given (claudeResumeValue), so
	// a refusal can name a non-UUID one; "" when none.
	resumeValue string
	// restored marks a profile restored from the session's pin by `clown
	// resume` (flags.profileFromRecord).
	restored bool
	// describeChange, when set, explains a pin change in the refusal
	// (describeProfileChange); nil adds nothing.
	describeChange func(old, new sessions.ProfilePin) string
}

// profileDiffLines caps the field diff a refusal prints.
const profileDiffLines = 6

// describeProfileChange explains a change from the pinned profile old to new
// for a refusal (FDR 0022 slice 3b), in up to profileDiffLines diff lines.
// When old's name still resolves to old's digest (a switch to another
// profile while the pinned one is unchanged) it diffs the two definitions
// (profilelookup.Diff). Otherwise, as after a same-name edit, it diffs the
// redacted form stored with the pin (clown#298, profilelookup.DiffRedacted),
// which cannot show env value changes. Without a stored form it says the old
// definition is not recorded, rather than inventing a diff. resolve is
// profilelookup.Resolve bound to the registry and storedForm
// profilelookup.RedactedFormByDigest (parameters for tests).
func describeProfileChange(old, new sessions.ProfilePin, resolve func(name string) (*profilelookup.Resolved, error), storedForm func(digest string) ([]byte, bool, error)) string {
	const unreadableNew = "the new definition could not be re-read, so no field diff is shown"
	readNew := func() (*profilelookup.Resolved, bool) {
		n, err := resolve(new.Name)
		return n, err == nil && n.DigestSHA256 == new.Digest
	}
	if o, err := resolve(old.Name); err == nil && o.DigestSHA256 == old.Digest {
		n, ok := readNew()
		if !ok {
			return unreadableNew
		}
		lines, _ := profilelookup.Diff(o, n, profileDiffLines)
		if len(lines) == 0 {
			return "only fields the diff does not show (the display name) differ"
		}
		return "changes:\n  " + strings.Join(lines, "\n  ")
	}
	oldForm, ok, err := storedForm(old.Digest)
	if err != nil || !ok {
		return "the previous definition is not recorded (this pin stored no redacted form of it), so no field diff is shown"
	}
	n, ok := readNew()
	if !ok {
		return unreadableNew
	}
	newForm, err := n.RedactedForm()
	if err != nil {
		return unreadableNew
	}
	lines, _, err := profilelookup.DiffRedacted(oldForm, newForm, profileDiffLines)
	switch {
	case err != nil:
		return fmt.Sprintf("the redacted form recorded with the pin is unreadable (%v), so no field diff is shown", err)
	case len(lines) == 0:
		return "only fields the recorded redacted form does not show (the display name, env values, a url that may carry a credential) differ"
	}
	return "changes (against the redacted form recorded with the pin; env value changes are not shown):\n  " + strings.Join(lines, "\n  ")
}

// withChange appends g.describeChange's explanation of old -> new to msg.
func (g profileGate) withChange(msg string, old, new sessions.ProfilePin) string {
	if g.describeChange == nil {
		return msg
	}
	if d := g.describeChange(old, new); d != "" {
		return msg + "\n" + d
	}
	return msg
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
// unit).
//
// Rules, in order:
//   - CLOWN_PROFILE_SESSION set: it must equal the gated session id
//     (gatedSessionID); a fresh launch or another session is refused. It
//     binds the approval to that session.
//   - CLOWN_PROFILE_DIGEST set: the applied profile must resolve to exactly
//     that digest (fresh launch or resume). A match is an approval.
//   - Fresh launch otherwise: unchanged.
//   - Resume with CLOWN_PROFILE set but no CLOWN_PROFILE_DIGEST: refused
//     (decision 8, amended), unless it names the session's pin exactly. A
//     launcher approves with --profile plus the digest, and clown consumes
//     CLOWN_PROFILE so no child inherits it, so this case is a user's own
//     shell export, where naming the unchanged pin changes nothing.
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
// the pin comparison for the session CLOWN_PROFILE_SESSION names; without
// it (until spinclass sends one) they skip it for both identity.Key and the
// claude id, so an unbound approval is not tied to one session.
//
// First of all: claude --continue/-c, a bare --resume/-r (claude's own
// picker) and --from-pr resume a conversation whose id clown never learns,
// so a launch that applies a profile refuses them (slice 3a); without a
// profile they pass through.
//
// recorded's error fails the resume closed. Since every fresh claude launch
// carries a --session-id (decideClaudeSession), an unreadable session-names
// sidecar blocks fresh claude launches too (gatedSessionID): availability
// now depends on that file being readable.
func checkLaunchProfile(g profileGate, recorded profileLookup) (profileDecision, error) {
	if g.unseenResume != "" && g.launch != nil {
		hint := ""
		if g.requested == "" && !g.restored {
			// resolveProfileName: an explicit --provider suppresses the
			// clownfile pin and the build default, so no profile applies.
			hint = fmt.Sprintf(". Profile %q was defaulted (clownfile pin or build default); passing --provider claude turns profile defaulting off, so claude's own picker still works without a profile", g.launch.Name)
		}
		if (g.unseenResume == "--resume" || g.unseenResume == "-r") && g.resumeValue != "" {
			return profileDecision{}, fmt.Errorf("the %s value %q is not a UUID: claude reads it as a search term, not a session id, so clown cannot check that session's profile (%q); refusing%s", g.unseenResume, g.resumeValue, g.launch.Name, hint)
		}
		return profileDecision{}, fmt.Errorf("%s resumes a session clown cannot identify, so it cannot check that session's profile (%q); refusing. Resume it by id instead: clown resume <id> (or pass --resume <id>)%s", g.unseenResume, g.launch.Name, hint)
	}
	if g.approvedSession != "" && g.approvedSession != g.resumeID {
		which := "a fresh launch"
		if g.resumeID != "" {
			which = "session " + g.resumeID
		}
		return profileDecision{}, fmt.Errorf("CLOWN_PROFILE_SESSION is %s, but this launch is %s; refusing", g.approvedSession, which)
	}
	if g.approvedDigest != "" {
		switch {
		case g.launch == nil:
			return profileDecision{}, fmt.Errorf("CLOWN_PROFILE_DIGEST is set (%s) but no profile is applied; refusing", g.approvedDigest)
		case g.launch.Digest == sessions.UnpinnableDigest:
			return profileDecision{}, fmt.Errorf("profile %q cannot be pinned (pkg/profilelookup cannot vouch for its registry), so it cannot match CLOWN_PROFILE_DIGEST %s; refusing", g.launch.Name, g.approvedDigest)
		case g.launch.Digest != g.approvedDigest:
			return profileDecision{}, fmt.Errorf("profile %q resolves to digest %s, not the CLOWN_PROFILE_DIGEST %s the launcher pinned; refusing", g.launch.Name, g.launch.Digest, g.approvedDigest)
		}
		d := profileDecision{approved: true, approvedSession: g.approvedSession}
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
		return profileDecision{}, errors.New(g.withChange(fmt.Sprintf("session %s was launched with profile %q (digest %s); this launch applies profile %q (digest %s); a profile change needs approval (CLOWN_PROFILE_DIGEST from the launcher); refusing", g.resumeID, existing.Name, existing.Digest, g.launch.Name, g.launch.Digest), existing, *g.launch))
	}
	return profileDecision{}, nil
}

// profileDecision is what checkLaunchProfile allowed.
type profileDecision struct {
	// approved: the launcher's CLOWN_PROFILE_DIGEST matched the applied
	// profile.
	approved bool
	// approvedSession is the session the approval is bound to
	// (CLOWN_PROFILE_SESSION); "" when unbound.
	approvedSession string
	// pinEarly is the pin to record for the gated session before any
	// [attach] re-exec (an approved pin, or a trust-on-first-use pin); nil
	// for none. The write must succeed or the launch is refused.
	pinEarly *sessions.ProfilePin
}

// approvesRecordFor reports whether the approval lets a record for id
// replace a different pin: an unbound approval (no CLOWN_PROFILE_SESSION,
// the behavior until spinclass sends one) covers every id the launch writes,
// a bound one only its session.
func (d profileDecision) approvesRecordFor(id string) bool {
	return d.approved && (d.approvedSession == "" || d.approvedSession == id)
}

// newProfileGate builds the gate for a launch. flagProfile is --profile or
// CLOWN_PROFILE as parseFlags read them (before clownfile/default
// resolution). attachInner marks the inner clown of an [attach] re-exec: its
// --profile comes from the outer's reexecArgv, which emits the resolved name
// even when it was defaulted, and the outer already ran this gate, so the
// inner does not count it as a request (rules that compare against a pin
// still apply).
func newProfileGate(flags parsedFlags, flagProfile string, env launcherProfileEnv, launch *sessions.ProfilePin, attachInner bool, recorded profileLookup) profileGate {
	g := profileGate{
		profileEnvSet:   env.profile != "",
		approvedDigest:  env.digest,
		approvedSession: env.session,
		launch:          launch,
	}
	if !flags.profileFromRecord && !attachInner {
		g.requested = flagProfile
	}
	g.restored = flags.profileFromRecord
	if flags.provider == "claude" {
		g.resumeID = gatedSessionID(flags.forwarded, recorded)
		g.unseenResume = unseenResumeForm(flags.forwarded)
		g.resumeValue = claudeResumeValue(flags.forwarded)
	}
	return g
}

// unseenResumeForm returns the claude flag in args that resumes a session
// without naming an id clown can read: -c/--continue, --from-pr, or a
// --resume/-r that does not name a session (resumeArg). "" when there is
// none.
func unseenResumeForm(args []string) string {
	for _, a := range args {
		switch {
		case a == "-c" || a == "--continue":
			return a
		case a == "--from-pr" || strings.HasPrefix(a, "--from-pr="):
			return "--from-pr"
		}
	}
	if flag, id := resumeArg(args); flag != "" && id == "" {
		return flag
	}
	return ""
}

// resumeArgRaw reads the first claude --resume/-r in args: flag is the
// spelling found ("" when there is none) and value what follows it ("" for
// no value, an empty value, or a following flag, which is taken as no
// value). It is the one parser behind both readings below.
func resumeArgRaw(args []string) (flag, value string) {
	for i, a := range args {
		switch {
		case a == "--resume" || a == "-r":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				return a, args[i+1]
			}
			return a, ""
		case strings.HasPrefix(a, "--resume="):
			return "--resume", strings.TrimPrefix(a, "--resume=")
		case strings.HasPrefix(a, "-r="):
			return "-r", strings.TrimPrefix(a, "-r=")
		}
	}
	return "", ""
}

// resumeArg is the strict reading the profile check uses: id is set only for
// a UUID value (claude session ids are UUIDs). No value, an empty value, a
// search term or a following flag open claude's picker, so id is "" and
// unseenResumeForm reports the flag, refused when a profile applies.
func resumeArg(args []string) (flag, id string) {
	flag, v := resumeArgRaw(args)
	if isUUID(v) {
		return flag, v
	}
	return flag, ""
}

// claudeResumeID returns the session a claude --resume/-r names (a UUID), or
// "" (resumeArg). The profile check uses it.
func claudeResumeID(args []string) string {
	_, id := resumeArg(args)
	return id
}

// claudeResumeValue is the lenient reading used where no profile is at stake
// (decideClaudeSession, the [attach] mode, the group-id command): any
// non-empty value, as before the profile check existed, except that a
// following flag is never taken as the value. A launch that applies a
// profile and passes a non-UUID value is refused by the profile check before
// any of that matters.
func claudeResumeValue(args []string) string {
	_, v := resumeArgRaw(args)
	return v
}

// claudeSessionIDArg returns a claude --session-id value, or "" when there is
// none or the next token is another flag. claude itself validates the value.
func claudeSessionIDArg(args []string) string {
	v := claudeFlagValue(args, "--session-id")
	if strings.HasPrefix(v, "-") {
		return ""
	}
	return v
}

// claudeSessionRef is the session a claude launch names: an explicit
// --session-id, else the --resume/-r value (lenient, claudeResumeValue). It
// selects the [attach] resume template and feeds the group-id command.
func claudeSessionRef(args []string) string {
	return cmpOr(claudeSessionIDArg(args), claudeResumeValue(args))
}

// gatedSessionID returns the existing session a claude launch continues, for
// checkLaunchProfile: the --resume/-r id (every `clown resume` form passes
// one), else a --session-id that already carries a pin or whose record
// cannot be read. Call it after decideClaudeSession, which turns
// CLOWN_SESSION_ID/CLAUDE_SESSION_ID (identity.Key) into an injected
// --session-id, so `clown -- --session-id <pinned>` and
// CLOWN_SESSION_ID=<pinned> are gated like a resume. A --session-id with no
// pin is a fresh launch. --continue/-c, bare --resume and --from-pr carry no
// id; checkLaunchProfile refuses them when a profile is applied
// (unseenResumeForm).
func gatedSessionID(forwarded []string, recorded profileLookup) string {
	if id := claudeResumeID(forwarded); id != "" {
		return id
	}
	id := claudeSessionIDArg(forwarded)
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

// launcherProfileEnv is the profile environment a launch was given:
// CLOWN_PROFILE (a user's own request; launchers pass --profile instead),
// CLOWN_PROFILE_DIGEST and CLOWN_PROFILE_SESSION (a launcher's approval).
type launcherProfileEnv struct {
	profile, digest, session string
}

// consumeLauncherProfileEnv returns the profile environment and removes it
// from this process's environment, so the provider, an [attach] re-exec
// child and any nested clown inherit neither a profile request nor the
// launcher's approval (an agent in the session could otherwise re-pin
// another session with it, and a nested resume would trip decision 8).
// Nothing reads CLOWN_PROFILE after this point: parseFlags and
// launchResume have already read it, the re-exec child gets --profile in
// its argv (reexecArgv), and the agent-scope unit's env allow-list never
// carried it. The approved pin is recorded before any re-exec, so the inner
// clown needs no digest either.
func consumeLauncherProfileEnv() launcherProfileEnv {
	env := launcherProfileEnv{
		profile: os.Getenv("CLOWN_PROFILE"),
		digest:  os.Getenv("CLOWN_PROFILE_DIGEST"),
		session: os.Getenv("CLOWN_PROFILE_SESSION"),
	}
	for _, k := range []string{"CLOWN_PROFILE", "CLOWN_PROFILE_DIGEST", "CLOWN_PROFILE_SESSION"} {
		_ = os.Unsetenv(k)
	}
	return env
}

// warnTruncatedSidecar writes one warning line to w when the session-names
// sidecar ends in an incomplete record (a crash mid-write). It never fails
// the launch: the next record starts on a fresh line, and a fragment that
// could be this session's pin is refused by sessions.ProfileOf anyway.
func warnTruncatedSidecar(w io.Writer) {
	path, truncated, err := sessions.TruncatedTail()
	if err != nil || !truncated {
		return
	}
	fmt.Fprintf(w, "clown: warning: %s ends in an incomplete record (an interrupted write?); later records are unaffected\n", path)
}

func digestSuffix(p *sessions.ProfilePin) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf(" (digest %s)", p.Digest)
}
