package main

import (
	"os"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/mcphttp"
)

// setOrUnsetEnv sets (or unsets) an env var for the duration of the test,
// restoring the original state on cleanup. Unlike t.Setenv it can model an
// absent variable, which heartbeatMode distinguishes from an empty value.
func setOrUnsetEnv(t *testing.T, key, val string, set bool) {
	t.Helper()
	orig, had := os.LookupEnv(key)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, orig)
		} else {
			_ = os.Unsetenv(key)
		}
	})
	if set {
		_ = os.Setenv(key, val)
	} else {
		_ = os.Unsetenv(key)
	}
}

func TestHeartbeatMode(t *testing.T) {
	tests := []struct {
		name         string
		mode         string
		modeSet      bool
		interval     string
		intervalSet  bool
		wantStream   bool
		wantInterval time.Duration
		wantFallback bool
	}{
		{name: "default: stream at default cadence", wantStream: true, wantInterval: heartbeatDefault},
		{name: "explicit interval", interval: "5s", intervalSet: true, wantStream: true, wantInterval: 5 * time.Second},
		{name: "interval off disables streaming", interval: "off", intervalSet: true, wantStream: false, wantInterval: 0},
		{name: "forward-only streams without timer", mode: "forward-only", modeSet: true, wantStream: true, wantInterval: 0},
		{name: "child alias", mode: "child", modeSet: true, wantStream: true, wantInterval: 0},
		{name: "forward-only is case/space insensitive", mode: "  Forward-Only  ", modeSet: true, wantStream: true, wantInterval: 0},
		{name: "forward-only overrides a short interval", mode: "forward-only", modeSet: true, interval: "20ms", intervalSet: true, wantStream: true, wantInterval: 0},
		{name: "fallback arms an activity-gated timer at the interval", mode: "forward-only+fallback", modeSet: true, interval: "20ms", intervalSet: true, wantStream: true, wantInterval: 20 * time.Millisecond, wantFallback: true},
		{name: "fallback alias child+fallback", mode: "child+fallback", modeSet: true, interval: "5s", intervalSet: true, wantStream: true, wantInterval: 5 * time.Second, wantFallback: true},
		{name: "fallback with no/off interval uses the default threshold", mode: "forward-only+fallback", modeSet: true, interval: "off", intervalSet: true, wantStream: true, wantInterval: heartbeatDefault, wantFallback: true},
		{name: "unknown mode falls back to interval", mode: "bogus", modeSet: true, interval: "5s", intervalSet: true, wantStream: true, wantInterval: 5 * time.Second},
		{name: "unknown mode falls back to default", mode: "bogus", modeSet: true, wantStream: true, wantInterval: heartbeatDefault},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setOrUnsetEnv(t, heartbeatModeEnvVar, tt.mode, tt.modeSet)
			setOrUnsetEnv(t, heartbeatEnvVar, tt.interval, tt.intervalSet)
			gotStream, gotInterval, gotFallback := heartbeatMode()
			if gotStream != tt.wantStream {
				t.Errorf("streaming = %v, want %v", gotStream, tt.wantStream)
			}
			if gotInterval != tt.wantInterval {
				t.Errorf("interval = %v, want %v", gotInterval, tt.wantInterval)
			}
			if gotFallback != tt.wantFallback {
				t.Errorf("fallback = %v, want %v", gotFallback, tt.wantFallback)
			}
		})
	}
}

// TestResolveHeartbeat confirms resolveHeartbeat maps heartbeatMode's resolved
// triple onto the mcphttp.Heartbeat the spine consumes, for the three shapes
// (fixed cadence, forward-only, activity-gated fallback) plus the off case.
func TestResolveHeartbeat(t *testing.T) {
	tests := []struct {
		name        string
		mode        string
		modeSet     bool
		interval    string
		intervalSet bool
		want        mcphttp.Heartbeat
	}{
		{name: "off", interval: "off", intervalSet: true, want: mcphttp.Heartbeat{Streaming: false, Interval: 0, Fallback: false}},
		{name: "fixed cadence", interval: "20ms", intervalSet: true, want: mcphttp.Heartbeat{Streaming: true, Interval: 20 * time.Millisecond, Fallback: false}},
		{name: "forward-only", mode: "forward-only", modeSet: true, want: mcphttp.Heartbeat{Streaming: true, Interval: 0, Fallback: false}},
		{name: "activity-gated fallback", mode: "forward-only+fallback", modeSet: true, interval: "50ms", intervalSet: true, want: mcphttp.Heartbeat{Streaming: true, Interval: 50 * time.Millisecond, Fallback: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setOrUnsetEnv(t, heartbeatModeEnvVar, tt.mode, tt.modeSet)
			setOrUnsetEnv(t, heartbeatEnvVar, tt.interval, tt.intervalSet)
			if got := resolveHeartbeat(); got != tt.want {
				t.Errorf("resolveHeartbeat() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
