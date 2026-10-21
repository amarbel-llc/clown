# Shared bash helpers for clown's bats integration suite.
# Loaded via `load 'lib/common.bash'` from per-file setup().
#
# Pulls in the standard amarbel-llc/bats helper vocabulary:
#   - bats-support  : internal infra for assert/island
#   - bats-assert   : assert_success / assert_output / assert_regex / ...
#   - bats-emo      : require_bin <ENV_VAR> <name> — fail fast if binary
#                     is not in scope
#   - bats-island   : setup_test_home — fresh $HOME, XDG dirs,
#                     GIT_CONFIG_GLOBAL, all rooted at $BATS_TEST_TMPDIR

bats_load_library bats-support
bats_load_library bats-assert
bats_load_library bats-emo
bats_load_library bats-island

# wait_for_file <path> [deadline_seconds]
# Block until <path> is non-empty or the deadline elapses (default 3 s).
# Polls every 100 ms. Does not fail if the deadline passes — callers are
# expected to assert on the file's contents and surface the failure.
wait_for_file() {
  local file="$1" deadline="${2:-3}" elapsed=0
  while [[ ! -s $file && $elapsed -lt $((deadline * 10)) ]]; do
    sleep 0.1
    elapsed=$((elapsed + 1))
  done
}

# start_ringmaster_service
# Run `ringmaster serve` — the job store's host service (ringmaster FDR-0001,
# RFC-0020), which is the store's only reader and writer — for this test, and
# block until its socket exists. Needs RINGMASTER_BIN, XDG_STATE_HOME for the
# store, and a SHORT XDG_RUNTIME_DIR (the socket path is limited to ~108
# bytes). Pair with stop_ringmaster_service in teardown().
start_ringmaster_service() {
  mkdir -p "$XDG_RUNTIME_DIR"
  "$RINGMASTER_BIN" serve >"$BATS_TEST_TMPDIR/ringmaster-serve.log" 2>&1 &
  RINGMASTER_SERVICE_PID=$!
  local sock="$XDG_RUNTIME_DIR/ringmaster/service.sock" tries=0
  while [[ $tries -lt 100 ]]; do
    [[ -S $sock ]] && return 0
    sleep 0.1
    tries=$((tries + 1))
  done
  echo "ringmaster serve did not come up; its log:" >&2
  cat "$BATS_TEST_TMPDIR/ringmaster-serve.log" >&2
  return 1
}

# stop_ringmaster_service — stop what start_ringmaster_service started.
stop_ringmaster_service() {
  if [[ -n ${RINGMASTER_SERVICE_PID:-} ]]; then
    cleanup_pids "$RINGMASTER_SERVICE_PID"
    RINGMASTER_SERVICE_PID=
  fi
}

# cleanup_pids <pid>...
# SIGTERM each pid and wait for it to reap. Tolerates already-dead pids.
# Use from teardown() so test failures don't leak background processes.
cleanup_pids() {
  for pid in "$@"; do
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
}
