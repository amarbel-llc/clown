package main

import (
	"fmt"
	"os"

	"code.linenisgreat.com/clown/internal/userpath"
)

// userConfigPath resolves the per-user config file `name` (e.g.
// "profiles.toml", "opencode.toml"). Canonical location is
// $XDG_CONFIG_HOME/clown/<name> (defaulting to ~/.config/clown/<name>); when
// the canonical file is absent but the legacy ~/.config/juggler/<name> exists,
// the legacy path is returned with legacy=true so the caller can warn once.
// Writers must always use userConfigWritePath. Legacy fallback removal
// criterion: one release with the warning, no reports (design doc, 2026-07-06).
func userConfigPath(name string) (path string, legacy bool, err error) {
	return userpath.ConfigFile(name)
}

// userConfigWritePath is the canonical (always-write) location for name:
// $XDG_CONFIG_HOME/clown/<name>, ~/.config/clown/<name> when XDG is unset.
// Thin wrapper over the shared internal/userpath ladder (clown#204); the
// legacy ~/.config/juggler fallback stays here in userConfigPath — it is a
// cmd/clown config-migration concern, not part of the ladder.
func userConfigWritePath(name string) (string, error) {
	return userpath.ConfigPath(name)
}

// userStateWritePath is the canonical location for per-user STATE (as opposed
// to config): $XDG_STATE_HOME/clown/<parts...>. Thin wrapper over the shared
// internal/userpath ladder (clown#204).
func userStateWritePath(parts ...string) (string, error) {
	return userpath.StatePath(parts...)
}

// legacyConfigWarned dedupes warnLegacyConfig per path — several read sites
// (profiles, opencode, crush) may resolve to legacy files in one launch.
var legacyConfigWarned = map[string]bool{}

// warnLegacyConfig emits the one-line legacy-path warning, once per file.
func warnLegacyConfig(path string) {
	if legacyConfigWarned[path] {
		return
	}
	legacyConfigWarned[path] = true
	fmt.Fprintf(os.Stderr, "clown: reading legacy config %s; move it to ~/.config/clown/ (the TUI writes there)\n", path)
}
