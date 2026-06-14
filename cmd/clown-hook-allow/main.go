// clown-hook-allow is a Claude Code PreToolUse hook handler. It reads the hook
// event JSON from stdin and acts on three trusted classes:
//
//   - Read/Glob/Grep against paths under /nix/store, and clown's own
//     clown-builtin-jobs MCP tools (the job-wakeup plumbing, clown#130) — these
//     get an ALLOW decision so the call is not prompted.
//   - The Explore subagent — the Agent tool launched with
//     subagent_type "Explore" is REWRITTEN to clown's read-only Discover
//     subagent (subagents/discover.md) via an updatedInput payload, so
//     exploration is transparently redirected rather than blocked. This is why
//     clown's --disallowed-tools no longer carries Agent(Explore): the hook
//     redirects it instead of the CLI arg refusing it.
//
// Everything else defers (emits nothing; the next hook or the default
// permission logic decides).
//
// Output form: the decision MUST be the nested hookSpecificOutput object
// (hookEventName/permissionDecision/permissionDecisionReason, plus updatedInput
// for the rewrite class). The older bare {"permissionDecision":...} form is
// honored by claude-code for some built-in tools but is IGNORED for MCP plugin
// tools (mcp__*) as of claude-code 2.1.177 — empirically (clown#130): the bare
// allow was silently dropped for the clown-builtin-jobs tools (and for
// /nix/store Reads) until this handler adopted the nested form, which moxy's
// working hook already uses.
//
// The /nix/store auto-allow exists because store paths are content-addressed and
// immutable: reading them is information-only and carries no risk worth a
// prompt. (--allowed-tools "Read(/nix/store/**)" is not honored by claude-code
// for the Read tool, which is why this is a hook.)
//
// Wire-up: clown synthesizes a built-in plugin (clown-builtin-jobs) whose
// hooks/hooks.json registers this binary as a PreToolUse handler (matcher ".*"),
// passed to claude via --plugin-dir. The legacy managed-settings wire-up is dead
// — claude does not read managed-settings outside --tent (clown#133).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

const nixStorePrefix = "/nix/store/"

// jobToolPrefix matches the MCP tool names of clown's synthesized
// clown-builtin-jobs plugin (RFC-0011): Claude Code names a plugin server's
// tools `mcp__plugin_<plugin>_<server>__<tool>`, and clown's built-in plugin is
// `clown-builtin-jobs` with server `jobs`. These are clown's own job-wakeup
// plumbing — a core harness facility, not an external side-effecting service —
// so every tool in the set is auto-allowed (clown#130).
const jobToolPrefix = "mcp__plugin_clown-builtin-jobs_jobs__"

// Subagent-rewrite constants. Claude Code launches subagents through the Agent
// tool (named "Agent" in this build's tool schema; "Task" in older/other
// builds). We match both names so the rewrite is robust to that naming — the
// rewrite only fires when subagent_type is exactly "Explore", so matching the
// extra name is harmless. The redirect target is clown's read-only Discover
// subagent (subagents/discover.md, registered via buildcfg.AgentsFile).
const (
	exploreSubagent  = "Explore"
	discoverSubagent = "Discover"
)

// subagentToolNames are the tool_name values under which Claude Code launches a
// subagent. See the rewrite-constant comment for why both are matched.
var subagentToolNames = map[string]bool{"Agent": true, "Task": true}

type hookInput struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// hookSpecificOutput is the PreToolUse permission-decision payload claude-code
// honors for ALL tools, including MCP plugin tools (clown#130). See the package
// doc for why the bare {"permissionDecision":...} form is insufficient for MCP
// tools as of claude-code 2.1.177.
type hookSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
	// UpdatedInput, when present, replaces the tool's entire input before it
	// runs (claude-code 2.1.177). Used for the Explore→Discover subagent
	// rewrite; omitted for plain allow decisions so their wire shape is
	// unchanged.
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
}

type hookOutput struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

// allow builds a nested PreToolUse "allow" decision carrying the given reason.
func allow(reason string) *hookOutput {
	return &hookOutput{HookSpecificOutput: hookSpecificOutput{
		HookEventName:            "PreToolUse",
		PermissionDecision:       "allow",
		PermissionDecisionReason: reason,
	}}
}

// rewrite builds a nested PreToolUse decision that replaces the tool input with
// updated and allows the (rewritten) call so it runs without a prompt. The
// rewrite target here is the read-only Discover subagent, so auto-allowing is
// consistent with clown's posture of not prompting for its own read-only
// surfaces.
func rewrite(updated json.RawMessage, reason string) *hookOutput {
	return &hookOutput{HookSpecificOutput: hookSpecificOutput{
		HookEventName:            "PreToolUse",
		PermissionDecision:       "allow",
		PermissionDecisionReason: reason,
		UpdatedInput:             updated,
	}}
}

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "clown-hook-allow: %v\n", err)
		os.Exit(1)
	}
}

func run(stdin io.Reader, stdout io.Writer) error {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	debugLog("input", string(data))

	var in hookInput
	if err := json.Unmarshal(data, &in); err != nil {
		debugLog("parse-error", err.Error())
		return fmt.Errorf("parsing hook input: %w", err)
	}

	out := evaluate(in)
	if out == nil {
		// Defer: emit nothing so the next hook / default permission logic
		// decides. ("defer" is not a real claude-code decision value; no
		// output is the canonical "no opinion".)
		debugLog("output", "(defer)")
		return nil
	}
	b, _ := json.Marshal(out)
	debugLog("output", string(b))
	if err := json.NewEncoder(stdout).Encode(out); err != nil {
		return fmt.Errorf("writing decision: %w", err)
	}
	return nil
}

// debugLog appends a tagged line to $CLOWN_HOOK_DEBUG_LOG when the env
// var is set. Used during development to confirm the hook is invoked
// and to capture its actual stdin/stdout.
func debugLog(tag, payload string) {
	path := os.Getenv("CLOWN_HOOK_DEBUG_LOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] %s\n", tag, payload)
}

// evaluate returns a nested PreToolUse decision for a trusted event, or nil to
// defer. It rewrites an Explore subagent launch to the Discover subagent,
// allows Read/Glob/Grep when the path argument is under /nix/store, and allows
// any clown-builtin-jobs MCP tool (jobToolPrefix); every other case defers.
func evaluate(in hookInput) *hookOutput {
	// Explore→Discover subagent rewrite: when the Agent tool is launched with
	// subagent_type "Explore", swap it for clown's read-only Discover subagent
	// and replace the whole tool input via updatedInput. All other input fields
	// (description, prompt, model, isolation, run_in_background) are preserved
	// because the full decoded object is re-marshaled. Malformed input or a
	// non-Explore subagent_type defers, leaving the call untouched.
	if subagentToolNames[in.ToolName] {
		var ti map[string]any
		if err := json.Unmarshal(in.ToolInput, &ti); err != nil {
			return nil // malformed input → defer
		}
		if st, _ := ti["subagent_type"].(string); st == exploreSubagent {
			ti["subagent_type"] = discoverSubagent
			updated, err := json.Marshal(ti)
			if err != nil {
				return nil // re-marshal failed → defer rather than emit garbage
			}
			return rewrite(updated, "Explore subagent redirected to clown's read-only Discover subagent")
		}
		return nil // Agent tool, but not an Explore launch → defer
	}

	switch in.ToolName {
	case "Read":
		var ti struct {
			FilePath string `json:"file_path"`
		}
		_ = json.Unmarshal(in.ToolInput, &ti)
		if isNixStorePath(ti.FilePath) {
			return allow("/nix/store reads are auto-allowed")
		}
	case "Glob":
		var ti struct {
			Path    string `json:"path"`
			Pattern string `json:"pattern"`
		}
		_ = json.Unmarshal(in.ToolInput, &ti)
		// Glob lets the caller supply a pattern with no path (the pattern
		// itself is absolute) or a path + relative pattern. Allow when
		// either is rooted in /nix/store.
		if isNixStorePath(ti.Path) || isNixStorePath(ti.Pattern) {
			return allow("/nix/store globs are auto-allowed")
		}
	case "Grep":
		var ti struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(in.ToolInput, &ti)
		if isNixStorePath(ti.Path) {
			return allow("/nix/store greps are auto-allowed")
		}
	}
	// clown's own job-wakeup MCP tools are auto-allowed regardless of arguments
	// (clown#130): they read and emit job-channel records that are part of the
	// intended cross-session workflow, and the channel is the same single-user
	// trust domain as any local write — so even broadcast job_message is allowed.
	if strings.HasPrefix(in.ToolName, jobToolPrefix) {
		return allow("clown-builtin-jobs job-channel tools are auto-allowed")
	}
	return nil // defer
}

// isNixStorePath reports whether p is anchored at /nix/store/. Empty
// strings, relative paths, and other absolute paths all return false.
func isNixStorePath(p string) bool {
	return strings.HasPrefix(p, nixStorePrefix)
}
