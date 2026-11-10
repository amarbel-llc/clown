// Package profilerules is RFC 0019's named ruleset for capability profiles:
// what a profile may write, beyond what ajour's parser accepts. Rules is the
// rule table (id, severity, rule, message template); vectors.json is one
// document per case with the findings it must produce. RFC 0019 is
// normative.
//
// TODO(#349): Validate(document) []Finding, once cutting-garden's pkgs/ajour
// parser can be a clown flake input; it runs every vector here.
package profilerules

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed vectors.json
var vectorsJSON []byte

// Severities.
const (
	Refuse = "refuse" // blocks the bless; the lint verb exits 1
	Warn   = "warn"   // shown at the bless prompt; does not block
)

// Layers a vector's body is checked as.
const (
	LayerStencil = "stencil"
	LayerPatch   = "patch"
)

// RuleSpec is one row of RFC 0019 §2 to §4. Message is a template: the
// placeholders in angle brackets are filled per finding.
type RuleSpec struct {
	ID       string
	Severity string
	Rule     string
	Message  string
}

// Rules is the ruleset, in RFC 0019's order.
var Rules = []RuleSpec{
	{
		"R-1", Refuse, "the body parses as the stencil fragment (ajour.ParseStencil; a patch: ajour.ParsePatch)",
		"R-1: <ajour's parse error>",
	},
	{
		"R-2", Refuse, "a _ node carries !type",
		"R-2: [_ …] has no !type: the null id matches by type and atoms, so say which type (e.g. [-> _ !tool-input-v1 open])",
	},
	{
		"R-3", Refuse, "a + node carries !type",
		"R-3: [+ …] has no !type: a creation allowance names the type it creates",
	},
	{
		"R-4", Refuse, "an atom-less typed _ node in a stencil carries the tag open, on every level",
		"R-4: [_ !<type>] has no atoms and no open tag: write [_ !<type> open] to admit every <type>, or add atoms",
	},
	{
		"R-5", Refuse, "a _ node on the plugin or server level appears only on a chain whose every leaf carries deny",
		"R-5: [_ !<type>] admits through every <plugin|server>: name it, or end every branch beneath it in deny",
	},
	{
		"R-6", Refuse, "no atom names the verb create",
		"R-6: op=create at <node>: creation is spelled by a + node ([-> + !<type> …]), never by op=create",
	},
	{
		"R-7", Refuse, "every atom names a field the node's type carries (RFC 0018 §1, §3); plugin cg types unchecked",
		"R-7: <field> is not a field of <type> (it carries: <fields>); an atom on a missing field never holds",
	},
	{
		"R-8", Refuse, "op= values other than create (R-6's) are among read, patch, put, delete",
		"R-8: op=<v> at <node>: verbs are read, patch, put, delete",
	},
	{
		"R-9", Refuse, "no tool-arg-v1 node",
		"R-9: tool-arg-v1 is retired: write [-> _ !tool-input-v1 field=<arg> value=[…]] beside [-> _ !tool-input-v1 ^field=<arg> open]",
	},
	{
		"R-10", Refuse, "no cg-roots-v1 or cg-node-v1 node",
		"R-10: <type> is retired: hang cg roots under [-> resources !mcp-resources-v1] and give each step the plugin's own type (or none)",
	},
	{
		"R-11", Refuse, "a locked id's digest is a valid blake2b256 markl id",
		"R-11: <id>@<digest> is not a valid markl id (<reason>): copy the lock from the catalog listing",
	},
	{
		"R-12", Refuse, "a resource-step node that admits a subject by itself (a leaf of its allowance, or subtree) and is not deny carries op=; pass-through container steps, + and cg-field-v1 exempt",
		"R-12: <node> admits every verb: say which, e.g. op=[read]",
	},
	{
		"R-13", Refuse, "a cg-field-v1 node's parent admits op=patch or op=put",
		"R-13: cg-field-v1 under <parent> is unreachable unless <parent> admits op=patch or op=put",
	},
	{
		"W-1", Warn, "a stencil admits harness/Bash or moxy/toolbox/run with command open or unconstrained",
		"W-1: <tool> admitted with an open command: the command line names files and hosts ajour cannot scope (RFC 0018 §7)",
	},
	{
		"W-2", Warn, "a ReadMcpResourceTool or ListMcpResourcesTool node under clown-builtin-harness/harness",
		"W-2: <tool> presents no tool step; this node is inert. Allow the resource under its owner's surface (RFC 0018 §7)",
	},
	{
		"W-4", Warn, "subtree on a catalog node",
		"W-4: subtree on <type> is inert: catalog ids are not paths",
	},
	{
		"H-1", Refuse, "the type line is ! espalier-profile-v1",
		"H-1: the type line is <x>; a capability profile is ! espalier-profile-v1",
	},
	{
		"H-2", Refuse, "role, harness and mode are present, once each",
		"H-2: <key> is required",
	},
	{
		"H-3", Refuse, "mode is attended or unattended",
		"H-3: mode=<v>: attended or unattended",
	},
	{
		"H-4", Refuse, "harness is claude-code when attended, juggler-run when unattended",
		"H-4: harness=<v> with mode=<m>: <m> profiles run <harness>",
	},
	{
		"H-5", Refuse, "provider and spend_cap_usd absent when attended, present when unattended",
		"H-5: <key> is <forbidden|required> in <mode> profiles",
	},
	{
		"H-6", Refuse, "model is present when unattended",
		"H-6: model is required in unattended profiles (it is what the spend cap buys)",
	},
	{
		"H-7", Refuse, "_base is absent or one @<markl id>; required when unattended",
		"H-7: _base <is required in unattended profiles|is not @<markl id>>",
	},
	{
		"H-8", Refuse, "role is manager-<leaf> or engineer-<leaf>",
		"H-8: role=<v>: manager-<leaf> or engineer-<leaf>",
	},
	{
		"H-9", Refuse, "no head key outside role, harness, mode, model, provider, spend_cap_usd, _base",
		"H-9: unknown head key <k>",
	},
	{
		"M-1", Refuse, "unattended: no perm=ask atom",
		"M-1: perm=ask in an unattended profile: nobody can answer",
	},
	{
		"M-2", Refuse, "unattended: no troupe/chat_send and no spinclass server node that is not deny",
		"M-2: <node> in an unattended profile: a node reports through its job",
	},
	{
		"M-3", Refuse, "attended: clown-builtin-jobs/ringmaster and /troupe present and tagged required",
		"M-3: attended profiles require <server> tagged required (the hello)",
	},
}

// Rule returns the rule with id.
func Rule(id string) (RuleSpec, bool) {
	for _, r := range Rules {
		if r.ID == id {
			return r, true
		}
	}
	return RuleSpec{}, false
}

// Vector is one row of RFC 0019 §5 as a whole document. Head is the
// hyphence head's metadata lines, the type line last; an empty Head means
// the body is checked alone, and the H-* and M-* rules do not run.
type Vector struct {
	ID     string   `json:"id"`
	Layer  string   `json:"layer"`
	Head   string   `json:"head,omitempty"`
	Body   string   `json:"body"`
	Expect []Expect `json:"expect"`
	Note   string   `json:"note,omitempty"`
}

// Expect is one finding a vector must produce, Count times.
type Expect struct {
	Rule  string `json:"rule"`
	Count int    `json:"count"`
}

// Vectors parses the embedded vectors, refusing unknown keys.
func Vectors() ([]Vector, error) {
	var vs []Vector
	dec := json.NewDecoder(bytes.NewReader(vectorsJSON))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&vs); err != nil {
		return nil, fmt.Errorf("parse vectors.json: %w", err)
	}
	return vs, nil
}

// RawVectors returns vectors.json verbatim, for a validator in another
// module to run.
func RawVectors() []byte { return bytes.Clone(vectorsJSON) }
