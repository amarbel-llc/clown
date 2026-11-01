// Package taskowner answers one question about a session's background tasks:
// did the model start this task itself? clown-hook-allow uses it to refuse
// TaskStop on the tasks it must not let an unattended worker stop (the wake
// monitor and the troupe monitor are started by Claude Code from a plugin
// manifest, not by a tool call) while leaving the worker's own tasks stoppable.
//
// It reads the session transcript (JSONL). A task counts as the model's only
// when the harness itself recorded its launch: the user line that carries the
// tool_result of one of the model's own launching tool calls (Agent/Task for a
// background agent, Bash for a background command) also carries a structured
// `toolUseResult` object that Claude Code writes, outside the message content a
// model or sub-agent controls: {"status":"async_launched","agentId":...} for a
// background agent, {"backgroundTaskId":...} for a background command. Only
// that structured field is believed. The result TEXT is not: a foreground
// sub-agent's report, or a command's output, can print any id in any format
// (reviewer finding, 2026-11-01), while the structured field of a completed
// call says status "completed" or carries no backgroundTaskId. A monitor's id
// appears only in the system's task notifications.
package taskowner

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// maxLine bounds one transcript line; a longer one is skipped, not read whole.
const maxLine = 8 << 20

// launchingTools are the tools whose structured result can acknowledge a launch.
var launchingTools = map[string]bool{"Agent": true, "Task": true, "Bash": true}

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

	names := map[string]string{} // tool_use id -> tool name, launching tools only
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

// scanLine records launching tool_use ids and reports whether this line is the
// harness's launch record for taskID.
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
			if l.Type == "assistant" && launchingTools[b.Name] {
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

// launchedTask is the task id the harness's structured result records as
// launched by a tool of the given name, or "".
func launchedTask(tool string, raw json.RawMessage) string {
	var rec launchRecord
	if len(raw) == 0 || json.Unmarshal(raw, &rec) != nil {
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
