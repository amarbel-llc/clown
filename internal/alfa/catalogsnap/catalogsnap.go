// Package catalogsnap is the launched session's catalog (clown#350, RFC 0018
// §10): at launch clown records, per session, each started server's tools as
// tools/list reported them, and the live clown-catalog:// listing presents
// that record as RFC 0018's catalog subjects (plugin, server, tool, and the
// tool's inputs), with each tool's kind and perm. The record keeps each
// tool's inputSchema verbatim, which a lock digest is computed from.
//
// The record is $XDG_STATE_HOME/clown/catalog/<hex sha256(principal)>.json,
// 0600 in a 0700 directory, replaced atomically. It holds names, schemas,
// descriptions and _meta only: never a server's URL, command, args or env,
// nor the tokens those can carry (#292).
package catalogsnap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"code.linenisgreat.com/clown/internal/0/catalogcontract"
	"code.linenisgreat.com/clown/internal/0/userpath"
)

// Version is the record's schema version.
const Version = 1

// HarnessClaudeCode is the harness whose built-in tools (Read, Bash, …) the
// catalog lists under HarnessPlugin/HarnessServer.
const HarnessClaudeCode = "claude-code"

// The plugin and server the harness built-ins hang under (RFC 0018 §7).
const (
	HarnessPlugin = "clown-builtin-harness"
	HarnessServer = "harness"
)

// MoxyPlugin is the plugin whose one MCP server multiplexes child servers:
// its tools are presented as [moxy, <child>, <tool>] (RFC 0018 §1).
const MoxyPlugin = "moxy"

// Tool perms (RFC 0018 §3).
const (
	PermAllow   = "allow"
	PermAsk     = "ask"
	PermDynamic = "dynamic"
)

// Snapshot is one launched session's record.
type Snapshot struct {
	Version   int    `json:"version"`
	Principal string `json:"principal"`
	// Harness is HarnessClaudeCode when the launched provider is claude; the
	// built-ins are listed only then.
	Harness string `json:"harness"`
	// FrontendPID and FrontendStartTicks name the clown frontend that wrote
	// the record (the start ticks tell it from a reused pid; 0 where /proc
	// is absent), so a reader tells a live session from a dead one.
	FrontendPID        int      `json:"frontend_pid"`
	FrontendStartTicks uint64   `json:"frontend_start_ticks"`
	WrittenAt          string   `json:"written_at"`
	Servers            []Server `json:"servers"`
}

// Server is one started plugin server, named as clown names it.
type Server struct {
	Plugin string `json:"plugin"`
	Server string `json:"server"`
	Tools  []Tool `json:"tools"`
}

// Tool is one tools/list entry.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Meta        json.RawMessage `json:"meta,omitempty"`
}

// Dir is $XDG_STATE_HOME/clown/catalog.
func Dir() (string, error) { return userpath.StatePath("catalog") }

// FileName is principal's record name in Dir.
func FileName(principal string) string {
	sum := sha256.Sum256([]byte(principal))
	return hex.EncodeToString(sum[:]) + ".json"
}

// Write stamps s and replaces its record in dir (created 0700).
func Write(dir string, s Snapshot) error {
	if s.Principal == "" {
		return errors.New("catalog snapshot without a principal")
	}
	s.Version = Version
	s.WrittenAt = time.Now().UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, tempPrefix+strconv.Itoa(os.Getpid())+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(b)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, FileName(s.Principal))); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// List reads every record in dir, skipping any it cannot parse or whose name
// is not its principal's, ordered by principal.
func List(dir string) []Snapshot {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Snapshot
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var s Snapshot
		if json.Unmarshal(b, &s) != nil || s.Version != Version || FileName(s.Principal) != e.Name() {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Principal < out[j].Principal })
	return out
}

// Retention is how long a dead session's snapshot is kept, counted from when
// it was written (a launch rewrites it, so this is the session's start): a
// week covers a session resumed after a weekend, and Prune never deletes a
// live session's snapshot whatever its age.
const Retention = 7 * 24 * time.Hour

// tempPrefix starts the name of Write's temp file, followed by the writer's
// pid and a random suffix (.snapshot-<pid>-<random>); List never reads one.
const tempPrefix = ".snapshot-"

// tempWriterPID is the writer pid a temp file's name carries.
func tempWriterPID(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, tempPrefix)
	if !ok {
		return 0, false
	}
	digits, _, ok := strings.Cut(rest, "-")
	pid, err := strconv.Atoi(digits)
	if !ok || err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// pruneTemps removes the temp files of interrupted Writes (#355): one last
// modified more than maxAge before now whose writer is not running.
func pruneTemps(dir string, now time.Time, maxAge time.Duration, alive func(Snapshot) bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, ok := tempWriterPID(e.Name())
		if !ok || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= maxAge || alive(Snapshot{FrontendPID: pid}) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}

// Prune deletes, in dir, every snapshot whose session is not alive and that
// was written more than maxAge before now, and returns the principals it
// removed. A file it cannot parse or date is left alone. It also removes the
// temp files of Writes interrupted that long ago (pruneTemps).
func Prune(dir string, now time.Time, maxAge time.Duration, alive func(Snapshot) bool) []string {
	pruneTemps(dir, now, maxAge, alive)
	var removed []string
	for _, s := range List(dir) {
		written, err := time.Parse(time.RFC3339Nano, s.WrittenAt)
		if err != nil || now.Sub(written) <= maxAge || alive(s) {
			continue
		}
		// A resumed session reuses its principal and may have rewritten the
		// file since List read it: re-read, and keep a file that changed.
		path := filepath.Join(dir, FileName(s.Principal))
		var cur Snapshot
		if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &cur) != nil || cur.WrittenAt != s.WrittenAt {
			continue
		}
		if os.Remove(path) == nil {
			removed = append(removed, s.Principal)
		}
	}
	return removed
}

// ToolNode is one tool as the catalog presents it.
type ToolNode struct {
	Plugin, Server, Tool string
	// Kind is read, write or exec, or "" when nothing declares one.
	Kind string
	// Perm is PermAllow, PermAsk or PermDynamic, or "" when unknown.
	Perm   string
	Inputs []Input
}

// Input is one input field of a tool and how RFC 0018 §4 presents it.
type Input struct {
	Name      string
	FieldKind string
}

// Present is s as catalog subjects, ordered by plugin, server, tool. A tool
// that presents no tool step of its own (a wrapper, a harness resource tool)
// is left out.
func Present(s Snapshot, c catalogcontract.Contract) []ToolNode {
	var out []ToolNode
	for _, srv := range s.Servers {
		for _, t := range srv.Tools {
			server, tool := srv.Server, t.Name
			if srv.Plugin == MoxyPlugin {
				server, tool = splitMoxyName(t.Name)
			}
			if !oneSegment(srv.Plugin) || !oneSegment(server) || !oneSegment(tool) {
				continue
			}
			if entry, ok := c.Tool(srv.Plugin, server, tool); ok && entry.Presents != "" {
				continue
			}
			n := ToolNode{Plugin: srv.Plugin, Server: server, Tool: tool, Kind: metaKind(t.Meta), Perm: permFromDescription(t.Description)}
			for _, name := range schemaProperties(t.InputSchema) {
				if !oneSegment(name) {
					continue
				}
				n.Inputs = append(n.Inputs, Input{Name: name, FieldKind: c.Field(srv.Plugin, server, tool, name).Kind})
			}
			out = append(out, n)
		}
	}
	if s.Harness == HarnessClaudeCode {
		for _, t := range c.Tools {
			if t.Plugin != HarnessPlugin || t.Presents != "" {
				continue
			}
			n := ToolNode{Plugin: t.Plugin, Server: t.Server, Tool: t.Tool, Kind: t.Kind}
			for _, f := range t.Fields {
				n.Inputs = append(n.Inputs, Input{Name: f.Name, FieldKind: f.Kind})
			}
			sort.Slice(n.Inputs, func(i, j int) bool { return n.Inputs[i].Name < n.Inputs[j].Name })
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Plugin != b.Plugin {
			return a.Plugin < b.Plugin
		}
		if a.Server != b.Server {
			return a.Server < b.Server
		}
		return a.Tool < b.Tool
	})
	return out
}

// oneSegment reports whether an id can be one URI segment of the listing
// (cutting-garden RFC 0018 §3): not empty, and not a dot segment a URI
// normalizer would collapse. The listing leaves out a node whose id cannot.
func oneSegment(id string) bool { return id != "" && id != "." && id != ".." }

// splitMoxyName splits the name moxy's own tools/list advertises under its
// default name template ({server}.{tool}, e.g. folio.read; the "_" form is
// Claude Code's mangling, which this never sees) at the first "."; a name
// with none is moxy's own tool. Transitional (RFC 0018 §9): moxy publishes no
// canonical server and tool in _meta, and a non-default --name-template
// would defeat this.
func splitMoxyName(name string) (server, tool string) {
	if s, t, ok := strings.Cut(name, "."); ok && s != "" && t != "" {
		return s, t
	}
	return MoxyPlugin, name
}

// metaKind is _meta.kind when it is one of RFC 0018's tool kinds. moxy's
// _meta.kind labels what a tool's result is (moxin(7) TOOL KIND), a different
// vocabulary, so any other value presents no kind.
func metaKind(meta json.RawMessage) string {
	var m struct {
		Kind string `json:"kind"`
	}
	if len(meta) == 0 || json.Unmarshal(meta, &m) != nil || !slices.Contains(catalogcontract.ToolKinds, m.Kind) {
		return ""
	}
	return m.Kind
}

var permsSuffix = regexp.MustCompile(`\[perms: ([a-z-]+)\]`)

// permFromDescription maps the "[perms: X]" suffix moxy appends to a tool's
// description to RFC 0018's perm. Transitional (RFC 0018 §9): the suffix is
// prose moxy may reword; the manifest's perms-request has no _meta key yet.
func permFromDescription(desc string) string {
	m := permsSuffix.FindStringSubmatch(desc)
	if m == nil {
		return ""
	}
	switch m[1] {
	case "always-allow":
		return PermAllow
	case "each-use", "delegate-to-client":
		return PermAsk
	case "dynamic":
		return PermDynamic
	}
	return ""
}

// schemaProperties is the sorted names of an object schema's top-level
// properties.
func schemaProperties(schema json.RawMessage) []string {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if len(schema) == 0 || json.Unmarshal(schema, &s) != nil {
		return nil
	}
	names := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
