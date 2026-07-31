# Integration test: the MCP-collapse aggregator fronting TWO trivial stdio MCP
# upstreams. It drives clown-mcp-collapse directly — the same wiring clown's own
# --mcp-collapse path builds internally — rather than the full `clown
# --mcp-collapse`, which needs a real claude in the sandbox that bats cannot
# drive. So this covers the AGGREGATOR end-to-end (the 3-verb collapse + the
# list/describe/call round-trip against real upstreams + the steering fragment);
# the cmd/clown flag-synthesis seam itself is covered by the Go unit tests on
# collapseBinding (cmd/clown/pluginbinding_test.go) and the default-path-unchanged
# assertion there.
#
# clown-mcp-collapse takes --upstream <name>=<url> where <url> is an MCP HTTP
# URL, but the mock fixture speaks stdio. So each of the two mock-stdio-mcp
# upstreams is first fronted by a clown-stdio-bridge (stdio -> streamable-HTTP),
# giving two HTTP URLs the aggregator can dial — exactly what pluginhost's
# stdioServers desugaring + StartAggregator produce in the real clown path.
#
# Binds 127.0.0.1. Works in the standard nix sandbox and every other Linux
# sandbox we use — all bring up `lo`. See docs/adrs/0007-drop-net-cap-bats-file-tag.md.

setup() {
  load 'lib/common.bash'
  setup_test_home

  require_bin CLOWN_MCP_COLLAPSE_BIN clown-mcp-collapse
  require_bin CLOWN_STDIO_BRIDGE_BIN clown-stdio-bridge
  require_bin MOCK_STDIO_MCP_BIN mock-stdio-mcp

  bridge_a_pid=
  bridge_b_pid=
  collapse_pid=

  # Front each mock with a bridge; capture each bridge's handshake to learn its
  # HTTP address.
  local hs_a="$BATS_TEST_TMPDIR/hs_a" hs_b="$BATS_TEST_TMPDIR/hs_b"
  local log_a="$BATS_TEST_TMPDIR/log_a" log_b="$BATS_TEST_TMPDIR/log_b"

  "$CLOWN_STDIO_BRIDGE_BIN" --command "$MOCK_STDIO_MCP_BIN" -- \
    >"$hs_a" 2>"$log_a" &
  bridge_a_pid=$!
  "$CLOWN_STDIO_BRIDGE_BIN" --command "$MOCK_STDIO_MCP_BIN" -- \
    >"$hs_b" 2>"$log_b" &
  bridge_b_pid=$!

  wait_for_file "$hs_a" 3
  wait_for_file "$hs_b" 3
  local addr_a addr_b
  addr_a="$(awk -F'|' '{print $4}' <"$hs_a")"
  addr_b="$(awk -F'|' '{print $4}' <"$hs_b")"
  if [[ -z $addr_a || -z $addr_b ]]; then
    echo "FAIL: a bridge produced no handshake within 3s" >&2
    cat "$log_a" "$log_b" >&2
    return 1
  fi

  # Start the aggregator fronting both bridge URLs, named alpha and beta.
  collapse_hs="$BATS_TEST_TMPDIR/collapse_hs"
  collapse_log="$BATS_TEST_TMPDIR/collapse_log"
  "$CLOWN_MCP_COLLAPSE_BIN" \
    --upstream "alpha=http://$addr_a/mcp" \
    --upstream "beta=http://$addr_b/mcp" \
    >"$collapse_hs" 2>"$collapse_log" &
  collapse_pid=$!

  wait_for_file "$collapse_hs" 5
  handshake="$(head -n1 "$collapse_hs")"
  if [[ -z $handshake ]]; then
    echo "FAIL: aggregator produced no handshake within 5s" >&2
    cat "$collapse_log" >&2
    return 1
  fi
  addr="$(awk -F'|' '{print $4}' <<<"$handshake")"
  base="http://$addr"
  export handshake base collapse_log
}

teardown() {
  cleanup_pids "$collapse_pid" "$bridge_a_pid" "$bridge_b_pid"
}

@test "aggregator emits the clown tcp/streamable-http handshake" {
  assert_regex "$handshake" '^1\|1\|tcp\|127\.0\.0\.1:[0-9]+\|streamable-http$'
}

@test "/healthz returns 200" {
  run curl -s -o /dev/null -w '%{http_code}' "$base/healthz"
  assert_success
  assert_output "200"
}

@test "tools/list surfaces EXACTLY the three collapse verbs, not the flat upstream tools" {
  run curl -sS -X POST "$base/mcp" \
    -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
  assert_success
  assert_output --partial '"name":"mcp_list"'
  assert_output --partial '"name":"mcp_describe"'
  assert_output --partial '"name":"mcp_call"'
  # The upstreams' own flat tool (echo) must NOT be in the harness-visible
  # catalog — that is the whole point of the collapse. echo only appears BEHIND
  # the verbs (mcp_list / mcp_describe / mcp_call), asserted below.
  refute_output --partial '"name":"echo"'
  refute_output --partial '"name":"alpha.echo"'
}

@test "mcp_list shows both upstreams' catalogs grouped by server" {
  run curl -sS -X POST "$base/mcp" \
    -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mcp_list","arguments":{}}}'
  assert_success
  # Both server groups and both echo tool_ids, dotted {server}.{tool}.
  assert_output --partial 'alpha.echo'
  assert_output --partial 'beta.echo'
}

@test "mcp_describe returns a real tool's schema block" {
  run curl -sS -X POST "$base/mcp" \
    -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"mcp_describe","arguments":{"tool_id":"alpha.echo"}}}'
  assert_success
  assert_output --partial 'alpha.echo'
  assert_output --partial 'inputSchema'
}

@test "mcp_call round-trips a real result from an upstream" {
  run curl -sS -X POST "$base/mcp" \
    -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"mcp_call","arguments":{"tool_id":"beta.echo","args":{}}}}'
  assert_success
  # The mock upstream's echo returns text "ok"; the aggregator passes the
  # upstream CallToolResult through verbatim.
  assert_output --partial '"text":"ok"'
}

@test "system-prompt fragment advertises the collapse verbs" {
  run curl -sS "$base/clown/system-prompt"
  assert_success
  assert_output --partial 'COLLAPSED'
  assert_output --partial 'mcp_list'
  assert_output --partial 'mcp_describe'
  assert_output --partial 'mcp_call'
}
