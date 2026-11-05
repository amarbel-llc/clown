// Package taskowner answers one question about a session's background tasks:
// did the model start this task itself? clown-hook-allow uses it to refuse
// TaskStop on the tasks it must not let an unattended worker stop (the wake
// monitor and the troupe monitor are started by Claude Code from a plugin
// manifest, not by a tool call) while leaving the worker's own tasks stoppable.
//
// It reads the session transcript (JSONL). A task counts as the model's only
// when the harness itself recorded its launch, in the user line that carries
// the tool_result of one of the model's own tool calls: that line also carries
// a `toolUseResult` field that Claude Code writes, outside the message content
// a model or sub-agent controls. Two records are believed:
//
//   - the structured launch record of a launching tool:
//     {"status":"async_launched","agentId":...} for a background agent
//     (Agent/Task), {"backgroundTaskId":...} for a background command (Bash).
//     The result TEXT is not believed: a foreground sub-agent's report, or a
//     command's output, can print any id in any format (reviewer finding,
//     2026-11-01), while the structured field of a completed call says status
//     "completed" or carries no backgroundTaskId.
//   - the harness's backgrounding notice for an MCP tool call it moved to the
//     background (clown#335): `MCP tool "..." is still running after 120s. It
//     was moved to the background as task <id> ...`. Claude Code 2.1.310 writes
//     no structured field for it; toolUseResult is the notice's one text block,
//     the same text the model sees. An MCP tool's own output can mimic that
//     text (jq prints what it is told to), so the notice is believed only for
//     an id with the prefix the harness gives MCP tasks: its task registry keys
//     ids by type (local_bash, which the plugin monitors are, "b"; local_agent
//     "a"; monitor_mcp "m"; monitor_ws "s"; mcp_task "k"). A mimicked notice
//     can therefore name at most another MCP call of this session, which the
//     session may stop anyway, never the wake or troupe monitor. `just
//     verify-claude-task-id-prefixes` checks the pinned claude still keys ids
//     that way.
//
// A monitor's id appears only in the system's task notifications.
package taskowner

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// maxLine bounds one transcript line; a longer one is skipped, not read whole.
const maxLine = 8 << 20

// launchingTools are the tools whose structured result can acknowledge a launch.
var launchingTools = map[string]bool{"Agent": true, "Task": true, "Bash": true}

// mcpToolPrefix names the tools whose call the harness may move to the
// background with a notice in place of a result; mcpTaskPrefix is the first
// character of every task id the harness mints for such a task (its mcp_task
// type) and of no monitor's.
const (
	mcpToolPrefix = "mcp__"
	mcpTaskPrefix = "k"
)

// mcpBackgroundedNotice is the harness's notice in its three wordings (the call
// ran past the foreground limit; a message arrived while it ran; the server
// took it over), anchored at the start of the text. Group 1 is the task id.
var mcpBackgroundedNotice = regexp.MustCompile(`^MCP tool "[^"]+" (?:is still running after [0-9]+s\. It |started server-side task [^ ]+\. It )?was moved to the background as task ([0-9a-z]+)(?: and keeps running| so that a message that arrived while it was running can reach you)`)

type block struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
}

type line struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	// ToolUseResult is the harness's structured record of the tool's result.
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

type launchRecord struct {
	Status           string `json:"status"`
	AgentID          string `json:"agentId"`
	BackgroundTaskID string `json:"backgroundTaskId"`
}

// StartedByModel reports whether taskID was launched by one of the model's own
// tool calls in the transcript at path. An unreadable transcript, a relative
// path or an empty id is an error or false, never true: the caller refuses.
func StartedByModel(path, taskID string) (bool, error) {
	if path == "" || !filepath.IsAbs(path) {
		return false, errors.New("taskowner: no absolute transcript path")
	}
	if taskID == "" {
		return false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("taskowner: %w", err)
	}
	defer f.Close()

	names := map[string]string{} // tool_use id -> tool name, launching and MCP tools only
	r := bufio.NewReaderSize(f, 1<<16)
	for {
		raw, tooLong, err := readLine(r)
		if len(raw) > 0 && !tooLong {
			if scanLine(raw, names, taskID) {
				return true, nil
			}
		}
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("taskowner: reading %s: %w", path, err)
		}
	}
}

// readLine returns the next line (without its newline). A line over maxLine is
// consumed and reported tooLong with no content.
func readLine(r *bufio.Reader) (raw []byte, tooLong bool, err error) {
	for {
		chunk, isPrefix, e := r.ReadLine()
		if e != nil {
			return raw, tooLong, e
		}
		if !tooLong {
			if len(raw)+len(chunk) > maxLine {
				tooLong, raw = true, nil
			} else {
				raw = append(raw, chunk...)
			}
		}
		if !isPrefix {
			return raw, tooLong, nil
		}
	}
}

// scanLine records the tool_use ids whose result can record a launch and
// reports whether this line is the harness's launch record for taskID.
func scanLine(raw []byte, names map[string]string, taskID string) bool {
	var l line
	if json.Unmarshal(raw, &l) != nil || len(l.Message.Content) == 0 {
		return false
	}
	var blocks []block
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		return false // plain-string content is a prompt or notification, never a tool result
	}
	for _, b := range blocks {
		switch b.Type {
		case "tool_use":
			if l.Type == "assistant" && (launchingTools[b.Name] || strings.HasPrefix(b.Name, mcpToolPrefix)) {
				names[b.ID] = b.Name
			}
		case "tool_result":
			name, ok := names[b.ToolUseID]
			if !ok || l.Type != "user" {
				continue
			}
			if launchedTask(name, l.ToolUseResult) == taskID {
				return true
			}
		}
	}
	return false
}

// launchedTask is the task id the harness's toolUseResult records as launched
// by a tool of the given name, or "".
func launchedTask(tool string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if strings.HasPrefix(tool, mcpToolPrefix) {
		return backgroundedMCPTask(raw)
	}
	var rec launchRecord
	if json.Unmarshal(raw, &rec) != nil {
		return ""
	}
	switch tool {
	case "Agent", "Task":
		if rec.Status == "async_launched" {
			return rec.AgentID
		}
	case "Bash":
		return rec.BackgroundTaskID
	}
	return ""
}

// backgroundedMCPTask is the MCP task id named by the harness's backgrounding
// notice when raw is exactly that notice: one text block, the notice from its
// first character, an id with the MCP task prefix. Anything else, an MCP tool's
// own output included, is "".
func backgroundedMCPTask(raw json.RawMessage) string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) != 1 || blocks[0].Type != "text" {
		return ""
	}
	m := mcpBackgroundedNotice.FindStringSubmatch(blocks[0].Text)
	if m == nil || !strings.HasPrefix(m[1], mcpTaskPrefix) {
		return ""
	}
	return m[1]
}
