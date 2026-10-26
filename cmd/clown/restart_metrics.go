package main

import "code.linenisgreat.com/clown/internal/toolcost"

// Counters for a long unattended run: how often restarts were asked about,
// what spinclass answered, and whether a relaunch happened, so a two-week run
// can be read from stats-me instead of close-self-run.log. They only count;
// nothing here changes what the frontend does. Each is one statsd counter
// sent best effort (CLOWN_DISABLE_STATSD=1 turns them off with the rest).
//
//	clown.restart.preflight.asked            spinclass restart-preflight was run
//	clown.restart.preflight.verdict.<v>      its verdict: restart, none or refused
//	clown.restart.preflight.error            it failed, timed out or was unparsable
//	clown.restart.cancelled                  a consumed request was dropped (profile gate, countdown, busy)
//	clown.restart.refused                    the relaunch's own resume gate refused, after the stop
//	clown.restart.stopping                 the provider was told to stop for a relaunch
//	clown.restart.relaunch.exec              the old frontend is exec'ing the new one
//	clown.restart.relaunch.aborted           the relaunch did not happen (session closed, no clown, exec failed)
//	clown.restart.relaunched                 a frontend started as a restart's relaunch
//	clown.restart.wake.delivered             the relaunched session was told it was restarted (clown#329)
//	clown.restart.wake.failed                that wake could not be written
//	clown.status.write_failed                the hung-session status file could not be written
const (
	metricPreflightAsked    = "clown.restart.preflight.asked"
	metricPreflightVerdict  = "clown.restart.preflight.verdict."
	metricPreflightError    = "clown.restart.preflight.error"
	metricRestartCancelled  = "clown.restart.cancelled"
	metricRestartRefused    = "clown.restart.refused"
	metricRestartStopping   = "clown.restart.stopping"
	metricRelaunchExec      = "clown.restart.relaunch.exec"
	metricRelaunchAborted   = "clown.restart.relaunch.aborted"
	metricRelaunched        = "clown.restart.relaunched"
	metricRestartWoke       = "clown.restart.wake.delivered"
	metricRestartWakeFailed = "clown.restart.wake.failed"
	metricStatusWriteFail   = "clown.status.write_failed"
)

// countMetric sends one counter. A var so tests can record instead.
var countMetric = func(name string) {
	if toolcost.StatsdDisabled() {
		return
	}
	_ = toolcost.Send(toolcost.Encode([]toolcost.Metric{{Name: name, Kind: toolcost.KindCounter, Value: 1}}))
}
