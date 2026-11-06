package main

// Test-side shorthands for restartControlHandlerTriggers: production builds
// the triggers form only (main.go), so these live with the tests that use
// them.

// restartControlHandler is the handler of a frontend with no triggers at all:
// `idle` is queued, `now` is refused.
func restartControlHandler(provider, unarmedReason string, q *restartQueue) func(controlMessage) controlReply {
	return restartControlHandlerNow(provider, unarmedReason, q, nil)
}

// restartControlHandlerNow is the handler with only the `now` trigger; now
// may be nil.
func restartControlHandlerNow(provider, unarmedReason string, q *restartQueue, now func(restartRequest) bool) func(controlMessage) controlReply {
	return restartControlHandlerTriggers(provider, unarmedReason, q, &restartTriggers{now: now})
}
