# Conformance suite for clown's job-platform MCP server (RFC-0011), exercised
# through the real `ringmaster mcp` and `troupe mcp` binaries over the MCP stdio
# transport (line-delimited JSON-RPC 2.0). The server is stateless — all job
# state lives in jobwake's files under $XDG_STATE_HOME — so each test pipes one
# request per invocation and threads the job id between invocations, the same
# shape the stdio bridge uses at runtime.
#
# The surface split (RFC-0015 §6): `ringmaster mcp` serves the job-control tools
# (and carries the system-prompt prompt); `troupe mcp` serves the messaging
# tools. Neither binary serves the whole catalog — that mode ("" surface) is
# covered by the Go tests in internal/jobmcp.
#
# This covers the tool catalog and the tools/call dispatch end-to-end. The full
# injection path (clown synthesizes the clown-builtin-jobs plugin → bridge →
# pluginhost → claude sees the tools) rides the existing plugin-host machinery
# and is not re-driven here.

setup() {
  load 'lib/common.bash'
  setup_test_home
  require_bin RINGMASTER_BIN ringmaster
  require_bin TROUPE_BIN troupe
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

# initialize reports the clown-ringmaster server identity (RFC-0015 §6).
@test "ringmaster mcp initialize reports the clown-ringmaster server" {
  req='{"jsonrpc":"2.0","id":1,"method":"initialize"}'
  run bash -c "printf '%s\n' '$req' | '$RINGMASTER_BIN' mcp"
  assert_success
  assert_output --partial '"name":"clown-ringmaster"'
}

# `ringmaster mcp` exposes only the seven job-control tools (clown#144).
@test "ringmaster mcp exposes only the job-control tools" {
  req='{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
  run bash -c "printf '%s\n' '$req' | '$RINGMASTER_BIN' mcp"
  assert_success
  for tool in job_start job_progress job_done job_read job_status job_spool_path job_wait; do
    assert_output --partial "\"$tool\""
  done
  for tool in chat_send chat_read chat_list job_message; do
    refute_output --partial "\"$tool\""
  done
  count="$(printf '%s' "$output" | jq -r '.result.tools | length')"
  assert_equal "$count" "7"
}

# `troupe mcp` exposes only the four messaging tools and reports the clown-troupe
# server identity (clown#144, RFC-0015 §6).
@test "troupe mcp exposes only the messaging tools" {
  req='{"jsonrpc":"2.0","id":1,"method":"initialize"}'
  run bash -c "printf '%s\n' '$req' | '$TROUPE_BIN' mcp"
  assert_success
  assert_output --partial '"name":"clown-troupe"'

  req='{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
  run bash -c "printf '%s\n' '$req' | '$TROUPE_BIN' mcp"
  assert_success
  for tool in chat_send chat_read chat_list job_message; do
    assert_output --partial "\"$tool\""
  done
  for tool in job_start job_done job_status job_wait; do
    refute_output --partial "\"$tool\""
  done
  count="$(printf '%s' "$output" | jq -r '.result.tools | length')"
  assert_equal "$count" "4"
}

# RFC-0002 §5.4: prompts/list advertises the system-prompt-append prompt that
# clown-stdio-bridge fetches for dynamic system-prompt contribution. The
# ringmaster surface carries it (the fragment is appended once).
@test "ringmaster mcp prompts/list advertises system-prompt-append" {
  req='{"jsonrpc":"2.0","id":1,"method":"prompts/list"}'
  run bash -c "printf '%s\n' '$req' | '$RINGMASTER_BIN' mcp"
  assert_success
  assert_output --partial '"system-prompt-append"'
}

# RFC-0002 §5: prompts/get returns a live fragment carrying the injected
# session key (CLOWN_SESSION_ID=test/chan from setup) and the server's own tool
# catalog — runtime state the build-time static path cannot express.
@test "ringmaster mcp prompts/get returns the live system-prompt fragment" {
  req='{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"system-prompt-append"}}'
  run bash -c "printf '%s\n' '$req' | '$RINGMASTER_BIN' mcp"
  assert_success
  assert_output --partial 'clown job platform'
  assert_output --partial 'test/chan'
  assert_output --partial 'job_start'
}

# An unknown prompt name is a JSON-RPC error (not a crash, not an empty result).
@test "ringmaster mcp prompts/get on an unknown name returns a JSON-RPC error" {
  req='{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"nope"}}'
  run bash -c "printf '%s\n' '$req' | '$RINGMASTER_BIN' mcp"
  assert_success
  code="$(printf '%s' "$output" | jq -r '.error.code')"
  assert_equal "$code" "-32602"
}

# §3/§4: tools/call job_start then job_status round-trips and the status is
# journal-derived (running before any terminal record).
@test "ringmaster mcp job_start then job_status round-trips" {
  start='{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"job_start","arguments":{"source":"moxy","label":"build"}}}'
  run bash -c "printf '%s\n' '$start' | '$RINGMASTER_BIN' mcp"
  assert_success
  id="$(printf '%s' "$output" | jq -r '.result.content[0].text')"
  [[ -n "$id" ]]

  status="$(printf '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"job_status","arguments":{"job_id":"%s"}}}' "$id")"
  run bash -c "printf '%s\n' '$status' | '$RINGMASTER_BIN' mcp"
  assert_success
  state="$(printf '%s' "$output" | jq -r '.result.content[0].text | fromjson | .state')"
  assert_equal "$state" "running"
}

# clown#154: job_wait on an already-terminal job returns immediately with the
# terminal status (state = succeeded), the same payload job_status reports. The
# job is marked done out of band (ringmaster done) before the wait so the call
# does not block.
@test "ringmaster mcp job_wait returns the terminal status of a finished job" {
  start='{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"job_start","arguments":{"source":"moxy","label":"build"}}}'
  run bash -c "printf '%s\n' '$start' | '$RINGMASTER_BIN' mcp"
  assert_success
  id="$(printf '%s' "$output" | jq -r '.result.content[0].text')"
  [[ -n "$id" ]]

  run "$RINGMASTER_BIN" done "$id" --state succeeded --message "ok"
  assert_success

  wait="$(printf '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"job_wait","arguments":{"job_id":"%s"}}}' "$id")"
  run bash -c "printf '%s\n' '$wait' | '$RINGMASTER_BIN' mcp"
  assert_success
  state="$(printf '%s' "$output" | jq -r '.result.content[0].text | fromjson | .state')"
  assert_equal "$state" "succeeded"
}

# clown#154: job_wait on an unknown job id is a tool error, not an indefinite
# block.
@test "ringmaster mcp job_wait on an unknown job is a tool error" {
  req='{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"job_wait","arguments":{"job_id":"nope-1a2b"}}}'
  run bash -c "printf '%s\n' '$req' | '$RINGMASTER_BIN' mcp"
  assert_success
  iserr="$(printf '%s' "$output" | jq -r '.result.isError')"
  assert_equal "$iserr" "true"
}

# §3: an invalid (traversal) job id is surfaced as a tool error, not a crash.
@test "ringmaster mcp job_status on a traversal id is a tool error" {
  req='{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"job_status","arguments":{"job_id":"../x"}}}'
  run bash -c "printf '%s\n' '$req' | '$RINGMASTER_BIN' mcp"
  assert_success
  iserr="$(printf '%s' "$output" | jq -r '.result.isError')"
  assert_equal "$iserr" "true"
}

# unknown method yields a JSON-RPC error object.
@test "ringmaster mcp unknown method returns a JSON-RPC error" {
  req='{"jsonrpc":"2.0","id":9,"method":"frobnicate"}'
  run bash -c "printf '%s\n' '$req' | '$RINGMASTER_BIN' mcp"
  assert_success
  code="$(printf '%s' "$output" | jq -r '.error.code')"
  assert_equal "$code" "-32601"
}
