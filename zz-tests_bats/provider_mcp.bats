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
  require_bin CRUSH_BIN crush

  clown_config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/clown"
  mkdir -p "$clown_config_dir"
  cat >"$clown_config_dir/crush.toml" <<'EOF'
url = "http://127.0.0.1:1/v1"
token = "local"
EOF
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

# crush has no `mcp list` equivalent, so its coverage is split in two: clown
# must write the mcp block into the workspace slot, AND crush must resolve
# that same directory. Either alone would be weak — a correct file in a
# directory crush ignores proves nothing, and vice versa.

# crush_workspace_config echoes the path of the workspace crush.json clown
# wrote, or fails if it is missing. The directory name is a hash of the
# project path (crushDataDir), so it is globbed rather than recomputed.
crush_workspace_config() {
  local state="${XDG_STATE_HOME:-$HOME/.local/state}"
  local found
  found="$(echo "$state"/clown/crush/*/crush.json)"
  [[ -f $found ]] || return 1
  echo "$found"
}

@test "clown writes its MCP servers into crush's workspace config slot" {
  cd "$project"
  run timeout 120 "$CLOWN_BIN" \
    --provider crush \
    --plugin-dir "$SYNTHETIC_PLUGIN_DIR" \
    -- --version
  assert_success

  local cfg
  cfg="$(crush_workspace_config)"

  # type "http" is crush's discriminator, deliberately NOT opencode's
  # "remote" — the two providers disagree and each writer translates.
  run jq -r '.mcp["synthetic-test__mock-mcp"].type' "$cfg"
  assert_success
  assert_output "http"

  run jq -r '.mcp["synthetic-test__mock-mcp"].url' "$cfg"
  assert_success
  assert_output --regexp '^http://127\.0\.0\.1:[0-9]+/mcp$'

  # The synthetic plugin declares no timeout, so none must be emitted —
  # crush then applies its own default rather than being pinned to 0.
  run jq -r '.mcp["synthetic-test__mock-mcp"] | has("timeout")' "$cfg"
  assert_success
  assert_output "false"
}

# NOT covered in this lane: that crush READS clown's workspace config slot.
#
# That property was verified manually against crush 0.86.0 during clown#202 —
# a scalar set in <data-dir>/crush.json overrode a repo-local crush.json in
# BOTH directions — and `crushArgs` is unit-tested in Go for emitting
# --data-dir. But neither surface crush exposes can assert it here:
# `crush dirs` prints only CONFIG paths (not the data dir), and
# `crush projects` reports "No projects tracked yet" because registration
# happens when crush runs a real session, which needs a model.
#
# This is a concrete case where the dumbo fixture (RFC 0017) would pay for
# itself: `crush run` against a mock model would register the project and make
# the resolved data dir assertable. Tracked as remaining work on clown#203.
@test "crush loads the config clown generated for it" {
  cd "$project"
  run timeout 120 "$CLOWN_BIN" \
    --provider crush \
    --plugin-dir "$SYNTHETIC_PLUGIN_DIR" \
    -- dirs
  assert_success
  # `crush dirs` lists the config paths crush itself resolved. clown's
  # generated config dir appearing there proves CRUSH_GLOBAL_CONFIG was both
  # set by clown and honored by crush — so the config asserted above is in
  # crush's search path, not merely on disk somewhere. The temp dir's suffix
  # is random, hence the pattern.
  assert_output --regexp 'clown-crush-[0-9]+'
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
