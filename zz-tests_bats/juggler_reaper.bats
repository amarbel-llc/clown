# Conformance test for ringmaster's host-wide liveness reaper (ADR-0001,
# ringmaster#33, consumed via the pinned flake input: clown#257).
#
# juggler_lifecycle.bats exercises ExitWake's race-handling logic (the
# hook's own `done` losing a race against "whatever already wrote a
# terminal") entirely against the FAKE ringmaster binary's race-once knob;
# it never runs a real reaper. This file drives the real, pinned
# `ringmaster` binary's own producer surface directly — `ringmaster lock`,
# not `juggler run` — because the host-wide reaper is a ringmaster
# mechanism, not a juggler-specific one: a full agent run would need a
# bash-reachable model/tool-plane fixture this suite does not have, which
# is disproportionate for exercising a mechanism with no juggler-specific
# moving parts. `ringmaster lock <job> -- <command>` (ringmaster.1) is the
# documented way a CLI producer holds the same advisory liveness lock
# `juggler run` holds in-process (RFC-0016 §1): running `sleep` under it
# and SIGKILLing the wrapper simulates a producer that crashed without
# writing a terminal record, exactly as a killed `juggler run` unit would.
#
# No network: this suite touches only the per-channel journal under
# $XDG_STATE_HOME and the host singleton lock under $XDG_RUNTIME_DIR. It
# runs in the standard nix sandbox and every other Linux sandbox we use.

setup() {
  load 'lib/common.bash'

  # bats-island isolation: fresh $HOME + XDG dirs (incl. XDG_STATE_HOME
  # and XDG_RUNTIME_DIR) all under $BATS_TEST_TMPDIR, same as
  # job_wakeup.bats.
  setup_test_home

  require_bin RINGMASTER_BIN ringmaster

  # Gotcha (job_wakeup.bats): setup_test_home's XDG_RUNTIME_DIR is too deep
  # for an AF_UNIX sun_path; short-circuit it the same way, even though
  # this file's only socket use is reap's own flock-based singleton (not a
  # bind), to stay consistent with the rest of the suite.
  CJR_RUNTIME_DIR="$(mktemp -d /tmp/cjr.XXXXXX)"
  export XDG_RUNTIME_DIR="$CJR_RUNTIME_DIR"

  export CLOWN_SESSION_ID="test/reaper-chan"
}

teardown() {
  cleanup_pids "${LOCK_PID:-}" "${REAP_PID:-}"
  if [[ -n ${CJR_RUNTIME_DIR:-} ]]; then
    rm -rf "$CJR_RUNTIME_DIR"
  fi
}

# status's JSON liveness field: alive while the lock is held, gone once
# the holder dies without a terminal record, before any reaper acts.
status_liveness() {
  "$RINGMASTER_BIN" status "$1" --json | jq -r '.liveness // empty'
}

status_state() {
  "$RINGMASTER_BIN" status "$1" --json | jq -r '.state // empty'
}

@test "ringmaster reap terminalizes a job whose lock-holding producer died without a terminal record" {
  local job
  job=$("$RINGMASTER_BIN" start --source test --label reaped)

  # A producer that holds the lock for the life of `sleep 300`; killed
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

  # Lock identity (RFC-0016 §1): the kernel releases the flock the instant
  # the holder dies, so status reads the producer as gone immediately —
  # before any reaper has had a chance to touch the job at all.
  [[ $(status_liveness "$job") == gone ]]
  [[ $(status_state "$job") == running ]]

  # `ringmaster reap`, the REAL pinned binary (not the fake platform used
  # elsewhere in this suite), against the SAME XDG_STATE_HOME/
  # XDG_RUNTIME_DIR this job's journal and lock live under.
  "$RINGMASTER_BIN" reap >"$BATS_TEST_TMPDIR/reap.log" 2>&1 &
  REAP_PID=$!
  wait_for_file "$BATS_TEST_TMPDIR/reap.log" 5
  grep -qF "reaping every channel on this host" "$BATS_TEST_TMPDIR/reap.log"

  # ringmaster.1: 3 continuous seconds of "gone" before the reaper
  # terminalizes, plus the reap loop's own ~1s scan cadence. Poll well
  # past that; this is the "within the grace" assertion.
  tries=0
  while [[ $(status_state "$job") == running && $tries -lt 100 ]]; do
    sleep 0.1
    tries=$((tries + 1))
  done
  [[ $(status_state "$job") == interrupted ]]

  # The reap unit itself exits 0 on SIGTERM, same as monitor's own clean
  # shutdown (ringmaster.1 SIGNALS).
  kill -TERM "$REAP_PID"
  wait "$REAP_PID"
  local rc=$?
  unset REAP_PID
  [[ $rc -eq 0 ]]
}
