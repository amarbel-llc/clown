# bats file_tags=net_cap
#
# Integration test: launch clown-plugin-host with the synthetic plugin's
# clown.json and verify the mock HTTP MCP server starts, completes the
# handshake, passes health checks, compiles plugin manifests with
# URL-based MCP entries, and preserves original server names.
#
# Tagged net_cap because the synthetic plugin's MCP server binds
# 127.0.0.1 — opt-in via `nix build .#bats-net_cap`.

load 'lib/common.bash'

setup() {
  # clown-plugin-host's OpenLog wants to mkdir its log dir.
  # The nix sandbox sets HOME=/homeless-shelter (read-only), so
  # the default XDG path fails. Point XDG_LOG_HOME at the bats
  # per-test scratch dir, which is writable and ephemeral.
  export XDG_LOG_HOME="$BATS_TEST_TMPDIR"

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
  [[ "$output" =~ COMPILED_PLUGIN_DIR=.*/clown-plugin-compile- ]]
}

@test "compiled plugin.json keeps original mcp-server name" {
  echo "$compiled_json" | jq -e '.mcpServers["mock-mcp"]' >/dev/null
}

@test "compiled mcpServer entry is http-typed" {
  entry_type="$(echo "$compiled_json" | jq -r '.mcpServers["mock-mcp"].type')"
  [[ "$entry_type" == "http" ]]
}

@test "compiled mcpServer url matches loopback /mcp pattern" {
  entry_url="$(echo "$compiled_json" | jq -r '.mcpServers["mock-mcp"].url')"
  [[ "$entry_url" =~ ^http://127\.0\.0\.1:[0-9]+/mcp$ ]]
}

@test "compiled mcpServer entry has no command field (replaced by url)" {
  ! echo "$compiled_json" | jq -e '.mcpServers["mock-mcp"].command' >/dev/null
}

@test "compiled plugin.json preserves name and agents fields" {
  echo "$compiled_json" | jq -e '.name == "synthetic-test"' >/dev/null
  echo "$compiled_json" | jq -e '.agents | length > 0' >/dev/null
}
