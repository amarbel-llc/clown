# bats file_tags=provider_mcp

# Regression coverage for FDR 0016 phase 0 + phase 1 (clown#202, clown#203):
# clown delivers its plugin-host MCP servers to the config-file providers
# through their own config's `mcp` block, and a repo-local provider config
# cannot override a clown-owned entry.
#
# Unlike opencode.bats — which hand-writes an opencode.json because clown's
# generated config lived in a self-cleaning temp dir with no interception
# point — these drive the REAL `clown --provider opencode` end to end and
# assert against opencode's own view of the result. `opencode mcp list` is the
# observable surface: it reports the servers opencode parsed out of clown's
# config, dialed, and completed an MCP handshake with. That covers config
# synthesis, the <plugin>__<server> key derivation, opencode's schema
# validation of type:"remote", plugin-host startup, and the handshake — with
# no model provider involved, which is why this lane does NOT need the dumbo
# fixture (RFC 0017).
#
# The synthetic plugin declares plugin name "synthetic-test" and httpServer
# "mock-mcp" (tests/synthetic-plugin/), so the key clown must emit is
# "synthetic-test__mock-mcp".

setup() {
  load 'lib/common.bash'
  setup_test_home
  require_bin CLOWN_BIN clown
  require_bin OPENCODE_BIN opencode

  clown_config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/clown"
  mkdir -p "$clown_config_dir"
  # No --profile is passed, so runOpencode falls back to reading this file.
  # The URL is never dialed: `opencode mcp list` is config introspection plus
  # MCP connections, not a model call.
  cat >"$clown_config_dir/opencode.toml" <<'EOF'
url = "http://127.0.0.1:1/v1"
token = "local"
EOF

  # A project directory is the unit of hermeticity: clown resolves the
  # clownfile from cwd, and opencode resolves its project config from cwd.
  project="$BATS_TEST_TMPDIR/project"
  mkdir -p "$project"
  export clown_config_dir project
}

# run_clown_opencode_mcp_list runs `clown --provider opencode -- mcp list`
# from the project dir with the synthetic plugin wired in.
#
# The 120s cap is per-invocation and deliberately generous: the run starts a
# real plugin-host MCP server and a real opencode, both of which are slower
# under the nix builder than on a warm host.
run_clown_opencode_mcp_list() {
  cd "$project" || return 1
  run timeout 120 "$CLOWN_BIN" \
    --provider opencode \
    --plugin-dir "$SYNTHETIC_PLUGIN_DIR" \
    -- mcp list
}

@test "clown delivers its MCP servers to opencode under <plugin>__<server>" {
  run_clown_opencode_mcp_list
  assert_success
  # The key proves the sanitized flat-map naming (internal/pluginhost
  # ServerEntries), not merely that some server arrived.
  assert_output --partial "synthetic-test__mock-mcp"
  # "connected" is the load-bearing word: opencode accepted the entry's
  # schema, dialed the loopback URL clown's plugin host bound, and completed
  # the MCP handshake. A parse-only success would not say connected.
  assert_output --partial "connected"
}

@test "a repo-local opencode.json cannot inject an MCP server (hermetic default)" {
  cat >"$project/opencode.json" <<'EOF'
{"mcp":{"repo-local-entry":{"type":"remote","url":"http://127.0.0.1:19003/mcp","enabled":true}}}
EOF

  run_clown_opencode_mcp_list
  assert_success
  refute_output --partial "repo-local-entry"
  # Clown's own server must still be there — proving the suppression did not
  # simply blank the whole config.
  assert_output --partial "synthetic-test__mock-mcp"
}

@test "hermetic-config = false restores the repo-local override" {
  # The control arm. Without it, the previous test cannot distinguish
  # "suppression works" from "opencode ignored the project file for some
  # unrelated reason" — which is exactly the ambiguity that made this
  # behavior worth pinning.
  cat >"$project/opencode.json" <<'EOF'
{"mcp":{"repo-local-entry":{"type":"remote","url":"http://127.0.0.1:19003/mcp","enabled":true}}}
EOF
  cat >"$project/clownfile" <<'EOF'
[providers]
hermetic-config = false
EOF

  run_clown_opencode_mcp_list
  assert_success
  assert_output --partial "repo-local-entry"
}
