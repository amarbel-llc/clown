package main

import (
	"encoding/json"
	"time"

	"code.linenisgreat.com/clown/internal/taskowner"
)

// taskStopTools are the names under which Claude Code stops a background task:
// TaskStop, and the older KillShell/KillBash aliases the binary still carries.
var taskStopTools = map[string]bool{"TaskStop": true, "KillShell": true, "KillBash": true}

// unattendedEnv is the marker clown puts in claude's settings env for an
// unattended launch (cmd/clown claudeSafetySettingsJSON), which reaches the
// hook through claude's environment.
const unattendedEnv = "CLOWN_UNATTENDED"

// transcriptRetries and transcriptRetryWait give the transcript a moment to
// catch up before an id is refused: the launch record is written in the turn
// before the TaskStop, but writes are buffered. Only a would-be refusal waits.
var (
	transcriptRetries   = 2
	transcriptRetryWait = 150 * time.Millisecond
)

// evaluateTaskStop is the unattended TaskStop rule (operator ruling
// 2026-10-27, clown#330): the worker may stop only a task it started itself,
// so the wake monitor and the troupe monitor, which Claude Code starts from the
// plugin manifest, cannot be stopped from inside the session. It answers nil
// (no objection; the permission mode decides) for the model's own task and a
// deny for everything else, including every case it cannot decide: no task id,
// no readable transcript, an id the harness never recorded as launched.
func evaluateTaskStop(in hookInput) *hookOutput {
	var ti struct {
		TaskID  string `json:"task_id"`
		ShellID string `json:"shell_id"` // the legacy field of KillShell
	}
	if err := json.Unmarshal(in.ToolInput, &ti); err != nil {
		return deny("TaskStop needs a task_id; an unattended session may only stop a task it started itself")
	}
	id := ti.TaskID
	if id == "" {
		id = ti.ShellID
	}
	if id == "" {
		return deny("TaskStop needs a task_id; an unattended session may only stop a task it started itself")
	}
	for attempt := 0; ; attempt++ {
		own, err := taskowner.StartedByModel(in.TranscriptPath, id)
		if err != nil {
			debugLog("taskstop-error", err.Error())
			return deny("TaskStop refused: this unattended session's task list could not be checked, so no task can be stopped")
		}
		if own {
			return nil
		}
		if attempt >= transcriptRetries {
			break
		}
		time.Sleep(transcriptRetryWait)
	}
	return deny("TaskStop refused: task " + id + " was not started by this session's own tool calls. In an unattended session the wake and troupe monitors are not stoppable; only tasks you started yourself are.")
}
