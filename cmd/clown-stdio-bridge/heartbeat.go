package main

import (
	"os"
	"strings"
	"time"

	"code.linenisgreat.com/clown/internal/mcphttp"
)

// heartbeatEnvVar selects the cadence at which the bridge emits keep-alive
// activity on streaming responses. Unset uses heartbeatDefault. "0" or
// "off" disables heartbeats AND falls back to plain application/json
// responses (legacy behavior). Any other value is parsed by
// time.ParseDuration.
const heartbeatEnvVar = "CLOWN_BRIDGE_HEARTBEAT_INTERVAL"

// heartbeatModeEnvVar selects a named heartbeat mode that overrides the
// interval-derived policy. Recognized overrides:
//   - "forward-only" (alias "child"): keep SSE streaming on so child
//     notifications/progress and the final response are delivered, but
//     suppress the bridge's own timer so heartbeats are activity-driven by the
//     child alone.
//   - "forward-only+fallback" (alias "child+fallback"): like forward-only, but
//     arm an ACTIVITY-GATED fallback timer — the bridge emits a heartbeat only
//     after the child has been silent for heartbeatEnvVar's interval, re-arming
//     on every child message. A bridge-side keep-alive ceiling that still lets
//     a genuinely hung child time out (clown#109).
//
// Unset or any unrecognized value falls back to the heartbeatEnvVar cadence.
const heartbeatModeEnvVar = "CLOWN_BRIDGE_HEARTBEAT"

const heartbeatDefault = 30 * time.Second

// resolveHeartbeat reads the bridge's CLOWN_BRIDGE_* heartbeat env vars and
// returns the resolved mcphttp.Heartbeat policy the spine applies per POST.
// The env-reading lives here (bridge-side), not in the shared spine, so the
// spine carries no policy source of its own.
//
// These env vars are read ONCE, at handler construction (newHTTPHandler folds
// the result into mcphttp.Config.Heartbeat), NOT per-request. A test that
// mutates CLOWN_BRIDGE_HEARTBEAT_INTERVAL / CLOWN_BRIDGE_HEARTBEAT must do so
// BEFORE constructing the handler — flipping them afterward has no effect on an
// already-built server.
func resolveHeartbeat() mcphttp.Heartbeat {
	streaming, interval, fallback := heartbeatMode()
	return mcphttp.Heartbeat{
		Streaming: streaming,
		Interval:  interval,
		Fallback:  fallback,
	}
}

// heartbeatInterval reports the configured heartbeat cadence. Returns 0
// when heartbeats are disabled.
func heartbeatInterval() time.Duration {
	v, set := os.LookupEnv(heartbeatEnvVar)
	if !set {
		return heartbeatDefault
	}
	switch v {
	case "0", "off", "":
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return heartbeatDefault
	}
	return d
}

// heartbeatMode resolves the per-POST streaming/timer policy from the two
// heartbeat env vars. It reports whether the response should stream
// (text/event-stream), the timer interval, and whether that interval is
// activity-gated (fallback). heartbeatModeEnvVar takes precedence.
//
//   - "forward-only"/"child": stream, no bridge timer (interval 0) — keep-alive
//     is the child's own notifications/progress alone.
//   - "forward-only+fallback"/"child+fallback": stream, and arm an
//     activity-gated fallback timer (fallback=true) whose threshold reuses
//     heartbeatInterval() (heartbeatDefault when that is 0/off).
//   - otherwise: the heartbeatEnvVar cadence as a fixed-interval timer
//     (fallback=false; streaming when the cadence is > 0).
func heartbeatMode() (streaming bool, interval time.Duration, fallback bool) {
	if v, set := os.LookupEnv(heartbeatModeEnvVar); set {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "forward-only", "child":
			return true, 0, false
		case "forward-only+fallback", "child+fallback":
			iv := heartbeatInterval()
			if iv <= 0 {
				iv = heartbeatDefault
			}
			return true, iv, true
		}
	}
	iv := heartbeatInterval()
	return iv > 0, iv, false
}
