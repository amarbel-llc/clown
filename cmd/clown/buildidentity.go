package main

import (
	"fmt"
	"strings"

	"code.linenisgreat.com/clown/internal/buildcfg"
)

// clownRepoURL is the canonical repository the agent links to when it signs
// off as Clown. The build-identity fragment extends this with a /commit/<sha>
// suffix so each sign-off pins the exact build it came from.
const clownRepoURL = "https://code.linenisgreat.com/clown"

// buildIdentityFragment renders a system-prompt append fragment that tells the
// agent which clown build it is running, so it stamps the version+shortSha and
// the originating commit into its sign-offs (git commits, pull requests,
// comments). The values come from build-time ldflags in internal/buildcfg.
//
// In dev builds (go build / go run) those ldflags are empty; the fragment then
// states it is an unversioned dev build with no commit link, so the agent does
// not fabricate provenance.
func buildIdentityFragment() string {
	version := buildcfg.Version
	shortSha := buildcfg.ShortSha
	commit := buildcfg.Commit

	// No build provenance at all (plain go build / go run): say so plainly
	// rather than letting the agent invent a version or commit link.
	if version == "" && shortSha == "" && commit == "" {
		return "You are running an unversioned local dev build of clown (no " +
			"published version or commit). When signing off as Clown, do not " +
			"fabricate a version or a commit link." + pinnedComponentsSection()
	}

	id := buildIdentifier(version, shortSha)

	var b strings.Builder
	fmt.Fprintf(&b, "You are running clown %s.\n", id)
	if commit != "" {
		fmt.Fprintf(&b, "When you sign off as Clown on git commits, pull "+
			"requests, or comments, include this build identifier (%s) and link "+
			"to the exact commit it was built from: %s/commit/%s.",
			id, clownRepoURL, commit)
	} else {
		fmt.Fprintf(&b, "When you sign off as Clown on git commits, pull "+
			"requests, or comments, include this build identifier (%s).", id)
	}
	b.WriteString(pinnedComponentsSection())
	return b.String()
}

// pinnedComponentsSection renders the `clown version` table's rows after
// clown's own (claude-code, codex, ringmaster, troupe and the plugins), so an
// agent has the deployed versions without shelling out (clown#227). A
// component this build did not pin (no version) is left out; with none
// pinned the section is empty. It follows the sign-off sentence, which stays
// as it was.
func pinnedComponentsSection() string {
	var lines []string
	for _, r := range buildVersionRows()[1:] {
		switch {
		case r.version != "" && r.rev != "":
			lines = append(lines, fmt.Sprintf("- %s %s (%s)", r.component, r.version, r.rev))
		case r.version != "":
			lines = append(lines, fmt.Sprintf("- %s %s", r.component, r.version))
		case strings.ContainsAny(r.component, " \t"):
			// A plugin's preformatted version-info line ("name version rev").
			lines = append(lines, "- "+strings.Join(strings.Fields(r.component), " "))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\nComponents pinned into this build at build time (the `clown version` " +
		"table, as component version (rev)). They are authoritative for what is " +
		"deployed and running; the source HEAD in a git-status snapshot is not, " +
		"and can be ahead of the running binaries.\n" + strings.Join(lines, "\n")
}

// buildIdentifier formats the human-facing build token. With both a version
// and a short sha it is `<version>+<shortSha>` (semver build-metadata style);
// with only one it is that value; with neither it is "an unversioned dev
// build".
func buildIdentifier(version, shortSha string) string {
	switch {
	case version != "" && shortSha != "":
		return version + "+" + shortSha
	case version != "":
		return version
	case shortSha != "":
		return shortSha
	default:
		return "an unversioned dev build"
	}
}
