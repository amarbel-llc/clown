// clown-hook-allow is a Claude Code PreToolUse hook handler. It reads
// the hook event JSON from stdin and emits a permission decision on
// stdout. For Read/Glob/Grep against paths under /nix/store, it returns
// "allow"; otherwise it returns "defer" (let downstream hooks or the
// default permission logic decide).
//
// The /nix/store auto-allow exists because store paths are
// content-addressed and immutable: reading them is information-only
// and carries no risk worth a permission prompt. We adopted this hook
// after empirically discovering that --allowed-tools "Read(/nix/store/**)"
// is not honored by claude-code 2.1 for the Read tool.
//
// Wire-up: clown's mkClownManagedSettings ships this binary's path as a
// PreToolUse handler in the managed-settings.json baked into the
// patched claude-code derivation.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

const nixStorePrefix = "/nix/store/"

type hookInput struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// decision is the bare hook output schema accepted by claude-code 2.1.
// Verified empirically that this form, the nested
// {"hookSpecificOutput":{...}} form, and even exit-0-with-no-stdout all
// produce the same allow outcome — so we use the simplest one.
type decision struct {
	PermissionDecision string `json:"permissionDecision"`
	Reason             string `json:"reason,omitempty"`
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

	d := evaluate(in)
	out, _ := json.Marshal(d)
	debugLog("output", string(out))

	enc := json.NewEncoder(stdout)
	if err := enc.Encode(d); err != nil {
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

// evaluate returns the permission decision for a PreToolUse event. It
// only allows Read/Glob/Grep when the path argument is under /nix/store;
// every other case defers.
func evaluate(in hookInput) decision {
	switch in.ToolName {
	case "Read":
		var ti struct {
			FilePath string `json:"file_path"`
		}
		_ = json.Unmarshal(in.ToolInput, &ti)
		if isNixStorePath(ti.FilePath) {
			return decision{PermissionDecision: "allow", Reason: "/nix/store reads are auto-allowed"}
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
			return decision{PermissionDecision: "allow", Reason: "/nix/store globs are auto-allowed"}
		}
	case "Grep":
		var ti struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(in.ToolInput, &ti)
		if isNixStorePath(ti.Path) {
			return decision{PermissionDecision: "allow", Reason: "/nix/store greps are auto-allowed"}
		}
	}
	return decision{PermissionDecision: "defer"}
}

// isNixStorePath reports whether p is anchored at /nix/store/. Empty
// strings, relative paths, and other absolute paths all return false.
func isNixStorePath(p string) bool {
	return strings.HasPrefix(p, nixStorePrefix)
}
