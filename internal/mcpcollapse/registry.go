// Package mcpcollapse implements the clown-mcp-collapse aggregator's own
// logic: the piece that sits in front of N upstream MCP servers and presents
// the harness with three generic tools (mcp_list/mcp_describe/mcp_call)
// instead of every upstream tool flattened into the catalog. This file is the
// registry — the stable-id → canonical-entry map those three verbs resolve
// against.
//
// The id scheme is dotted {server}.{tool} (e.g. server "grit" + tool "commit"
// → "grit.commit"). Because tool names are already unique within one upstream,
// a rendered id can only collide if two upstreams share the same server NAME —
// so duplicate server names are the primary defense (Build rejects them) and a
// rendered-id tie is a last-resort first-wins-with-warning fallback that is
// only reachable when a server name itself contains a dot.
package mcpcollapse

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Entry is the canonical record a rendered tool id resolves to. mcp_call
// dispatches to URL with the real upstream Tool name; mcp_describe hands back
// Schema and Description; mcp_list enumerates Description.
type Entry struct {
	// Server is the upstream server name (the {server} half of the id).
	Server string
	// Tool is the real upstream tool name (the {tool} half of the id) — what
	// mcp_call names in its tools/call to the upstream.
	Tool string
	// URL is the upstream server's MCP HTTP URL, where mcp_call dispatches.
	URL string
	// Schema is the upstream tool's inputSchema, kept as json.RawMessage so the
	// aggregator hands it back verbatim in mcp_describe and validates mcp_call
	// args against it later — a deliberate contrast with pluginhost's ToolInfo,
	// which drops the schema.
	Schema json.RawMessage
	// Description is the upstream tool's one-line description, surfaced in
	// mcp_list.
	Description string
}

// ID renders the entry's dotted {server}.{tool} identifier — the string the
// agent passes to mcp_call/mcp_describe and the Registry's Lookup key.
func (e Entry) ID() string {
	return e.Server + "." + e.Tool
}

// ToolSpec is one upstream tool as handed to the Builder: the real tool name,
// its one-line description, and its raw inputSchema. The Builder pairs it with
// the owning server's name and URL to produce an Entry.
type ToolSpec struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// serverContribution is one AddServer call captured for the Build pass: a
// server name, its URL, and the tools it contributes. Build validates and
// renders these into the final id → Entry map.
type serverContribution struct {
	name  string
	url   string
	tools []ToolSpec
}

// Builder accumulates server contributions, then validates and renders them
// into a Registry via Build. The zero value is ready to use.
type Builder struct {
	contributions []serverContribution
}

// AddServer records one upstream's contribution: the server name (the {server}
// half of every id it produces), its MCP HTTP URL, and the tools it exposes.
// Validation is deferred to Build so the caller can add every server first and
// get a single, complete error.
func (b *Builder) AddServer(name, url string, tools []ToolSpec) {
	b.contributions = append(b.contributions, serverContribution{
		name:  name,
		url:   url,
		tools: tools,
	})
}

// Build validates the accumulated contributions and renders them into a
// Registry.
//
// It fails outright on duplicate server names (the primary collision defense):
// two different upstreams registered under one name would silently drop one
// server's whole tool set, so Build returns an error naming the server and both
// conflicting URLs rather than letting a tool vanish mysteriously.
//
// Given distinct server names a rendered id still cannot collide via the dotted
// scheme alone — but a server name containing a dot can make two entries render
// the same id (e.g. "grit.sub"+"commit" and "grit"+"sub.commit" both →
// "grit.sub.commit"). That last-resort case keeps the first entry and records a
// warning (surfaced via Registry.Warnings) rather than failing, since it is a
// defensive corner rather than the misconfiguration duplicate names represent.
func (b *Builder) Build() (*Registry, error) {
	seenServers := make(map[string]string, len(b.contributions))
	for _, c := range b.contributions {
		if firstURL, dup := seenServers[c.name]; dup {
			return nil, fmt.Errorf(
				"mcpcollapse: duplicate server name %q registered by two upstreams (%s and %s); "+
					"server names must be unique because they form the {server} half of every tool id",
				c.name, firstURL, c.url,
			)
		}
		seenServers[c.name] = c.url
	}

	byID := make(map[string]Entry)
	var warnings []string
	for _, c := range b.contributions {
		for _, tool := range c.tools {
			entry := Entry{
				Server:      c.name,
				Tool:        tool.Name,
				URL:         c.url,
				Schema:      tool.Schema,
				Description: tool.Description,
			}
			id := entry.ID()
			if existing, taken := byID[id]; taken {
				warnings = append(warnings, fmt.Sprintf(
					"mcpcollapse: tool id %q rendered by two entries (%s.%s and %s.%s); "+
						"keeping the first, dropping the second",
					id, existing.Server, existing.Tool, entry.Server, entry.Tool,
				))
				continue
			}
			byID[id] = entry
		}
	}

	return &Registry{byID: byID, warnings: warnings}, nil
}

// Registry maps a rendered tool id to its canonical Entry. It is immutable
// after Build; the three verbs read it concurrently without locking.
type Registry struct {
	byID     map[string]Entry
	warnings []string
}

// Lookup resolves a rendered id back to its canonical Entry. The bool is false
// for an unknown id, so mcp_call/mcp_describe can reject ids the agent invented
// rather than dispatching to a zero Entry.
func (r *Registry) Lookup(id string) (Entry, bool) {
	entry, ok := r.byID[id]
	return entry, ok
}

// Entries returns every entry sorted by id, so mcp_list output is deterministic
// across builds regardless of the order servers and tools were added.
func (r *Registry) Entries() []Entry {
	entries := make([]Entry, 0, len(r.byID))
	for _, e := range r.byID {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ID() < entries[j].ID()
	})
	return entries
}

// Warnings returns the non-fatal first-wins tiebreaker messages recorded during
// Build (empty when every id was unique). The caller surfaces these — e.g. logs
// them at startup — so a silently-dropped colliding tool is visible.
func (r *Registry) Warnings() []string {
	return r.warnings
}
