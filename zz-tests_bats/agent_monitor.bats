# The frontend monitor relay (FDR 0020 "frontend monitors") against the real
# `ringmaster monitor`: `clown agent-monitor-serve` runs the monitor and
# serves its lines on a socket, `clown agent-monitor` reads them the way it
# does inside an agent unit. Covers delivery, and the mandatory case: the
# monitor is killed, a wake is produced while it is down, and that wake still
# arrives after the relay restarts it (ringmaster replays unacked records).
#
# No unit and no network: the relay's caller check needs a unit, so the
# harness verb skips it; everything else is the production code path.

setup() {
  load 'lib/common.bash'
  setup_test_home
  require_bin CLOWN_BIN clown
  require_bin RINGMASTER_BIN ringmaster
  export CLOWN_SESSION_ID="test/relay"
  # AF_UNIX path limit: keep the runtime dir short (see job_wakeup.bats).
  RELAY_RUNTIME_DIR="$(mktemp -d /tmp/crl.XXXXXX)"
  export XDG_RUNTIME_DIR="$RELAY_RUNTIME_DIR"
  SOCK="$RELAY_RUNTIME_DIR/m.sock"
  OUT="$BATS_TEST_TMPDIR/monitor.out"
  : >"$OUT"
}

teardown() {
  [[ -n ${READER_PID:-} ]] && kill "$READER_PID" 2>/dev/null
  [[ -n ${SERVE_PID:-} ]] && kill "$SERVE_PID" 2>/dev/null
  wait 2>/dev/null
  [[ -n ${RELAY_RUNTIME_DIR:-} ]] && rm -rf "$RELAY_RUNTIME_DIR"
  return 0
}

# wait_for_line waits up to 20s for $OUT to contain $1.
wait_for_line() {
  local i
  for i in $(seq 200); do
    grep -qF -- "$1" "$OUT" && return 0
    sleep 0.1
  done
  echo "never saw '$1' in:" >&2
  cat "$OUT" >&2
  return 1
}

# monitor_pid prints the pid of the real ringmaster monitor process (not the
# serve process, whose cmdline carries the same words).
monitor_pid() {
  local want p
  want="$(readlink -f "$RINGMASTER_BIN")"
  for p in /proc/[0-9]*; do
    [[ "$(readlink -f "$p/exe" 2>/dev/null)" == "$want" ]] || continue
    tr '\0' ' ' <"$p/cmdline" 2>/dev/null | grep -q 'monitor --session test/relay' || continue
    echo "${p#/proc/}"
    return 0
  done
  return 1
}

start_relay() {
  "$CLOWN_BIN" agent-monitor-serve --socket "$SOCK" --name ringmaster-monitor -- \
    "$RINGMASTER_BIN" monitor --session test/relay 2>"$BATS_TEST_TMPDIR/serve.log" &
  SERVE_PID=$!
  "$CLOWN_BIN" agent-monitor "$SOCK" >"$OUT" &
  READER_PID=$!
}

finish_job() {
  local id
  id="$("$RINGMASTER_BIN" start --source relay-test --label "$1")"
  "$RINGMASTER_BIN" done "$id" --state succeeded --message "$1"
  echo "$id"
}

@test "agent_monitor: a ringmaster wake reaches the unit side through the relay" {
  start_relay
  id="$(finish_job first)"
  wait_for_line "$id"
}

@test "agent_monitor: a wake produced while the frontend monitor is down arrives after it restarts" {
  start_relay
  first="$(finish_job first)"
  wait_for_line "$first"

  pid="$(monitor_pid)"
  kill -KILL "$pid"
  second="$(finish_job second)"
  wait_for_line "$second"
  # The relay restarted the monitor: a different process serves now.
  [[ "$(monitor_pid)" != "$pid" ]]
}
