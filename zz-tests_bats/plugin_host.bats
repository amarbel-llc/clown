# Integration test: launch clown-plugin-host with the synthetic plugin's
# clown.json and verify the mock HTTP MCP server starts, completes the
# handshake, passes health checks, compiles plugin manifests with
# URL-based MCP entries, and preserves original server names.
#
# Binds 127.0.0.1 (synthetic plugin's MCP server). Works in the standard
# nix sandbox and every other Linux sandbox we use; see ADR
# docs/adrs/0007-drop-net-cap-bats-file-tag.md.

setup() {
  load 'lib/common.bash'

  # bats-island isolation: gives us a fresh $HOME, XDG dirs, and
  # GIT_CONFIG_GLOBAL all under $BATS_TEST_TMPDIR. Among other
  # things this sets XDG_*_HOME, which clown-plugin-host's OpenLog
  # consults when it picks a log directory — replacing the old
  # inline `export XDG_LOG_HOME=...` workaround.
  setup_test_home

  require_bin CLOWN_PLUGIN_HOST_BIN clown-plugin-host

  inspect="$BATS_TEST_DIRNAME/inspect-compiled"
  chmod +x "$inspect"

  # `timeout` is provided by coreutils on PATH inside the bats lane.
  output="$(timeout 30 "$CLOWN_PLUGIN_HOST_BIN" \
    --plugin-dir "$SYNTHETIC_PLUGIN_DIR" \
    -- "$inspect" 2>&1)" || {
    echo "FAIL: clown-plugin-host exited with $?" >&2
    echo "$output" >&2
    return 1
  }
  export output

  compiled_json="$(echo "$output" \
    | sed -n '/COMPILED_PLUGIN_JSON_START/,/COMPILED_PLUGIN_JSON_END/p' \
    | grep -v 'COMPILED_PLUGIN_JSON_')"
  [[ -n "$compiled_json" ]] || {
    echo "FAIL: could not extract compiled plugin.json from output" >&2
    echo "$output" >&2
    return 1
  }
  export compiled_json
}

@test "plugin-host launches and produces compiled --plugin-dir" {
  assert_regex "$output" 'COMPILED_PLUGIN_DIR=.*/clown-plugin-compile-'
}

@test "compiled plugin.json keeps original mcp-server name" {
  run jq -e '.mcpServers["mock-mcp"]' <<<"$compiled_json"
  assert_success
}

@test "compiled mcpServer entry is http-typed" {
  run jq -r '.mcpServers["mock-mcp"].type' <<<"$compiled_json"
  assert_success
  assert_output "http"
}

@test "compiled mcpServer url matches loopback /mcp pattern" {
  run jq -r '.mcpServers["mock-mcp"].url' <<<"$compiled_json"
  assert_success
  assert_output --regexp '^http://127\.0\.0\.1:[0-9]+/mcp$'
}

@test "compiled mcpServer entry has no command field (replaced by url)" {
  run jq -e '.mcpServers["mock-mcp"].command' <<<"$compiled_json"
  assert_failure
}

@test "compiled plugin.json preserves name and agents fields" {
  run jq -e '.name == "synthetic-test"' <<<"$compiled_json"
  assert_success

  run jq -e '.agents | length > 0' <<<"$compiled_json"
  assert_success
}
