// Package clownfile reads clown's cascading per-instance config file (RFC-0013
// §1): a `clownfile` (TOML) discovered by ascending from $PWD to $HOME, with a
// deeper file overriding a shallower one per key. It implements the [profile]
// layer (provider/backend/model/env defaults, §1.2) and the [attach] layer
// (multiplexer self-wrap templates, §1.3).
package clownfile

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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

// Attach is the clownfile [attach] table: the clown-owned multiplexer/attach
// layer (RFC-0013 §1.3) that subsumes spinclass's former [session-entry] mux
// templates. clown EXECUTES Start/Resume (self-wrap on boot); Spawn/SpawnEntry/
// SpawnWindow are parsed and held as the single-source schema but their executor
// is unresolved (RFC §1.3 open question), so this revision does not run them.
type Attach struct {
	Multiplexer string   `toml:"multiplexer"`  // "zmx" | "none"
	Start       []string `toml:"start"`        // fresh interactive self-wrap argv
	Resume      []string `toml:"resume"`       // reattach argv
	ResumeTitle string   `toml:"resume-title"` // OSC-2 title emitted before a resume attach
	Spawn       []string `toml:"spawn"`        // detached-worker launch (schema-only this revision)
	SpawnEntry  []string `toml:"spawn-entry"`  // harness argv a spawned worker boots (schema-only)
	SpawnWindow []string `toml:"spawn-window"` // fire-and-forget window opener (schema-only)
}

// AttachMode selects which executed template Resolve renders.
type AttachMode int

const (
	// ModeStart is a fresh interactive launch (the Start template).
	ModeStart AttachMode = iota
	// ModeResume is a reattach (the Resume template).
	ModeResume
)

// Enabled reports whether [attach] requests a multiplexer wrap. "" and "none"
// mean run inline (RFC-0013 §1.3 rule 1).
func (a Attach) Enabled() bool {
	return a.Multiplexer != "" && a.Multiplexer != "none"
}

// placeholderRe matches a {placeholder} token so Resolve can reject any that
// survives substitution (RFC-0013 §1.3 rule 2).
var placeholderRe = regexp.MustCompile(`\{[a-zA-Z][a-zA-Z0-9_-]*\}`)

// Resolve renders the executed template for mode into a concrete multiplexer
// argv (RFC-0013 §1.3): the exact element "{entry}" is replaced by splicing the
// entry argv into that position, and "{id}" is string-substituted within any
// element. Only {id}/{entry} are available for the interactive Start/Resume
// modes; any other surviving {...} placeholder (e.g. {prompt}/{dir}, or an
// unknown token) is rejected with a diagnostic. Returns an error when the
// multiplexer is not enabled or the selected template is empty.
func (a Attach) Resolve(mode AttachMode, id string, entry []string) ([]string, error) {
	if a.Multiplexer != "zmx" && a.Multiplexer != "none" {
		return nil, fmt.Errorf("clownfile [attach]: multiplexer must be \"zmx\" or \"none\", got %q", a.Multiplexer)
	}
	if !a.Enabled() {
		return nil, fmt.Errorf("clownfile [attach]: multiplexer is %q; no wrap", a.Multiplexer)
	}
	var tmpl []string
	switch mode {
	case ModeStart:
		tmpl = a.Start
	case ModeResume:
		tmpl = a.Resume
	default:
		return nil, fmt.Errorf("clownfile [attach]: unknown mode %d", mode)
	}
	if len(tmpl) == 0 {
		return nil, fmt.Errorf("clownfile [attach]: empty template for the requested mode")
	}
	out := make([]string, 0, len(tmpl)+len(entry))
	for _, el := range tmpl {
		if el == "{entry}" {
			out = append(out, entry...)
			continue
		}
		s := strings.ReplaceAll(el, "{id}", id)
		if m := placeholderRe.FindString(s); m != "" {
			return nil, fmt.Errorf("clownfile [attach]: unrecognized or unavailable placeholder %s in %q", m, el)
		}
		out = append(out, s)
	}
	return out, nil
}

// Title renders ResumeTitle with {id} substituted, for emission as an OSC-2
// terminal title before a resume attach (RFC-0013 §1.3 rule 4). Empty when no
// title is configured.
func (a Attach) Title(id string) string {
	return strings.ReplaceAll(a.ResumeTitle, "{id}", id)
}

// Clownfile is one parsed clownfile.
type Clownfile struct {
	Profile Profile `toml:"profile"`
	Attach  Attach  `toml:"attach"`
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

	// [attach]: scalars replace when non-empty; argv templates replace wholesale
	// when set (a deeper clownfile's template wins as a unit — argv lists do not
	// merge element-wise).
	if src.Attach.Multiplexer != "" {
		dst.Attach.Multiplexer = src.Attach.Multiplexer
	}
	if src.Attach.ResumeTitle != "" {
		dst.Attach.ResumeTitle = src.Attach.ResumeTitle
	}
	if src.Attach.Start != nil {
		dst.Attach.Start = src.Attach.Start
	}
	if src.Attach.Resume != nil {
		dst.Attach.Resume = src.Attach.Resume
	}
	if src.Attach.Spawn != nil {
		dst.Attach.Spawn = src.Attach.Spawn
	}
	if src.Attach.SpawnEntry != nil {
		dst.Attach.SpawnEntry = src.Attach.SpawnEntry
	}
	if src.Attach.SpawnWindow != nil {
		dst.Attach.SpawnWindow = src.Attach.SpawnWindow
	}
}
