// Package ajourshadow is the clown hook's side of RFC 0018 §11 in shadow
// mode (clown#349): it turns a PreToolUse call into ajour's Call (RFC 0018
// §4's presentation), evaluates it against the session's layers, and
// records the verdict without acting on it. Enforcement is one switch,
// CurrentMode, which stays ModeShadow until the operator rules otherwise.
//
// The layers file is operator-placed at
// $XDG_STATE_HOME/clown/ajour/<principal>/layers.json until spinclass writes
// one (RFC 0018 §11's open question); an absent file is no profile, and
// nothing is recorded. The record, shadow.jsonl beside it, carries each
// call's tool and verdict and never its arguments, capped at MaxBytes with
// one rotated generation.
package ajourshadow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"code.linenisgreat.com/clown/internal/0/catalogcontract"
	"code.linenisgreat.com/clown/internal/0/userpath"
	"code.linenisgreat.com/clown/internal/alfa/catalogsnap"
	"code.linenisgreat.com/cutting-garden/pkgs/ajour"
)

// Mode is whether the hook acts on a verdict.
type Mode int

const (
	// ModeShadow records the verdict and never denies or escalates.
	ModeShadow Mode = iota
	// ModeEnforce denies every verdict but Admitted. An Ask is a refusal
	// until the escalation channel (RFC 0018 §11) is built.
	ModeEnforce
)

func (m Mode) String() string {
	if m == ModeEnforce {
		return "enforce"
	}
	return "shadow"
}

// CurrentMode is the hook's mode: shadow, by operator ruling (2026-11-16,
// via circus/eager-holly). Turning enforcement on is this one constant.
const CurrentMode = ModeShadow

// Decision is what the hook tells the harness: Deny with Reason, or nothing.
type Decision struct {
	Deny   bool
	Reason string
}

// Decide is the hook's answer to v under mode.
func Decide(mode Mode, v ajour.Verdict) Decision {
	if mode == ModeShadow || v.Outcome == ajour.OutcomeAdmitted {
		return Decision{}
	}
	return Decision{Deny: true, Reason: "ajour " + v.Outcome.String() + ": " + strings.TrimSpace(v.String())}
}

// The node types a call presents (RFC 0018 §1).
const (
	typePlugin = "clown-plugin-v1"
	typeServer = "mcp-server-v1"
	typeTool   = "mcp-tool-v1"
	typeInput  = "tool-input-v1"
	typePath   = "fs-path-v1"
)

// uriScheme matches a value that names a scheme (RFC 0018 §4.2); a
// single-letter "scheme" would be a Windows drive and is not one.
var uriScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]+:`)

// BuildCall presents a call of node with input as ajour's Call (RFC 0018
// §3, §4), cwd being the call's working directory and home the user's. It
// presents path and input fields; for any other kind, a URI in a path
// field, or a field resolved from an environment other than cwd, it
// returns unbuilt saying which, and the caller records that instead of a
// verdict over a half-built call.
func BuildCall(node catalogsnap.ToolNode, c catalogcontract.Contract, input map[string]json.RawMessage, cwd, home string) (call ajour.Call, unbuilt string) {
	tool := ajour.Subject{Type: typeTool, ID: node.Tool, Digest: node.Lock}
	if node.Kind != "" {
		tool.Fields = map[string][]string{"kind": {node.Kind}}
	}
	call = ajour.Call{
		Path: []ajour.Subject{
			{Type: typePlugin, ID: node.Plugin},
			{Type: typeServer, ID: node.Server},
			tool,
		},
		Anchor: 1,
	}

	type field struct {
		name   string
		raw    json.RawMessage
		spec   catalogcontract.Field
		filled bool // resolved from cwd, not given
	}
	var fields []field
	for name, raw := range input {
		fields = append(fields, field{name: name, raw: raw, spec: c.Field(node.Plugin, node.Server, node.Tool, name)})
	}
	if entry, ok := c.Tool(node.Plugin, node.Server, node.Tool); ok {
		for _, spec := range entry.Fields {
			if _, given := input[spec.Name]; given || spec.Resolve == "" {
				continue
			}
			if spec.Resolve != catalogcontract.ResolveCwd {
				return ajour.Call{}, fmt.Sprintf("field %s resolves from %s, which is not built", spec.Name, spec.Resolve)
			}
			raw, _ := json.Marshal(cwd)
			fields = append(fields, field{name: spec.Name, raw: raw, spec: spec, filled: true})
		}
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].name < fields[j].name })

	for _, f := range fields {
		kind := f.spec.Kind
		if kind == "" {
			kind = catalogcontract.KindInput
		}
		switch kind {
		case catalogcontract.KindRoute:
			continue
		case catalogcontract.KindInput:
			values, err := presentValues(f.raw)
			if err != nil {
				return ajour.Call{}, fmt.Sprintf("field %s: %v", f.name, err)
			}
			for _, v := range values {
				call.Values = append(call.Values, []ajour.Subject{{
					Type: typeInput, ID: f.name,
					Fields: map[string][]string{"field": {f.name}, "value": {v}},
				}})
			}
		case catalogcontract.KindPath:
			if f.spec.RelativeTo != "" {
				return ajour.Call{}, fmt.Sprintf("field %s is relative to %s, which is not built", f.name, f.spec.RelativeTo)
			}
			paths, err := pathValues(f.raw, f.spec.Split)
			if err != nil {
				return ajour.Call{}, fmt.Sprintf("field %s: %v", f.name, err)
			}
			for _, p := range paths {
				if uriScheme.MatchString(p) && !strings.HasPrefix(p, "file:") {
					return ajour.Call{}, fmt.Sprintf("field %s holds a URI (RFC 0018 §4.2, a resource read), which is not built", f.name)
				}
				id, err := FileID(strings.TrimPrefix(p, "file:"), cwd, home)
				if err != nil {
					return ajour.Call{}, fmt.Sprintf("field %s: %v", f.name, err)
				}
				call.Values = append(call.Values, []ajour.Subject{{
					Type: typePath, ID: id, Fields: map[string][]string{"field": {f.name}},
				}})
			}
		default:
			return ajour.Call{}, fmt.Sprintf("field %s is of kind %s, which is not built", f.name, kind)
		}
	}
	return call, ""
}

// presentValues is RFC 0018 §4.3: a string's text, any other value's
// canonical JSON, one value per element of a top-level array, and "[]" for
// an empty one.
func presentValues(raw json.RawMessage) ([]string, error) {
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		elems = []json.RawMessage{raw}
	} else if len(elems) == 0 {
		return []string{"[]"}, nil
	}
	out := make([]string, 0, len(elems))
	for _, e := range elems {
		var s string
		if json.Unmarshal(e, &s) == nil {
			out = append(out, s)
			continue
		}
		canon, err := catalogcontract.CanonicalJSON(e)
		if err != nil {
			return nil, err
		}
		out = append(out, string(canon))
	}
	return out, nil
}

// pathValues is a path field's paths: a string (split on whitespace when the
// contract says so) or an array of strings.
func pathValues(raw json.RawMessage, split string) ([]string, error) {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		if split == "whitespace" {
			return strings.Fields(one), nil
		}
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, errors.New("a path field is a string or an array of strings")
	}
	return many, nil
}

// FileID is RFC 0018 §4.1's canonical file: id of path: resolved against
// cwd, cleaned, symlinks resolved as far as the path exists, then relative
// to home (home itself is file:.) or absolute outside it.
func FileID(path, cwd, home string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	resolved, err := resolveExisting(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	if h, err := resolveExisting(filepath.Clean(home)); err == nil {
		if rel, err := filepath.Rel(h, resolved); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return "file:" + rel, nil
		}
	}
	return "file:" + resolved, nil
}

// resolveExisting resolves the symlinks of path's longest existing prefix
// and keeps the rest as written, so a path a write is about to create is
// presented under its real parent.
func resolveExisting(path string) (string, error) {
	var rest []string
	for p := path; ; p = filepath.Dir(p) {
		real, err := filepath.EvalSymlinks(p)
		if err == nil {
			return filepath.Join(append([]string{real}, rest...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("cannot canonicalize %s: %w", p, err)
		}
		if parent := filepath.Dir(p); parent == p {
			return path, nil
		}
		rest = append([]string{filepath.Base(p)}, rest...)
	}
}

// Layer roles in the layers file (RFC 0018 §11).
const (
	RoleStencil = "stencil"
	RolePatch   = "patch"
)

// LayerEntry is one entry of the layers file, chain start first.
type LayerEntry struct {
	Path   string `json:"path"`
	Role   string `json:"role"`
	Digest string `json:"digest,omitempty"`
	Act    string `json:"act,omitempty"`
}

// Dir is $XDG_STATE_HOME/clown/ajour/<principal>.
func Dir(principal string) (string, error) {
	if principal == "" || strings.ContainsAny(principal, `/\`) || principal == "." || principal == ".." {
		return "", fmt.Errorf("principal %q cannot name a directory", principal)
	}
	return userpath.StatePath("ajour", principal)
}

// LoadLayers reads the layers file at path: nil and no error when it is
// absent. Every layer must parse; a stencil's digest must match its tree
// (Stencil.Digest; purpose-full or bare). ajour defines no patch digest, so
// a patch's is not checked.
func LoadLayers(path string) ([]ajour.Layer, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []LayerEntry
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	layers := make([]ajour.Layer, 0, len(entries))
	for i, e := range entries {
		doc, err := os.ReadFile(e.Path)
		if err != nil {
			return nil, fmt.Errorf("layer %d: %w", i, err)
		}
		body := documentBody(string(doc))
		switch e.Role {
		case RoleStencil:
			st, err := ajour.ParseStencil(body)
			if err != nil {
				return nil, fmt.Errorf("layer %d (%s): %w", i, e.Path, err)
			}
			want := strings.TrimPrefix(e.Digest, ajour.PurposeStencilDigest+"@")
			if got := st.Digest(); want != got {
				return nil, fmt.Errorf("layer %d (%s): digest %s does not match the tree's %s", i, e.Path, e.Digest, got)
			}
			layers = append(layers, st)
		case RolePatch:
			p, err := ajour.ParsePatch(body)
			if err != nil {
				return nil, fmt.Errorf("layer %d (%s): %w", i, e.Path, err)
			}
			layers = append(layers, p)
		default:
			return nil, fmt.Errorf("layer %d: role %q is not stencil or patch", i, e.Role)
		}
	}
	return layers, nil
}

// documentBody strips a hyphence head (hyphence(7)) from doc, if it has one.
func documentBody(doc string) string {
	rest, ok := strings.CutPrefix(doc, "---\n")
	if !ok {
		return doc
	}
	if _, body, found := strings.Cut(rest, "\n---\n"); found {
		return strings.TrimPrefix(body, "\n")
	}
	return ""
}

// Record is one shadow.jsonl line: the call's tool and ajour's verdict.
// It never carries the call's arguments: the deciding layer names the
// refusing node or the refused subject's type, not its id.
type Record struct {
	TS          string `json:"ts"`
	Mode        string `json:"mode"`
	HarnessName string `json:"harness_name"`
	Plugin      string `json:"plugin,omitempty"`
	Server      string `json:"server,omitempty"`
	Tool        string `json:"tool,omitempty"`
	// Outcome is ajour's (refused, ask, admitted), or unresolved, unbuilt
	// or layers-error when no verdict was computed.
	Outcome  string `json:"outcome"`
	Perm     string `json:"perm,omitempty"`
	Deciding string `json:"deciding,omitempty"`
	Unbuilt  string `json:"unbuilt,omitempty"`
	// Error is why the layers did not load (OutcomeLayersError).
	Error string `json:"error,omitempty"`
}

// The files in a session's Dir.
const (
	LayersFile = "layers.json"
	ShadowFile = "shadow.jsonl"
)

// The outcomes a record carries when no verdict was computed.
const (
	OutcomeUnresolved  = "unresolved"   // the session's catalog does not name the tool
	OutcomeUnbuilt     = "unbuilt"      // BuildCall could not present the call
	OutcomeLayersError = "layers-error" // the layers file did not load
)

// Hook is the PreToolUse hook for one call of the session principal: the
// harness's tool name and input, the call's working directory and the
// user's home. It records the verdict in the session's shadow.jsonl and
// returns what to tell the harness under CurrentMode. With no layers file it
// records and writes nothing. A failure to record never changes the answer.
func Hook(principal, harnessName string, input map[string]json.RawMessage, cwd, home string) Decision {
	dir, err := Dir(principal)
	if err != nil {
		return Decision{}
	}
	layers, err := LoadLayers(filepath.Join(dir, LayersFile))
	if err == nil && layers == nil {
		return Decision{}
	}
	rec := Recorder{Path: filepath.Join(dir, ShadowFile)}
	r := Record{TS: time.Now().UTC().Format(time.RFC3339Nano), Mode: CurrentMode.String(), HarnessName: harnessName}
	var v ajour.Verdict // zero: refused, so enforce fails closed on anything below
	switch node, c, ok := resolve(principal, harnessName); {
	case err != nil:
		r.Outcome, r.Error = OutcomeLayersError, err.Error()
	case !ok:
		r.Outcome = OutcomeUnresolved
	default:
		r, v = Observe(layers, node, c, input, cwd, home)
	}
	_ = rec.Append(r) // a failed record must not change the call's fate
	return Decide(CurrentMode, v)
}

// resolve maps harnessName to its catalog tool through the principal's
// snapshot and the embedded contract.
func resolve(principal, harnessName string) (catalogsnap.ToolNode, catalogcontract.Contract, bool) {
	c, err := catalogcontract.Load()
	if err != nil {
		return catalogsnap.ToolNode{}, c, false
	}
	dir, err := catalogsnap.Dir()
	if err != nil {
		return catalogsnap.ToolNode{}, c, false
	}
	for _, s := range catalogsnap.List(dir) {
		if s.Principal == principal {
			node, ok := catalogsnap.Resolve(s, c, harnessName)
			return node, c, ok
		}
	}
	return catalogsnap.ToolNode{}, c, false
}

// Observe evaluates one resolved call against layers and returns its record
// and ajour's verdict (the zero Verdict, a refusal, when the call is
// unbuilt).
func Observe(layers []ajour.Layer, node catalogsnap.ToolNode, c catalogcontract.Contract, input map[string]json.RawMessage, cwd, home string) (Record, ajour.Verdict) {
	r := Record{
		TS: time.Now().UTC().Format(time.RFC3339Nano), Mode: CurrentMode.String(),
		HarnessName: node.HarnessName, Plugin: node.Plugin, Server: node.Server, Tool: node.Tool,
	}
	call, unbuilt := BuildCall(node, c, input, cwd, home)
	if unbuilt != "" {
		r.Outcome, r.Unbuilt = OutcomeUnbuilt, unbuilt
		return r, ajour.Verdict{}
	}
	v := ajour.Evaluate(layers, call)
	r.Outcome = v.Outcome.String()
	if v.Outcome == ajour.OutcomeAdmitted {
		r.Perm = v.Perm.String()
	}
	r.Deciding = deciding(v)
	return r, v
}

// deciding names the first refusing (or else asking) layer and node.
func deciding(v ajour.Verdict) string {
	if len(v.Refusals) > 0 {
		f := v.Refusals[0]
		what := f.Node
		if f.Kind == ajour.KindNoAllowance {
			what = "{" + f.Subject.Type + "}"
		}
		return fmt.Sprintf("layer %d: %s %s", f.Layer, f.Kind, what)
	}
	if len(v.Asks) > 0 {
		return fmt.Sprintf("layer %d: ask %s", v.Asks[0].Layer, v.Asks[0].Node)
	}
	return ""
}

// DefaultMaxBytes caps shadow.jsonl at 4 MiB; with its one rotated
// generation a session's record holds at most 8 MiB.
const DefaultMaxBytes = 4 << 20

// Recorder appends records to Path (0600, in a 0700 directory). Past
// MaxBytes (DefaultMaxBytes when 0) it renames Path to Path.1, replacing the
// older generation, and starts afresh.
type Recorder struct {
	Path     string
	MaxBytes int64
}

// Append writes r as one line.
func (rec Recorder) Append(r Record) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	limit := rec.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxBytes
	}
	if err := os.MkdirAll(filepath.Dir(rec.Path), 0o700); err != nil {
		return err
	}
	if st, err := os.Stat(rec.Path); err == nil && st.Size()+int64(len(line)) > limit {
		if err := os.Rename(rec.Path, rec.Path+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(rec.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(line)
	return errors.Join(werr, f.Close())
}
