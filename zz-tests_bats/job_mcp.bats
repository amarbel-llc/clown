# Conformance suite for clown's job-platform MCP server (RFC-0011), exercised
# through the real `clown job-mcp` binary over the MCP stdio transport
# (line-delimited JSON-RPC 2.0). The server is stateless — all job state lives
# in jobwake's files under $XDG_STATE_HOME — so each test pipes one request per
# invocation and threads the job id between invocations, the same shape the
# stdio bridge uses at runtime.
#
# This covers the tool catalog and the tools/call dispatch end-to-end. The full
# injection path (clown synthesizes the clown-builtin-jobs plugin → bridge →
# pluginhost → claude sees the tools) rides the existing plugin-host machinery
# and is not re-driven here.

setup() {
  load 'lib/common.bash'
  setup_test_home
  require_bin CLOWN_BIN clown
  export CLOWN_SESSION_ID="test/chan"
  # Short socket dir for job_done's best-effort nudge (AF_UNIX sun_path limit);
  # the journal/spool live under XDG_STATE_HOME.
  CJM_RUNTIME_DIR="$(mktemp -d /tmp/cjm.XXXXXX)"
  export XDG_RUNTIME_DIR="$CJM_RUNTIME_DIR"
}

teardown() {
  if [[ -n "${CJM_RUNTIME_DIR:-}" ]]; then
    rm -rf "$CJM_RUNTIME_DIR"
  fi
}

# §1: initialize reports the clown-jobs server identity.
@test "job-mcp initialize reports the clown-jobs server" {
  req='{"jsonrpc":"2.0","id":1,"method":"initialize"}'
  run bash -c "printf '%s\n' '$req' | '$CLOWN_BIN' job-mcp"
  assert_success
  assert_output --partial '"name":"clown-jobs"'
}

# §3: tools/list enumerates the seven job_* tools plus the two chat tools
# (RFC-0013 §3 chat surface rides the same clown-builtin-jobs server).
@test "job-mcp tools/list enumerates the job and chat tools" {
  req='{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
  run bash -c "printf '%s\n' '$req' | '$CLOWN_BIN' job-mcp"
  assert_success
  for tool in job_start job_progress job_done job_message job_read job_status job_spool_path chat_send chat_read chat_list; do
    assert_output --partial "\"$tool\""
  done
  count="$(printf '%s' "$output" | jq -r '.result.tools | length')"
  assert_equal "$count" "10"
}

# RFC-0002 §5.4: prompts/list advertises the system-prompt-append prompt that
# clown-stdio-bridge fetches for dynamic system-prompt contribution.
@test "job-mcp prompts/list advertises system-prompt-append" {
  req='{"jsonrpc":"2.0","id":1,"method":"prompts/list"}'
  run bash -c "printf '%s\n' '$req' | '$CLOWN_BIN' job-mcp"
  assert_success
  assert_output --partial '"system-prompt-append"'
}

# RFC-0002 §5: prompts/get returns a live fragment carrying the injected
# session key (CLOWN_SESSION_ID=test/chan from setup) and the server's own tool
# catalog — runtime state the build-time static path cannot express.
@test "job-mcp prompts/get returns the live system-prompt fragment" {
  req='{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"system-prompt-append"}}'
  run bash -c "printf '%s\n' '$req' | '$CLOWN_BIN' job-mcp"
  assert_success
  assert_output --partial 'clown job platform'
  assert_output --partial 'test/chan'
  assert_output --partial 'job_start'
}

# An unknown prompt name is a JSON-RPC error (not a crash, not an empty result).
@test "job-mcp prompts/get on an unknown name returns a JSON-RPC error" {
  req='{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"nope"}}'
  run bash -c "printf '%s\n' '$req' | '$CLOWN_BIN' job-mcp"
  assert_success
  code="$(printf '%s' "$output" | jq -r '.error.code')"
  assert_equal "$code" "-32602"
}

# §3/§4: tools/call job_start then job_status round-trips and the status is
# journal-derived (running before any terminal record).
@test "job-mcp job_start then job_status round-trips" {
  start='{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"job_start","arguments":{"source":"moxy","label":"build"}}}'
  run bash -c "printf '%s\n' '$start' | '$CLOWN_BIN' job-mcp"
  assert_success
  id="$(printf '%s' "$output" | jq -r '.result.content[0].text')"
  [[ -n "$id" ]]

  status="$(printf '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"job_status","arguments":{"job_id":"%s"}}}' "$id")"
  run bash -c "printf '%s\n' '$status' | '$CLOWN_BIN' job-mcp"
  assert_success
  state="$(printf '%s' "$output" | jq -r '.result.content[0].text | fromjson | .state')"
  assert_equal "$state" "running"
}

# §3: an invalid (traversal) job id is surfaced as a tool error, not a crash.
@test "job-mcp job_status on a traversal id is a tool error" {
  req='{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"job_status","arguments":{"job_id":"../x"}}}'
  run bash -c "printf '%s\n' '$req' | '$CLOWN_BIN' job-mcp"
  assert_success
  iserr="$(printf '%s' "$output" | jq -r '.result.isError')"
  assert_equal "$iserr" "true"
}

# unknown method yields a JSON-RPC error object.
@test "job-mcp unknown method returns a JSON-RPC error" {
  req='{"jsonrpc":"2.0","id":9,"method":"frobnicate"}'
  run bash -c "printf '%s\n' '$req' | '$CLOWN_BIN' job-mcp"
  assert_success
  code="$(printf '%s' "$output" | jq -r '.error.code')"
  assert_equal "$code" "-32601"
}
