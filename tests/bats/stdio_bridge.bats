# bats file_tags=net_cap
#
# Integration test: launch clown-stdio-bridge wrapping a mock stdio MCP
# server. Verifies the handshake/healthcheck path AND the streamable-HTTP
# MCP translation path: client POSTs an `initialize` request and receives
# a JSON response with the matching id; client triggers a server-initiated
# notification via a `notify-broadcast` request and observes it on the
# GET SSE stream.
#
# Tagged net_cap because the bridge binds 127.0.0.1 — the nix sandbox
# does not always grant loopback, so this lane is opt-in via
# `nix build .#bats-net_cap`.

load 'lib/common.bash'

setup() {
  bridge_pid=
  sse_pid=
  handshake_file="$(mktemp)"
  log_file="$(mktemp)"
  sse_out="$(mktemp)"

  "$CLOWN_STDIO_BRIDGE_BIN" --command "$MOCK_STDIO_MCP_BIN" -- \
    >"$handshake_file" 2>"$log_file" &
  bridge_pid=$!

  wait_for_file "$handshake_file" 3
  handshake="$(head -n1 "$handshake_file")"
  [[ -n "$handshake" ]] || {
    echo "FAIL: bridge produced no handshake within 3s" >&2
    cat "$log_file" >&2
    return 1
  }
  addr="$(awk -F'|' '{print $4}' <<<"$handshake")"
  base="http://$addr"
  export handshake addr base
}

teardown() {
  cleanup_pids "$sse_pid" "$bridge_pid"
  rm -f "$handshake_file" "$log_file" "$sse_out"
}

@test "handshake emits tcp/streamable-http format" {
  [[ "$handshake" =~ ^1\|1\|tcp\|127\.0\.0\.1:[0-9]+\|streamable-http$ ]]
}

@test "/healthz returns 200" {
  status="$(curl -s -o /dev/null -w '%{http_code}' "$base/healthz")"
  [[ "$status" == "200" ]]
}

@test "initialize round-trip preserves jsonrpc id and serverInfo" {
  resp="$(curl -sS -X POST "$base/mcp" \
    -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}')"
  [[ "$resp" == *'"id":1'* ]]
  [[ "$resp" == *'mock-stdio-mcp'* ]]
}

@test "tools/list surfaces echo tool" {
  resp="$(curl -sS -X POST "$base/mcp" \
    -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}')"
  [[ "$resp" == *'"name":"echo"'* ]]
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

  if ! grep -q 'tools/list_changed' "$sse_out"; then
    echo "--- sse_out ---" >&2
    cat "$sse_out" >&2
    echo "--- bridge log ---" >&2
    cat "$log_file" >&2
    return 1
  fi
}
