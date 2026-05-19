# End-to-end smoke test for the ringmaster control plane.
#
# Boots the ringmaster daemon on a temp Unix socket, points it at
# the Go fake-llama-server fixture via --llama-server, then drives
# the surface through the circus CLI client (start / list / status
# / stop). The fake-llama-server only serves /health and
# /v1/models — same source as the launcher_test.go fixture — so
# nothing here exercises real llama-cpp.
#
# Binds 127.0.0.1 (each llama-server child picks its own port). The
# nix sandbox provides a fresh network namespace with loopback up;
# no net_cap escalation needed. See
# docs/adrs/0007-drop-net-cap-bats-file-tag.md.

setup_file() {
  load 'lib/common.bash'

  # All tests share one ringmaster daemon and act on the same
  # registry, so they must run in order — list must observe a start
  # the previous test fired, stop must follow the start that ran
  # before it. bats's default parallel-within-file scheduler would
  # race them. Serialize within this file; other *.bats files still
  # run in parallel across the lane.
  export BATS_NO_PARALLELIZE_WITHIN_FILE=true

  require_bin RINGMASTER_BIN ringmaster
  require_bin CIRCUS_BIN circus
  require_bin FAKE_LLAMA_SERVER_BIN fake-llama-server

  # Per-suite scratch under BATS_FILE_TMPDIR; bats wipes it on
  # teardown_file. The socket path stays short (no $HOME interpolation)
  # so we don't trip the macOS sun_path 104-byte limit when run
  # under launchd later — and the same path is reachable in CI Linux.
  export RM_DIR="$BATS_FILE_TMPDIR/rm"
  mkdir -p "$RM_DIR"
  export RINGMASTER_SOCKET="$RM_DIR/control.sock"

  # Spawn the daemon. --llama-server overrides the build-time
  # buildcfg.LlamaServerPath so the launcher exec's our fake. Stderr
  # is captured into rm.log for post-mortems on failed tests.
  "$RINGMASTER_BIN" daemon \
    --socket "$RINGMASTER_SOCKET" \
    --llama-server "$FAKE_LLAMA_SERVER_BIN" \
    >"$RM_DIR/rm.log" 2>&1 &
  export RM_PID=$!

  # Block until the daemon binds the socket (or 5 s elapse). The
  # daemon mkdirs and binds synchronously after argv parsing, so this
  # is bounded by Go startup time — typically <100 ms.
  local elapsed=0
  while [[ ! -S "$RINGMASTER_SOCKET" && $elapsed -lt 50 ]]; do
    sleep 0.1
    elapsed=$((elapsed + 1))
  done
  if [[ ! -S "$RINGMASTER_SOCKET" ]]; then
    echo "ringmaster never bound socket; log:" >&2
    cat "$RM_DIR/rm.log" >&2
    return 1
  fi
}

teardown_file() {
  if [[ -n "${RM_PID:-}" ]]; then
    kill "$RM_PID" 2>/dev/null || true
    wait "$RM_PID" 2>/dev/null || true
  fi
}

setup() {
  load 'lib/common.bash'
}

@test "list is empty before any starts" {
  run "$CIRCUS_BIN" list
  assert_success
  # cmdList prints nothing for an empty registry (rc=0, no header).
  assert_output ""
}

@test "start launches an instance and prints its address" {
  run "$CIRCUS_BIN" start --alias e2e-one fake-model
  assert_success
  assert_line --regexp '^circus: started e2e-one at 127\.0\.0\.1:[0-9]+ \(pid [0-9]+\)$'
}

@test "list shows the running instance with stable columns" {
  run "$CIRCUS_BIN" list
  assert_success
  assert_line --index 0 --regexp '^ALIAS *MODEL *BIND *PORT *PID *UPTIME$'
  assert_line --regexp '^e2e-one *fake-model *127\.0\.0\.1 *[0-9]+ *[0-9]+ *[0-9]+s$'
}

@test "status for the alias prints key:value detail" {
  run "$CIRCUS_BIN" status e2e-one
  assert_success
  assert_line --regexp '^alias: *e2e-one$'
  assert_line --regexp '^model: *fake-model$'
  assert_line --regexp '^bind: *127\.0\.0\.1$'
  assert_line --regexp '^port: *[0-9]+$'
  assert_line --regexp '^pid: *[0-9]+$'
}

@test "status with no args prints the table for one running instance" {
  run "$CIRCUS_BIN" status
  assert_success
  assert_line --index 0 --regexp '^ALIAS *MODEL *BIND *PORT *PID *UPTIME$'
  assert_line --regexp '^e2e-one'
}

@test "status for an unknown alias fails with not-found" {
  run "$CIRCUS_BIN" status no-such-alias
  assert_failure
  assert_output --partial 'not found'
}

@test "stop tears the instance down and list returns empty" {
  run "$CIRCUS_BIN" stop e2e-one
  assert_success
  assert_output --partial 'stopped e2e-one'

  run "$CIRCUS_BIN" list
  assert_success
  assert_output ""
}

@test "stop on already-stopped alias fails cleanly" {
  run "$CIRCUS_BIN" stop e2e-one
  assert_failure
  assert_output --partial 'not running'
}
