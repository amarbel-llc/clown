# Shared bash helpers for clown's bats integration suite.
# Loaded via `bats_load_library common.bash` from per-file setup().

# wait_for_file <path> [deadline_seconds]
# Block until <path> is non-empty or the deadline elapses (default 3 s).
# Polls every 100 ms. Does not fail if the deadline passes — callers are
# expected to assert on the file's contents and surface the failure.
wait_for_file() {
  local file="$1" deadline="${2:-3}" elapsed=0
  while [[ ! -s "$file" && $elapsed -lt $((deadline * 10)) ]]; do
    sleep 0.1
    elapsed=$((elapsed + 1))
  done
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
