// Added for mcp-collapse permission-mux POC (throwaway; stage 1 mechanics).
//
// clown-hook-collapse is a Claude Code PreToolUse hook that restores
// per-upstream-tool permission granularity under --mcp-collapse. When N upstream
// MCP plugins collapse behind the three aggregator verbs
// (mcp_list/mcp_describe/mcp_call), the harness only ever sees ONE side-effecting
// tool — mcp_call — so its normal per-tool allow/ask/deny prompt can no longer
// distinguish "read a file" from "push to a remote": both arrive as mcp_call. This
// hook demuxes the collapsed call: it reads the mcp_call arguments' tool_id (the
// dotted "{server}.{tool}" demux key), looks it up in a policy map, and emits the
// matching per-tool permission decision so claude honors it live.
//
// Stage 1 (this file) uses a HARDCODED policy map (stage1Policy) so we can prove the
// hook MECHANICS — matching the collapsed tool, extracting tool_id, applying a
// decision, and failing open — in isolation from policy plumbing. Stage 2 later
// swaps stage1Policy for policy captured from the upstream plugins before collapse
// drops their dirs.
//
// This hook ONLY speaks for the collapsed mcp_call tool. For any other tool_name it
// emits nothing (defers to the next hook / claude's default permission logic). It
// also fails open: any parse error, a missing tool_id, or a tool_id absent from the
// policy map all defer rather than block — an unknown collapsed tool prompts as
// claude normally would.
//
// Output form: the decision MUST be the nested hookSpecificOutput object
// (hookEventName/permissionDecision/permissionDecisionReason). The older bare
// {"permissionDecision":...} form is IGNORED for MCP plugin tools (mcp__*) as of
// claude-code 2.1.177 (clown#130) — and the collapsed tool IS an mcp__* tool — so the
// nested form is mandatory here. This mirrors clown-hook-allow exactly.
//
// Wire-up: clown's collapseBinding.synthAggregatorPluginDir writes a
// hooks/hooks.json into the synthesized aggregator plugin dir registering this binary
// as a PreToolUse handler (matcher ".*"), passed to claude via --plugin-dir. The
// binary path is baked in via the buildcfg.McpCollapseHookPath ldflag; empty in dev
// builds, where the hook is omitted.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Stage-1 POC policy: hardcoded so we test hook MECHANICS, not policy plumbing.
// tool_id (dotted {server}.{tool}) -> decision. Stage 2 replaces this with
// policy captured from the upstream plugins before collapse drops their dirs.
// These use moxy tools that WILL appear under collapse in the live demo.
var stage1Policy = map[string]string{
	"moxy/moxy.folio_read":  "allow",
	"moxy/moxy.folio_write": "ask",
	"moxy/moxy.grit_push":   "deny",
}

// mcpCallToolSuffix / collapsePluginMarker identify the collapsed mcp_call verb.
// claude names a plugin server's tools `mcp__plugin_<plugin>_<server>__<tool>`; with
// plugin name `clown-mcp-collapse` (pluginbinding.go synthAggregatorPluginDir) and
// server key `mcp-collapse`, the full name is
// `mcp__plugin_clown-mcp-collapse_mcp-collapse__mcp_call`. We key on the `__mcp_call`
// suffix so the match survives a namespacing shift, and additionally require the
// aggregator plugin marker so a coincidentally-named `mcp_call` from an unrelated
// server is never claimed by this hook.
const (
	mcpCallToolSuffix    = "__mcp_call"
	collapsePluginMarker = "clown-mcp-collapse"
)

type hookInput struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// hookSpecificOutput is the nested PreToolUse permission-decision payload claude-code
// honors for ALL tools, including MCP plugin tools (clown#130). The bare
// {"permissionDecision":...} form is insufficient for mcp__* tools as of claude-code
// 2.1.177 — hence the nested shape, copied from clown-hook-allow.
type hookSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

type hookOutput struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

// decision builds a nested PreToolUse decision (allow/ask/deny) carrying reason.
func decision(perm, reason string) *hookOutput {
	return &hookOutput{HookSpecificOutput: hookSpecificOutput{
		HookEventName:            "PreToolUse",
		PermissionDecision:       perm,
		PermissionDecisionReason: reason,
	}}
}

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "clown-hook-collapse: %v\n", err)
		os.Exit(1)
	}
}

func run(stdin io.Reader, stdout io.Writer) error {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	var in hookInput
	if err := json.Unmarshal(data, &in); err != nil {
		// Fail open: a hook input we cannot parse must not block the tool. Emit
		// nothing (defer) and report a clean nil error so we exit 0.
		fmt.Fprintf(os.Stderr, "clown-hook-collapse: parse error (fail-open, deferring): %v\n", err)
		return nil
	}

	out := evaluate(in)
	if out == nil {
		// Defer: emit nothing so the next hook / default permission logic decides.
		return nil
	}
	if err := json.NewEncoder(stdout).Encode(out); err != nil {
		return fmt.Errorf("writing decision: %w", err)
	}
	return nil
}

// evaluate demuxes a collapsed mcp_call and returns its per-tool permission decision,
// or nil to defer. It acts ONLY on the aggregator's mcp_call tool; every other
// tool_name defers. It fails open — missing/unparseable tool_input, an absent
// tool_id, or a tool_id not in stage1Policy all defer rather than block. Diagnostics
// go to stderr so the hook firing is observable in the plugin-host log during the live
// test.
func evaluate(in hookInput) *hookOutput {
	if !isCollapsedMcpCall(in.ToolName) {
		return nil // not the collapsed mcp_call tool → defer (this hook only speaks for mcp_call)
	}

	var ti struct {
		ToolID string `json:"tool_id"`
	}
	if err := json.Unmarshal(in.ToolInput, &ti); err != nil {
		fmt.Fprintf(os.Stderr, "clown-hook-collapse: mcp_call tool_input unparseable (fail-open, deferring): %v\n", err)
		return nil // fail open
	}
	if ti.ToolID == "" {
		fmt.Fprintln(os.Stderr, "clown-hook-collapse: mcp_call had no tool_id (fail-open, deferring)")
		return nil // fail open
	}

	perm, ok := stage1Policy[ti.ToolID]
	if !ok {
		// Miss: unknown collapsed tool_id → defer to claude's normal flow
		// (fail-open, don't block unknown tools).
		fmt.Fprintf(os.Stderr, "clown-hook-collapse: tool_id=%q not in policy (deferring)\n", ti.ToolID)
		return nil
	}

	reason := fmt.Sprintf("mcp-collapse POC: tool_id=%s policy=%s", ti.ToolID, perm)
	fmt.Fprintf(os.Stderr, "clown-hook-collapse: tool_id=%q decision=%s\n", ti.ToolID, perm)
	return decision(perm, reason)
}

// isCollapsedMcpCall reports whether toolName is the aggregator's collapsed mcp_call
// verb. It requires BOTH the `__mcp_call` suffix and the aggregator plugin marker so
// an unrelated server's `mcp_call` (should one ever exist) is not claimed here.
func isCollapsedMcpCall(toolName string) bool {
	return strings.HasSuffix(toolName, mcpCallToolSuffix) && strings.Contains(toolName, collapsePluginMarker)
}
