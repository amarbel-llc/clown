# Conformance test for ringmaster's liveness reaping, now done by the host
# service itself (ringmaster FDR-0001, RFC-0020 §5.4; clown#257), consumed via
# the pinned flake input.
#
# juggler_lifecycle.bats exercises ExitWake's race-handling logic (the
# hook's own `done` losing a race against "whatever already wrote a
# terminal") entirely against the FAKE ringmaster binary's race-once knob;
# it never runs a real service. This file drives the real, pinned
# `ringmaster` binary's own producer surface directly — `ringmaster lock`,
# not `juggler run` — because the reaping is a ringmaster mechanism, not a
# juggler-specific one: a full agent run would need a bash-reachable
# model/tool-plane fixture this suite does not have, which is
# disproportionate for exercising a mechanism with no juggler-specific
# moving parts. `ringmaster lock <job> -- <command>` (ringmaster.1) is the
# documented way a CLI producer holds the same liveness hold `juggler run`
# holds in-process (RFC-0016 §1): running `sleep` under it and SIGKILLing
# the wrapper simulates a producer that crashed without writing a terminal
# record, exactly as a killed `juggler run` unit would.
#
# No network: this suite touches only the store under $XDG_STATE_HOME and the
# service socket under $XDG_RUNTIME_DIR. It runs in the standard nix sandbox
# and every other Linux sandbox we use.

setup() {
  load 'lib/common.bash'

  # bats-island isolation: fresh $HOME + XDG dirs (incl. XDG_STATE_HOME
  # and XDG_RUNTIME_DIR) all under $BATS_TEST_TMPDIR, same as
  # job_wakeup.bats.
  setup_test_home

  require_bin RINGMASTER_BIN ringmaster

  # Gotcha (job_wakeup.bats): setup_test_home's XDG_RUNTIME_DIR is too deep
  # for an AF_UNIX sun_path, and the service binds one there.
  CJR_RUNTIME_DIR="$(mktemp -d /tmp/cjr.XXXXXX)"
  export XDG_RUNTIME_DIR="$CJR_RUNTIME_DIR"

  export CLOWN_SESSION_ID="test/reaper-chan"

  # The store is private to the host service, which is also what reaps.
  start_ringmaster_service
}

teardown() {
  cleanup_pids "${LOCK_PID:-}"
  stop_ringmaster_service
  if [[ -n ${CJR_RUNTIME_DIR:-} ]]; then
    rm -rf "$CJR_RUNTIME_DIR"
  fi
}

# status's JSON liveness field: alive while the hold is kept, gone once the
# holder dies without a terminal record, before the service acts.
status_liveness() {
  "$RINGMASTER_BIN" status "$1" --json | jq -r '.liveness // empty'
}

status_state() {
  "$RINGMASTER_BIN" status "$1" --json | jq -r '.state // empty'
}

@test "the ringmaster service terminalizes a job whose lock-holding producer died without a terminal record" {
  local job
  job=$("$RINGMASTER_BIN" start --source test --label reaped)

  # A producer that holds the job for the life of `sleep 300`; killed
  # before it ever gets to run `done`.
  "$RINGMASTER_BIN" lock "$job" -- sleep 300 &
  LOCK_PID=$!

  local tries=0
  while [[ $(status_liveness "$job") != alive && $tries -lt 50 ]]; do
    sleep 0.1
    tries=$((tries + 1))
  done
  [[ $(status_liveness "$job") == alive ]]

  kill -KILL "$LOCK_PID"
  wait "$LOCK_PID" 2>/dev/null || true
  unset LOCK_PID

  # The hold ends the instant the holder dies, so status reads the producer
  # as gone — within the service's reconnect grace, before it writes anything.
  # The state is still running: death alone is not a terminal.
  tries=0
  while [[ $(status_liveness "$job") != gone && $tries -lt 20 ]]; do
    sleep 0.1
    tries=$((tries + 1))
  done
  [[ $(status_liveness "$job") == gone ]]
  [[ $(status_state "$job") == running ]]

  # The service itself writes `interrupted` once the hold is not renewed
  # within its grace (RFC-0020 §5.4); there is no separate reaper process to
  # run. Poll well past the grace.
  tries=0
  while [[ $(status_state "$job") == running && $tries -lt 150 ]]; do
    sleep 0.1
    tries=$((tries + 1))
  done
  [[ $(status_state "$job") == interrupted ]]

  # The message is the service's own, not merely "some terminal landed":
  # status --json has no field for a terminal's message, so read the
  # journal and check the terminal record itself.
  run "$RINGMASTER_BIN" read --job "$job" --json
  assert_success
  [[ $(jq -s -r '.[-1].message' <<<"$output") == "producer connection lost without writing a terminal record" ]]
}
