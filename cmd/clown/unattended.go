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

// unattendedCarrierFlag is the internal flag that carries the posture across
// the [attach] multiplexer wrap. The wrap hands the multiplexer clown's
// environment, but a multiplexer whose server already runs may start the
// inner clown with its own, which lacks the marker; the inner would then run
// attended and wait at its first dialog. The outer therefore puts this flag
// beside --clown-attach-id in the inner's argv (attachEntry), and a restart of
// that inner does the same (restartArgv). It is not a way to ask for the
// posture: run() refuses it without --clown-attach-id, and it is in no help
// or completion.
const unattendedCarrierFlag = "--clown-unattended"

// unattendedCarried is set by run() when this clown is the inner of an
// [attach] wrap whose outer was unattended.
var unattendedCarried bool

// unattendedValue is the marker's effective value: "1" when carried across
// the wrap, else the environment's.
func unattendedValue() string {
	if unattendedCarried {
		return "1"
	}
	return os.Getenv(unattendedEnv)
}

// unattendedCarrierArgs is the carrier an [attach] inner (or its restart)
// gets: the flag when this launch is unattended, else nothing.
func unattendedCarrierArgs() []string {
	if unattendedValue() == "1" {
		return []string{unattendedCarrierFlag}
	}
	return nil
}

// extractUnattendedCarrier removes the carrier from clown's own args (before
// any `--`, like extractAttachID) and reports whether it was there.
func extractUnattendedCarrier(args []string) (carried bool, rest []string) {
	rest = make([]string, 0, len(args))
	for i, a := range args {
		if a == "--" {
			return carried, append(rest, args[i:]...)
		}
		if a == unattendedCarrierFlag {
			carried = true
			continue
		}
		rest = append(rest, a)
	}
	return carried, rest
}

// unattendedCarrierError refuses the carrier outside an [attach] re-exec:
// the marker comes from the environment only (2026-10-26 ruling).
func unattendedCarrierError(carried bool, attachID string) error {
	if carried && attachID == "" {
		return fmt.Errorf("%s is internal to the [attach] re-exec and needs --clown-attach-id; set %s=1 in the environment instead; refusing", unattendedCarrierFlag, unattendedEnv)
	}
	return nil
}

// unattendedEnvTableError refuses an env table (the clownfile [profile].env,
// a named profile's env) that names the marker: the posture is the
// launcher's choice, made in clown's environment, never configuration.
func unattendedEnvTableError(source string, env map[string]string) error {
	if _, ok := env[unattendedEnv]; ok {
		return fmt.Errorf("%s sets %s, which only the launching environment may set; refusing", source, unattendedEnv)
	}
	return nil
}

// unattendedMode is the claude permission mode the marker applies.
const unattendedMode = "dontAsk"

// unattendedGate is the check runWithFlags makes once the provider and the
// forwarded args are final; a variable so a test can prove it is called.
var unattendedGate = func(flags *parsedFlags) error {
	return unattendedLaunchError(*flags, unattendedValue())
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
