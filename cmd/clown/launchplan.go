package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

// launchPlan is the fully-resolved provider invocation, dumped by
// --print-launch-plan instead of being executed. It exists so the launch path
// — which is nearly pure (flags + config -> a command) — can be
// characterization-tested without spawning anything.
//
// Args order is significant and preserved. Env and Files are SORTED: both
// derive from map or directory iteration upstream, so unsorted output would
// make golden fixtures flap for reasons unrelated to the code under test.
type launchPlan struct {
	Binary string   `json:"binary"`
	Args   []string `json:"args"`
	Env    []string `json:"env"`
	// Files lists per-launch artifacts clown wrote for this invocation. It is
	// empty today: the artifacts are scattered across seven independent temp
	// dirs with no common registry, and any enumeration built against that
	// scattering would be a fragile guess. The staging-root migration
	// (docs/plans/2026-07-28-containment-primitive-design.md, part 1b) gives
	// them one home, at which point filling this in is a directory walk.
	Files []string `json:"files"`
}

// secretEnvKeyRe matches env KEYS whose value must never appear in a plan.
// Deliberately a substring match, not an exact one: over-redacting a variable
// that merely contains "key" costs nothing, while under-redacting writes a live
// credential into a committed golden fixture — permanently, since git history
// does not forget.
var secretEnvKeyRe = regexp.MustCompile(`(?i)(TOKEN|KEY|SECRET|PASSWORD)`)

const redactedValue = "<redacted>"

// redactEnvEntry replaces the VALUE of a "KEY=VALUE" entry with a placeholder
// when the KEY looks secret-bearing. Matching is on the key alone: a value that
// merely mentions "token" is usually a path, and blanking it would erase
// exactly the detail the fixtures exist to pin.
//
// An entry with no "=" has no value to leak and is returned unchanged.
func redactEnvEntry(entry string) string {
	key, _, ok := strings.Cut(entry, "=")
	if !ok {
		return entry
	}
	if !secretEnvKeyRe.MatchString(key) {
		return entry
	}
	return key + "=" + redactedValue
}

// JSON renders the plan as a single line of deterministic JSON.
//
// Redaction happens here rather than at the call site so there is no way to
// emit a plan that skipped it. Sorting operates on copies: the receiver is by
// value but slices share a backing array, and the caller is runProvider holding
// the env it would otherwise hand to a child process.
func (p launchPlan) JSON() ([]byte, error) {
	// Args is never reordered, so it is used as given.
	env := make([]string, 0, len(p.Env))
	for _, e := range p.Env {
		env = append(env, redactEnvEntry(e))
	}
	slices.Sort(env)
	p.Env = env

	files := slices.Clone(p.Files)
	slices.Sort(files)
	p.Files = files

	// nil slices marshal to `null`; normalize to `[]` so a fixture's shape does
	// not change just because a provider contributed no args or files.
	if p.Args == nil {
		p.Args = []string{}
	}
	if p.Files == nil {
		p.Files = []string{}
	}

	// An Encoder with HTML escaping off keeps "<redacted>" and any "&" in a URL
	// legible in the committed fixture; Marshal would emit < / &.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
