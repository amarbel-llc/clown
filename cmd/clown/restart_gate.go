package main

import (
	"fmt"

	"code.linenisgreat.com/clown/internal/profile"
	"code.linenisgreat.com/clown/internal/sessions"
)

// restartProfileCheck is the relaunch's profile gate (FDR 0022, clown#296),
// run while the provider still runs: the relaunched frontend would refuse a
// changed, unresolvable or unpinnable profile only after the old one was
// stopped, leaving the session down. It checks what the relaunch will apply
// (--profile <the launch's profile>, --resume <principal>, no launcher
// approval) against the session's recorded pin, the same checkLaunchProfile.
type restartProfileCheck struct {
	// relaunch carries the relaunch's provider, profile and forwarded args.
	relaunch    parsedFlags
	attachInner bool
	// pinNow resolves a profile name to the pin it would record now.
	pinNow   func(name string) (*sessions.ProfilePin, error)
	recorded profileLookup
}

func newRestartProfileCheck(flags parsedFlags, attachedID string) restartProfileCheck {
	relaunch := parsedFlags{
		provider:  flags.provider,
		profile:   flags.profile,
		forwarded: append(withoutClaudeSessionArgs(flags.forwarded), "--resume", flags.identity.Key),
	}
	relaunch.identity = flags.identity
	return restartProfileCheck{relaunch: relaunch, attachInner: attachedID != "", pinNow: currentProfilePin, recorded: sessions.ProfileOf}
}

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
	_, err := checkLaunchProfile(gate, c.recorded)
	return err
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
