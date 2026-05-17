# Integration test: launch clown-stdio-bridge wrapping a mock stdio MCP
# server. Verifies the handshake/healthcheck path AND the streamable-HTTP
# MCP translation path: client POSTs an `initialize` request and receives
# a JSON response with the matching id; client triggers a server-initiated
# notification via a `notify-broadcast` request and observes it on the
# GET SSE stream.
#
# Binds 127.0.0.1. This works in the standard nix sandbox (`sandbox = true`)
# and every other Linux sandbox we currently use — all bring up `lo` inside
# their network namespace. See ADR docs/adrs/0007-drop-net-cap-bats-file-tag.md.

setup() {
  load 'lib/common.bash'

  # bats-island gives us a fresh $HOME, XDG dirs, and writable scratch
  # space under $BATS_TEST_TMPDIR. The bridge and the curl client both
  # write under here; teardown_test_home is implicit via bats's per-test
  # tmpdir cleanup.
  setup_test_home

  require_bin CLOWN_STDIO_BRIDGE_BIN clown-stdio-bridge
  require_bin MOCK_STDIO_MCP_BIN mock-stdio-mcp

  bridge_pid=
  sse_pid=
  handshake_file="$BATS_TEST_TMPDIR/handshake"
  log_file="$BATS_TEST_TMPDIR/log"
  sse_out="$BATS_TEST_TMPDIR/sse_out"

  "$CLOWN_STDIO_BRIDGE_BIN" --command "$MOCK_STDIO_MCP_BIN" -- \
    >"$handshake_file" 2>"$log_file" &
  bridge_pid=$!

  wait_for_file "$handshake_file" 3
  handshake="$(head -n1 "$handshake_file")"
  if [[ -z "$handshake" ]]; then
    echo "FAIL: bridge produced no handshake within 3s" >&2
    cat "$log_file" >&2
    return 1
  fi
  addr="$(awk -F'|' '{print $4}' <<<"$handshake")"
  base="http://$addr"
  export handshake addr base log_file sse_out
}

teardown() {
  cleanup_pids "$sse_pid" "$bridge_pid"
}

@test "handshake emits tcp/streamable-http format" {
  assert_regex "$handshake" '^1\|1\|tcp\|127\.0\.0\.1:[0-9]+\|streamable-http$'
}

@test "/healthz returns 200" {
  run curl -s -o /dev/null -w '%{http_code}' "$base/healthz"
  assert_success
  assert_output "200"
}

@test "initialize round-trip preserves jsonrpc id and serverInfo" {
  run curl -sS -X POST "$base/mcp" \
    -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}'
  assert_success
  assert_output --partial '"id":1'
  assert_output --partial 'mock-stdio-mcp'
}

@test "tools/list surfaces echo tool" {
  run curl -sS -X POST "$base/mcp" \
    -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
  assert_success
  assert_output --partial '"name":"echo"'
}

@test "SSE broadcast forwards server-initiated notifications" {
  curl -sNS "$base/mcp" >"$sse_out" 2>/dev/null &
  sse_pid=$!
  sleep 0.2

  curl -sS -X POST "$base/mcp" \
    -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":3,"method":"notify-broadcast"}' >/dev/null

  deadline=$(( $(date +%s) + 3 ))
  while [[ $(date +%s) -lt $deadline ]]; do
    if grep -q 'tools/list_changed' "$sse_out" 2>/dev/null; then
      break
    fi
    sleep 0.1
  done

  run grep -q 'tools/list_changed' "$sse_out"
  if (( status != 0 )); then
    echo "--- sse_out ---" >&2
    cat "$sse_out" >&2
    echo "--- bridge log ---" >&2
    cat "$log_file" >&2
  fi
  assert_success
}
