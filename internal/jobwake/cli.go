package jobwake

// LeadingArg peels a required leading positional argument (a job id) off the
// front of an arg slice before a flag set parses the rest. Go's flag package
// stops at the first non-flag token, so subcommands that take a positional id
// first must split it out. Returns ok=false when the first token is missing or
// looks like a flag. Shared by the `ringmaster` and `troupe` job front-ends.
func LeadingArg(args []string) (val string, rest []string, ok bool) {
	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		return "", args, false
	}
	return args[0], args[1:], true
}

// ResourcesFromURIs builds resource attachments from bare URIs (the CLI form;
// the MCP surface carries the richer {uri,digest,mediaType,size} objects). Used
// by the ringmaster and troupe front-ends for `done`, `message`, and chat
// `send` (clown#112). Returns nil for an empty input.
func ResourcesFromURIs(uris []string) []Resource {
	if len(uris) == 0 {
		return nil
	}
	out := make([]Resource, 0, len(uris))
	for _, u := range uris {
		out = append(out, Resource{URI: u})
	}
	return out
}
