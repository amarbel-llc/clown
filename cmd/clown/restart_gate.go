package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"code.linenisgreat.com/clown/internal/0/profile"
	"code.linenisgreat.com/clown/internal/alfa/sessions"
	"code.linenisgreat.com/clown/pkg/profilelookup"
)

// restartProfileCheck is the relaunch's profile gate (FDR 0022, clown#296),
// run while the provider still runs: the relaunched frontend would refuse a
// changed, unresolvable or unpinnable profile only after the old one was
// stopped, leaving the session down. It checks what the relaunch will apply
// (--profile <the launch's profile>, --resume <principal>, no launcher
// approval) against the session's recorded pin, the same checkLaunchProfile,
// with the same field diff and provenance in its refusal.
//
// check runs that gate in THIS binary; checkWithPathClown (clown#340) then
// also asks the clown on PATH, the binary the relaunch execs, for the digest
// it computes (`clown profile digest`): a digest is a function of the
// definition and the build, so a build that computes another digest for the
// same registry (a changed builtin the profile is based on, a changed
// canonical form) would pass here and refuse after the stop.
type restartProfileCheck struct {
	// relaunch carries the relaunch's provider, profile and forwarded args.
	relaunch    parsedFlags
	attachInner bool
	// pinNow resolves a profile name to the pin it would record now.
	pinNow   func(name string) (*sessions.ProfilePin, error)
	recorded profileLookup
	// describeChange and describeProvenance are the gate's refusal detail
	// (profileGate's); nil adds nothing.
	describeChange     func(old, new sessions.ProfilePin) string
	describeProvenance func(id string) string
	// pathDigest asks the clown on PATH for the digest it computes for a
	// profile (pathProfileDigest): supported is false when that clown has
	// no `profile digest` (an older build) or there is none on PATH. nil
	// skips the step.
	pathDigest func(profile string) (digest string, supported bool, err error)
	// pathClown names the binary pathDigest asks, for the refusal.
	pathClown string
}

func newRestartProfileCheck(flags parsedFlags, attachedID, registryPath string) restartProfileCheck {
	relaunch := parsedFlags{
		provider:  flags.provider,
		profile:   flags.profile,
		forwarded: append(withoutClaudeSessionArgs(flags.forwarded), "--resume", flags.identity.Key),
	}
	relaunch.identity = flags.identity
	c := restartProfileCheck{
		relaunch:    relaunch,
		attachInner: attachedID != "",
		pinNow:      currentProfilePin,
		recorded:    sessions.ProfileOf,
		describeChange: func(old, new sessions.ProfilePin) string {
			return describeProfileChange(old, new, func(name string) (*profilelookup.Resolved, error) {
				return profilelookup.Resolve(name, profilelookup.Options{RegistryPath: registryPath})
			}, profilelookup.RedactedFormByDigest)
		},
		describeProvenance: pinProvenanceLine,
		pathClown:          lookPathOrEmpty("clown"),
	}
	c.pathDigest = pathProfileDigest(c.pathClown, launchEnviron, 0)
	return c
}

// check runs the relaunch's profile gate in this binary.
func (c restartProfileCheck) check() error {
	var launch *sessions.ProfilePin
	if c.relaunch.profile != "" {
		pin, err := c.pinNow(c.relaunch.profile)
		if err != nil {
			return err
		}
		launch = pin
	}
	gate := newProfileGate(c.relaunch, c.relaunch.profile, launcherProfileEnv{}, launch, c.attachInner, c.recorded)
	gate.describeChange = c.describeChange
	gate.describeProvenance = c.describeProvenance
	_, err := checkLaunchProfile(gate, c.recorded)
	return err
}

// checkWithPathClown is check, then the same comparison with the digest the
// clown on PATH computes (the binary the relaunch will exec): a pinned
// session whose profile that clown resolves to another digest is refused
// now, naming both builds, instead of after the stop. A PATH clown without
// `profile digest` (an older build), or none, leaves only this binary's
// verdict; one that fails to answer refuses, since the relaunch would run
// it. Nothing is asked for a launch without a profile, an unpinned session
// or an unpinnable pin (the gate above has already decided those).
func (c restartProfileCheck) checkWithPathClown() error {
	if err := c.check(); err != nil {
		return err
	}
	if c.pathDigest == nil || c.relaunch.profile == "" {
		return nil
	}
	pin, ok, err := c.recorded(c.relaunch.identity.Key)
	if err != nil || !ok || pin.Digest == sessions.UnpinnableDigest {
		return nil
	}
	digest, supported, err := c.pathDigest(c.relaunch.profile)
	switch {
	case err != nil:
		return fmt.Errorf("the clown on PATH (%s), which the relaunch would exec, could not resolve profile %q: %v", c.pathClown, c.relaunch.profile, err)
	case !supported:
		return nil
	case digest != pin.Digest:
		msg := fmt.Sprintf("the clown on PATH (%s), which the relaunch would exec, resolves profile %q to digest %s, but session %s is pinned to digest %s, so the relaunch would refuse to resume under a changed profile; this frontend's own build resolves it to the pinned digest, so the two builds compute it differently (restore the profile's previous definition under the new build, or have the launcher approve the new digest with CLOWN_PROFILE_DIGEST)", c.pathClown, c.relaunch.profile, digest, c.relaunch.identity.Key, pin.Digest)
		if c.describeProvenance != nil {
			if p := c.describeProvenance(c.relaunch.identity.Key); p != "" {
				msg += "\n" + p
			}
		}
		return errors.New(msg)
	}
	return nil
}

// pathDigestTimeout bounds `clown profile digest` on PATH's clown.
const pathDigestTimeout = 10 * time.Second

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// pathProfileDigest asks bin (the clown on PATH) for the digest it computes
// for a profile, with `clown profile digest <name>` under the relaunch's
// environment (the launch environment minus the keys the relaunch drops, so
// it reads the registry the relaunch will). An empty bin, or a clown that
// answers the verb with a usage error (exit 2: an older build), is
// unsupported. Exit 1 (the profile cannot be pinned there) or any other
// failure is an error carrying what the clown said. timeout zero is
// pathDigestTimeout.
func pathProfileDigest(bin string, environ []string, timeout time.Duration) func(profile string) (string, bool, error) {
	if timeout <= 0 {
		timeout = pathDigestTimeout
	}
	return func(profile string) (string, bool, error) {
		if bin == "" {
			return "", false, nil
		}
		res, err := runBounded(bin, []string{"profile", "digest", profile}, environWithout(environ, restartEnvDropped...), timeout)
		switch {
		case err != nil:
			return "", true, fmt.Errorf("%s profile digest %s: %w", bin, profile, err)
		case res.exitCode == 2:
			return "", false, nil
		case res.exitCode != 0:
			return "", true, fmt.Errorf("%s profile digest %s exited %d: %s", bin, profile, res.exitCode, res.stderrLine())
		}
		digest := strings.TrimSpace(string(res.stdout))
		if !sha256Hex.MatchString(digest) {
			return "", true, fmt.Errorf("%s profile digest %s printed %q, not a digest", bin, profile, digest)
		}
		return digest, true, nil
	}
}

// currentProfilePin resolves name as the relaunch's runWithFlags would
// (registry, Validate, the base check) and returns the pin it would record.
func currentProfilePin(name string) (*sessions.ProfilePin, error) {
	builtin, user, path, err := loadProfileSets("")
	if err != nil {
		return nil, fmt.Errorf("loading profiles: %w", err)
	}
	for _, p := range profile.Merge(builtin, user) {
		if p.Name != name {
			continue
		}
		if err := profile.Validate(p); err != nil {
			return nil, fmt.Errorf("invalid profile: %w", err)
		}
		if err := verifyProfileBase(p, path); err != nil {
			return nil, err
		}
		pin, _, _ := launchProfilePin(&p, path)
		return &pin, nil
	}
	return nil, fmt.Errorf("unknown profile %q", name)
}
