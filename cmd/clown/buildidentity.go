package main

import (
	"fmt"
	"regexp"
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

// Bounds on the pinned-components section: plugin rows are self-reported
// (each plugin's plugin.json name and version, via the flake's
// version-info), so they are shape-checked, at most maxPluginRows are
// listed, and the whole section stays within maxPinnedSectionBytes.
const (
	maxPluginRows         = 16
	maxPinnedSectionBytes = 2048
)

// pluginRowShape is a plugin version-info line after whitespace collapse:
// "<flake>/<plugin> <version> <rev>", each a plain token. A line of any
// other shape (prose, markup, extra words) is dropped.
var pluginRowShape = regexp.MustCompile(`^([A-Za-z0-9._+/-]{1,64}) ([A-Za-z0-9._+~-]{1,32}) ([A-Za-z0-9._+-]{1,64})$`)

// pinnedComponentsSection renders the `clown version` table's rows other
// than clown's own (claude-code, codex, ringmaster, troupe, then the
// plugins), so an agent can see what this build pinned without shelling out
// (clown#227). Build-time rows come first; a component this build did not
// pin (no version) is left out, and with none pinned the section is empty.
// It follows the sign-off sentence, which stays as it was.
func pinnedComponentsSection() string {
	var pinned, plugins []string
	for _, r := range buildVersionRows() {
		switch {
		case r.component == "clown":
			// The first sentence already states clown's own build.
		case r.version != "" && r.rev != "":
			pinned = append(pinned, fmt.Sprintf("- %s %s (%s)", r.component, r.version, r.rev))
		case r.version != "":
			pinned = append(pinned, fmt.Sprintf("- %s %s", r.component, r.version))
		default:
			// A plugin's version-info line, or an unpinned component (one
			// bare word), which the shape check drops.
			m := pluginRowShape.FindStringSubmatch(strings.Join(strings.Fields(r.component), " "))
			if m == nil {
				continue
			}
			version := m[2]
			if version == "-" { // no version in the plugin's plugin.json
				version = "unversioned"
			}
			plugins = append(plugins, fmt.Sprintf("- %s %s (%s)", m[1], version, m[3]))
		}
	}
	if len(pinned) == 0 && len(plugins) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nComponents pinned into this build at build time (the `clown version` " +
		"table), as component version (rev); plugin rows are self-reported by " +
		"each plugin's plugin.json. They describe this build, not the worktree: " +
		"the source HEAD in a git-status snapshot can differ from them.")
	for _, l := range pinned {
		b.WriteString("\n" + l)
	}
	// Room for the "N more" note, so it always fits.
	const noteRoom = 48
	shown := 0
	for _, l := range plugins {
		if shown == maxPluginRows || b.Len()+1+len(l)+noteRoom > maxPinnedSectionBytes {
			break
		}
		b.WriteString("\n" + l)
		shown++
	}
	if more := len(plugins) - shown; more > 0 {
		fmt.Fprintf(&b, "\n- (%d more plugin rows not shown)", more)
	}
	return b.String()
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
