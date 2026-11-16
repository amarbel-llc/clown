// clown-hook-ajour is a Claude Code PreToolUse hook (matcher .*): it
// evaluates each call against the session's ajour layers and records the
// verdict (internal/*/ajourshadow, RFC 0018 §11, clown#349). In shadow, the
// only mode the operator has ruled, it prints nothing, so every call goes on
// to the other hooks and the permission logic exactly as before.
//
// The session's principal arrives as CLOWN_SESSION_ID on the hook's command
// line (clown does not export it ambiently; see teeHookCommand). Without
// one, or without a layers file, the hook does nothing. It always exits 0: a
// broken hook must never break the call.
package main

import (
	"encoding/json"
	"io"
	"os"

	"code.linenisgreat.com/clown/internal/bravo/ajourshadow"
)

// event is the part of the PreToolUse payload the hook reads.
type event struct {
	ToolName  string                     `json:"tool_name"`
	ToolInput map[string]json.RawMessage `json:"tool_input"`
	Cwd       string                     `json:"cwd"`
}

// output is the nested PreToolUse decision claude-code honors for every
// tool, MCP tools included (see cmd/clown-hook-allow).
type output struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
	} `json:"hookSpecificOutput"`
}

func main() {
	principal := os.Getenv("CLOWN_SESSION_ID")
	raw, err := io.ReadAll(os.Stdin)
	if err != nil || principal == "" {
		return
	}
	var ev event
	if json.Unmarshal(raw, &ev) != nil || ev.ToolName == "" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	d := ajourshadow.Hook(principal, ev.ToolName, ev.ToolInput, ev.Cwd, home)
	if !d.Deny {
		return
	}
	var out output
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	out.HookSpecificOutput.PermissionDecision = "deny"
	out.HookSpecificOutput.PermissionDecisionReason = d.Reason
	_ = json.NewEncoder(os.Stdout).Encode(out)
}
