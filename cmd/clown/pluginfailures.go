package main

// What a launch does when some plugin servers fail to start (clown#346).

type pluginFailureAction int

const (
	// failSkip: continue with the healthy servers (--skip-failed).
	failSkip pluginFailureAction = iota
	// failPrompt: ask the person at the terminal whether to continue.
	failPrompt
	// failAbort: stop the launch with a message naming the way to continue.
	failAbort
)

// pluginFailurePolicy decides it. A prompt needs a person to answer it: an
// unattended session (CLOWN_UNATTENDED=1) can sit on a pty, which makes it
// look interactive, yet nobody is there, so it never prompts: a restarted
// worker that put the prompt up stayed down until restarted by hand. It
// aborts instead, with a non-zero exit its holder can see; the explicit
// --skip-failed / CLOWN_SKIP_FAILED_PLUGINS=1 still wins and lets it come
// back without the failed plugin.
func pluginFailurePolicy(skipFailed, interactive, unattended bool) pluginFailureAction {
	switch {
	case skipFailed:
		return failSkip
	case interactive && !unattended:
		return failPrompt
	default:
		return failAbort
	}
}
