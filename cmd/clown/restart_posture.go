package main

import "strings"

// A restart may change the session's permission posture (clown#342): the
// relaunch runs with CLOWN_UNATTENDED=1 (claude in dontAsk, FDR 0023) or
// without it. The posture is spinclass's to decide and record (spinclass FDR
// 0037: the request names it, the preflight's verdict carries it, and
// spinclass rewrites the session's pinned resume entry in the same update, so
// the next `sc resume` agrees). clown applies the verdict's value to its own
// relaunch, whose argv and environment are this frontend's (restartArgv,
// restartEnv), not the entry's, so the restart itself agrees too.
//
// A posture in a control message is only the hand-off's argument: it is gated
// at request time like the profile (restartTriggers.posture), carried on the
// request for the log, and never acted on by itself. The control socket is
// any-same-uid, so a widening out of dontAsk must not be decidable there; who
// may ask for which posture is spinclass's rights model (narrowing is free,
// widening is operator-gated). Confined sessions are out of scope: their
// posture is the confinement profile's, and spinclass refuses the request.

// The postures a restart carries. postureKeep (absent) is today's behaviour:
// the relaunch inherits the launch environment's marker.
const (
	postureKeep       = ""
	postureUnattended = "unattended"
	postureAttended   = "attended"
)

func validPosture(p string) bool {
	return p == postureKeep || p == postureUnattended || p == postureAttended
}

// postureEnv applies posture to the relaunch's environment: unattended sets
// exactly one CLOWN_UNATTENDED=1 whatever the launch had, attended removes the
// marker, keep leaves the environment as it is.
func postureEnv(env []string, posture string) []string {
	switch posture {
	case postureUnattended:
		return append(environWithout(env, unattendedEnv), unattendedEnv+"=1")
	case postureAttended:
		return environWithout(env, unattendedEnv)
	}
	return env
}

// postureArgs applies posture to the relaunch's provider args before any `--`
// (claude's flags). unattended drops every --permission-mode and
// --inherit-permission-mode and adds exactly one `--permission-mode dontAsk`,
// mirroring spinclass's splice so argv and settings agree. attended drops
// only those that name dontAsk (claude's own flag would otherwise keep the
// mode) and nothing else: which attended mode spinclass's pinned entry
// carries is its spawn rule, and clown's relaunch runs claude's default mode
// until the next `sc resume`. keep leaves the args.
func postureArgs(args []string, posture string) []string {
	switch posture {
	case postureUnattended:
		return append(withoutPermissionMode(args, func(string) bool { return true }), "--permission-mode", unattendedMode)
	case postureAttended:
		return withoutPermissionMode(args, func(mode string) bool { return mode == unattendedMode })
	}
	return args
}

// withoutPermissionMode drops each --permission-mode/--inherit-permission-mode
// in args (either spelling, with the value in the same or the next token)
// whose value drop accepts; a flag with no value is offered "".
func withoutPermissionMode(args []string, drop func(mode string) bool) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, hasValue := strings.Cut(a, "=")
		if name == "--permission-mode" || name == "--inherit-permission-mode" {
			takesNext := !hasValue && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-")
			if takesNext {
				value = args[i+1]
			}
			if drop(value) {
				if takesNext {
					i++
				}
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

// postureCarrierArgs is the [attach] inner's carrier for a relaunch under
// posture: present for unattended, absent for attended, and for keep whatever
// this launch has (unattendedCarrierArgs).
func postureCarrierArgs(posture string) []string {
	switch posture {
	case postureUnattended:
		return []string{unattendedCarrierFlag}
	case postureAttended:
		return nil
	}
	return unattendedCarrierArgs()
}

// relaunchPostureError reports why the relaunch of flags could not take
// posture, or nil: unattended needs what unattendedLaunchError needs of the
// args as postureArgs rewrites them (a claude provider, no --naked, no
// forwarded --settings or --dangerously-skip-permissions); attended and keep
// always can. It is the request-time gate (restartTriggers.posture) and the
// pre-stop check (frontendRestarter.postureCheck), so a session that cannot
// be put in dontAsk is refused while it still runs.
func relaunchPostureError(flags parsedFlags, posture string) error {
	if posture != postureUnattended {
		return nil
	}
	f := flags
	f.forwarded = postureArgs(flags.forwarded, posture)
	return unattendedLaunchError(f, "1")
}

// restartPostureRefusal is the control reply, and the dropped-restart reason,
// for a relaunch that could not take the requested posture.
func restartPostureRefusal(posture string, err error) string {
	return "the relaunch could not take posture " + posture + ", so the session keeps running: " + err.Error()
}
