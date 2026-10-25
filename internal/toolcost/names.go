package toolcost

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// subkeyExtractors is THE allowlist of parameterised tools whose argument is
// what matters for context cost, keyed by the "<server>.<tool>" key ToolKey
// builds. Every other tool gets no subkey: a free-form argument (a file path,
// a jq filter) would explode metric cardinality. Keep it small — adding an
// entry multiplies that tool's series by the number of distinct values.
var subkeyExtractors = map[string]func(input json.RawMessage) string{
	// Which recipe ran decides how much output it dumps into context.
	"just-us.run_recipe": inputString("recipe"),
	// moxy namespaces child tools "<child>_<tool>"; chix develop-run's first
	// command word (go, just, bats, ...) separates the cheap runs from the
	// chatty ones.
	"moxy.chix_develop-run": firstCommandWord,
}

// ToolKey maps a transcript tool_use name (plus its input, for the subkey
// allowlist) onto the dotted "<server>.<tool>[.<subkey>]" metric key:
//
//	mcp__plugin_moxy_moxy__rg_search          -> moxy.rg_search
//	mcp__plugin_just-us_just-us__run_recipe   -> just-us.run_recipe.<recipe>
//	mcp__claude_ai_Krone__cutting-garden_x    -> claude_ai_Krone.cutting-garden_x
//	Read                                      -> builtin.Read
//
// Plugin-served MCP tools are "mcp__plugin_<plugin>_<server>__<tool>"; the
// plugin segment is dropped (the plugin name is assumed hyphenated, so the
// first '_' after "plugin_" separates plugin from server). Every segment is
// statsd-sanitized, so dots in values become underscores.
func ToolKey(name string, input json.RawMessage) string {
	rest, isMCP := strings.CutPrefix(name, "mcp__")
	if !isMCP {
		return "builtin." + sanitize(name)
	}
	serverPart, tool, ok := strings.Cut(rest, "__")
	if !ok {
		return "unknown." + sanitize(rest)
	}
	if plugin, isPlugin := strings.CutPrefix(serverPart, "plugin_"); isPlugin {
		if _, server, ok := strings.Cut(plugin, "_"); ok {
			serverPart = server
		} else {
			serverPart = plugin
		}
	}
	key := sanitize(serverPart) + "." + sanitize(tool)
	if extract, ok := subkeyExtractors[key]; ok {
		if sub := sanitize(extract(input)); sub != "" {
			key += "." + sub
		}
	}
	return key
}

// inputString extracts a top-level string field of a tool input.
func inputString(field string) func(json.RawMessage) string {
	return func(input json.RawMessage) string {
		var m map[string]json.RawMessage
		if json.Unmarshal(input, &m) != nil {
			return ""
		}
		var s string
		if json.Unmarshal(m[field], &s) != nil {
			return ""
		}
		return s
	}
}

// firstCommandWord extracts the first word of the first entry of a
// chix develop-run "commands" array, reduced to its basename.
func firstCommandWord(input json.RawMessage) string {
	var in struct {
		Commands []struct {
			Command string `json:"command"`
		} `json:"commands"`
	}
	if json.Unmarshal(input, &in) != nil || len(in.Commands) == 0 {
		return ""
	}
	fields := strings.Fields(in.Commands[0].Command)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

// sanitize maps a value onto the statsd-safe alphabet [A-Za-z0-9_-]; every
// other rune (dots and path separators included) becomes '_'. Same rule as
// clown-stdio-bridge's sanitizeMetric.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, s)
}

// maxKeyLen bounds a metric key read back from outside (ValidKey).
const maxKeyLen = 256

// ValidKey reports whether k has the shape of a key this package builds
// (ToolKey): one or more non-empty segments of the statsd-safe alphabet,
// joined by dots, at most maxKeyLen bytes. A key read back from a file
// someone else can write is checked with it before it reaches statsd, where
// ':', '|' or a newline would forge metrics.
func ValidKey(k string) bool {
	if k == "" || len(k) > maxKeyLen {
		return false
	}
	for _, seg := range strings.Split(k, ".") {
		if seg == "" || sanitize(seg) != seg {
			return false
		}
	}
	return true
}

// Segment sanitizes one caller-supplied name segment (host, repo).
func Segment(s string) string {
	if s == "" {
		return "unknown"
	}
	return sanitize(s)
}

// modelSegment drops the "claude-" prefix from a model id and sanitizes it.
func modelSegment(model string) string {
	return Segment(strings.TrimPrefix(model, "claude-"))
}
