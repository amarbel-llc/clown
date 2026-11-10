// Package catalogcontract is the machine-readable copy of RFC 0018, the
// capability catalog contract: the node types an enforcer presents to ajour
// (cutting-garden FDR 0028), the URI schemes each server's resource surface
// owns, and, per tool, the kind of each input field (path, reference,
// content, recipe, route, or the default input). RFC 0018 is normative; a
// disagreement between it and catalog.json is a bug in catalog.json.
//
// catalog.json is the single source: it is hand-written, embedded here, and
// returned verbatim by Raw so another enforcer (moxy's hand-typed shadow-mode
// table) can copy it as data instead of keeping a second source of truth.
package catalogcontract

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed catalog.json
var catalogJSON []byte

// Field kinds (RFC 0018 §4).
const (
	KindPath      = "path"      // one fs-path-v1 subject per path; a non-file: URI is a resource read
	KindReference = "reference" // one resource path per object, under the scheme owner's surface
	KindContent   = "content"   // a whole madder://blobs URI is a resource read, anything else is input
	KindRecipe    = "recipe"    // run_recipe's recipe step and its recipe-arg-v1 values
	KindRoute     = "route"     // consumed by the enforcer to route the call; presents no subject
	KindInput     = "input"     // the default: one tool-input-v1 subject
)

// Kinds is every field kind RFC 0018 §4 declares.
var Kinds = []string{KindPath, KindReference, KindContent, KindRecipe, KindRoute, KindInput}

// Resolutions of an omitted field the tool fills from its environment
// (RFC 0018 §4.3): the enforcer presents the resolved value.
const (
	ResolveCwd             = "cwd"              // the call's working directory
	ResolveCwdRemote       = "cwd-remote"       // the working directory's forge remote (smith repo, host)
	ResolveSessionWorktree = "session-worktree" // the spinclass session's worktree
)

// Resolutions is every resolution RFC 0018 §4.3 declares.
var Resolutions = []string{ResolveCwd, ResolveCwdRemote, ResolveSessionWorktree}

// ResourceVerbs is the verb set of a resource step (RFC 0018 §5.3).
var ResourceVerbs = []string{"read", "patch", "put", "delete", "create"}

// OpFromBody marks a reference field whose verb is read from the call's own
// body per object (mutate_nodes: each op's kind).
const OpFromBody = "from-body"

// ToolKinds is the set of a tool subject's kind field (RFC 0018 §3).
var ToolKinds = []string{"read", "write", "exec"}

// PresentsOwnerResourceRead marks a harness tool that presents no tool step:
// the call is presented as a resource read under the scheme owner's
// surface (RFC 0018 §7).
const PresentsOwnerResourceRead = "owner-resource-read"

// PresentsWrappedCall marks a wrapper (moxy async, batch) that presents the
// call or calls it wraps and no subject of its own (RFC 0018 §3).
const PresentsWrappedCall = "wrapped-call"

// Presentations is every value of Tool.Presents.
var Presentations = []string{PresentsOwnerResourceRead, PresentsWrappedCall}

// RelativeToRepoRoot: a relative path resolves against the root of the
// repository the call runs in (grit status, diff, log paths).
const RelativeToRepoRoot = "repo-root"

// Contract is catalog.json parsed.
type Contract struct {
	Version   int        `json:"version"`
	NodeTypes []NodeType `json:"node_types"`
	Schemes   []Scheme   `json:"schemes"`
	Tools     []Tool     `json:"tools"`
}

// NodeType is one row of RFC 0018 §1.
type NodeType struct {
	Type   string   `json:"type"`
	Layer  string   `json:"layer"`
	ID     string   `json:"id"`
	Fields []string `json:"fields"`
}

// Scheme names the server whose resource surface owns a URI scheme.
type Scheme struct {
	Scheme string `json:"scheme"`
	Plugin string `json:"plugin"`
	Server string `json:"server"`
}

// Tool is one tool's entry: its kind (for a tool with no manifest, the
// harness built-ins) and its non-default fields. A field it does not list
// is KindInput.
type Tool struct {
	Plugin   string  `json:"plugin"`
	Server   string  `json:"server"`
	Tool     string  `json:"tool"`
	Kind     string  `json:"kind,omitempty"`
	Presents string  `json:"presents,omitempty"`
	Fields   []Field `json:"fields,omitempty"`
	Note     string  `json:"note,omitempty"`
}

// Key is plugin/server/tool.
func (t Tool) Key() string { return t.Plugin + "/" + t.Server + "/" + t.Tool }

// Field is one declared input field.
type Field struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Op         string `json:"op,omitempty"`
	Resolve    string `json:"resolve,omitempty"`
	Split      string `json:"split,omitempty"`       // "whitespace": one value per word (folio rm paths)
	RelativeTo string `json:"relative_to,omitempty"` // a relative path resolves against this field's value
}

// Raw returns catalog.json verbatim.
func Raw() []byte { return bytes.Clone(catalogJSON) }

// Load parses the embedded table, refusing unknown keys.
func Load() (Contract, error) {
	var c Contract
	dec := json.NewDecoder(bytes.NewReader(catalogJSON))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Contract{}, fmt.Errorf("parse catalog.json: %w", err)
	}
	return c, nil
}

// Tool returns the entry for plugin/server/tool.
func (c Contract) Tool(plugin, server, tool string) (Tool, bool) {
	for _, t := range c.Tools {
		if t.Plugin == plugin && t.Server == server && t.Tool == tool {
			return t, true
		}
	}
	return Tool{}, false
}

// Field returns how a tool's input field is presented; an undeclared field,
// or any field of an undeclared tool, is KindInput (RFC 0018 §4).
func (c Contract) Field(plugin, server, tool, field string) Field {
	if t, ok := c.Tool(plugin, server, tool); ok {
		for _, f := range t.Fields {
			if f.Name == field {
				return f
			}
		}
	}
	return Field{Name: field, Kind: KindInput}
}

// SchemeOwner returns the server whose surface owns scheme (no colon).
func (c Contract) SchemeOwner(scheme string) (Scheme, bool) {
	for _, s := range c.Schemes {
		if s.Scheme == scheme {
			return s, true
		}
	}
	return Scheme{}, false
}
