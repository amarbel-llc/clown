// Package clownfile reads clown's cascading per-instance config file (RFC-0013
// §1): a `clownfile` (TOML) discovered by ascending from $PWD to $HOME, with a
// deeper file overriding a shallower one per key. This package implements the
// [profile] layer (provider/backend/model/env defaults); the [attach] table is
// future work.
package clownfile

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/amarbel-llc/clown/internal/promptwalk"
)

// Filename is the per-instance config file clown discovers by ascending from
// $PWD to $HOME (RFC-0013 §1.1).
const Filename = "clownfile"

// Profile is the clownfile [profile] table: per-directory defaults for the run
// (RFC-0013 §1.2). It is a default layer beneath explicit flags/env; the
// named-profile registry (`--profile`) is a separate mechanism.
type Profile struct {
	Provider string            `toml:"provider"`
	Backend  string            `toml:"backend"`
	Model    string            `toml:"model"`
	Env      map[string]string `toml:"env"`
}

// Clownfile is one parsed clownfile.
type Clownfile struct {
	Profile Profile `toml:"profile"`
}

// Load parses a single clownfile from disk.
func Load(path string) (Clownfile, error) {
	var c Clownfile
	_, err := toml.DecodeFile(path, &c)
	return c, err
}

// Discover walks startDir up to homeDir, loading a `clownfile` from each
// ancestor and merging shallowest-first so a deeper file (closer to startDir)
// overrides per key (RFC-0013 §1.1). Absent everywhere yields the zero value
// (non-fatal); a present-but-malformed clownfile is an error.
func Discover(startDir, homeDir string) (Clownfile, error) {
	ancestors, err := promptwalk.Ancestors(startDir, homeDir)
	if err != nil {
		return Clownfile{}, err
	}
	// ancestors is deepest-first; apply shallowest-first so deeper overrides.
	var merged Clownfile
	for i := len(ancestors) - 1; i >= 0; i-- {
		path := filepath.Join(ancestors[i], Filename)
		if _, err := os.Stat(path); err != nil {
			continue // absent at this level
		}
		c, err := Load(path)
		if err != nil {
			return Clownfile{}, fmt.Errorf("clownfile %s: %w", path, err)
		}
		mergeInto(&merged, c)
	}
	return merged, nil
}

// mergeInto layers src over dst: scalar fields replace when non-empty; Env keys
// union with src winning.
func mergeInto(dst *Clownfile, src Clownfile) {
	if src.Profile.Provider != "" {
		dst.Profile.Provider = src.Profile.Provider
	}
	if src.Profile.Backend != "" {
		dst.Profile.Backend = src.Profile.Backend
	}
	if src.Profile.Model != "" {
		dst.Profile.Model = src.Profile.Model
	}
	for k, v := range src.Profile.Env {
		if dst.Profile.Env == nil {
			dst.Profile.Env = map[string]string{}
		}
		dst.Profile.Env[k] = v
	}
}
