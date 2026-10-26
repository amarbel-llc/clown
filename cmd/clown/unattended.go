package main

import (
	"fmt"
	"os"
	"strings"
)

// unattendedEnv is the marker for the unattended permission posture
// (clown#323, FDR 0023): exactly "1" puts claude in dontAsk through the
// --settings JSON (claudeSafetySettingsJSON). It is read from clown's own
// environment at every launch and never recorded, so a restart (which keeps
// the launch environment) stays unattended and a resume follows the
// environment it is run in.
const unattendedEnv = "CLOWN_UNATTENDED"

// unattendedMode is the claude permission mode the marker applies.
const unattendedMode = "dontAsk"

// unattendedGate is the check runWithFlags makes once the provider and the
// forwarded args are final; a variable so a test can prove it is called.
var unattendedGate = func(flags *parsedFlags) error {
	return unattendedLaunchError(*flags, os.Getenv(unattendedEnv))
}

// unattendedRequested reads the marker's value: "1" applies the posture,
// unset or empty does not, and anything else is refused, so a misspelt
// marker never leaves a session the operator believes unattended waiting at
// a permission dialog.
func unattendedRequested(value string) (bool, error) {
	switch value {
	case "":
		return false, nil
	case "1":
		return true, nil
	}
	return false, fmt.Errorf("%s=%q: the only accepted value is 1 (unset it to launch attended); refusing", unattendedEnv, value)
}

// unattendedLaunchError refuses a launch the marker cannot put in dontAsk:
// a provider other than claude, --naked (claude gets no --settings), and
// forwarded claude flags that choose another mode or bring their own
// settings. A forwarded --permission-mode dontAsk (or
// --inherit-permission-mode dontAsk) agrees with the marker and is allowed:
// spinclass splices it into its confined spawns.
func unattendedLaunchError(flags parsedFlags, value string) error {
	on, err := unattendedRequested(value)
	if err != nil || !on {
		return err
	}
	refuse := func(why string) error {
		return fmt.Errorf("%s=1 puts claude in %s, but %s; refusing", unattendedEnv, unattendedMode, why)
	}
	if flags.provider != "claude" {
		return refuse(fmt.Sprintf("the provider is %s, which has no such mode clown can set", flags.provider))
	}
	if flags.naked {
		return refuse("--naked passes claude no --settings")
	}
	args := flags.forwarded
	for i, a := range args {
		name, value, hasValue := strings.Cut(a, "=")
		switch name {
		case "--permission-mode", "--inherit-permission-mode":
			if !hasValue {
				value = ""
				if i+1 < len(args) {
					value = args[i+1]
				}
			}
			if value != unattendedMode {
				return refuse(fmt.Sprintf("the provider args pass %s %q", name, value))
			}
		case "--dangerously-skip-permissions", "--allow-dangerously-skip-permissions":
			return refuse("the provider args pass " + name)
		case "--settings", "--setting-sources":
			return refuse("the provider args pass " + name + ", which could set another mode")
		}
	}
	return nil
}
