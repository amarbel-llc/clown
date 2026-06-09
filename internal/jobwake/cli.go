package jobwake

// LeadingArg peels a required leading positional argument (a job id) off the
// front of an arg slice before a flag set parses the rest. Go's flag package
// stops at the first non-flag token, so subcommands that take a positional id
// first must split it out. Returns ok=false when the first token is missing or
// looks like a flag. Shared by the `clown job` and `ringmaster` job front-ends.
func LeadingArg(args []string) (val string, rest []string, ok bool) {
	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		return "", args, false
	}
	return args[0], args[1:], true
}
