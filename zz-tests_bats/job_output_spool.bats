# Conformance suite for clown's job output spool + status probe (RFC-0010),
# the observability layer over the RFC-0009 job-wakeup channel.
#
# Exercises the two new reference subcommands through the real CLI:
#
#   clown job spool-path <id>   resolve/print the producer-written .out path
#   clown job status <id>       derive state/elapsed/last_activity/tail
#
# Asserts: the one-line absolute spool path; the disabled-channel signature
# (empty + exit 0) and traversal/grammar rejection (exit 2) for spool-path;
# status' running-vs-terminal derivation, spool tail + last_activity, the
# unknown-job exit 1, and the human one-line header + tail separator.
#
# GC coupling (§4 — reap spool with journal, age-gated orphan sweep) is NOT
# exercised here: the sweep runs only in the blocking `clown job-watch`
# monitor (not in `--once`), so it is covered deterministically by the Go
# unit tests in internal/jobwake/status_test.go instead.
#
# No network: touches only the per-channel journal/spool under
# $XDG_STATE_HOME. Mirrors job_wakeup.bats's isolation (short
# $XDG_RUNTIME_DIR for the AF_UNIX sun_path limit, stable CLOWN_SESSION_ID).

setup() {
  load 'lib/common.bash'

  setup_test_home

  require_bin CLOWN_BIN clown

  # Stable channel key so every emit and probe resolve the same channel.
  export CLOWN_SESSION_ID="test/chan"

  # Short socket dir: setup_test_home's XDG_RUNTIME_DIR is too deep for the
  # AF_UNIX sun_path limit. The journal/spool stay under XDG_STATE_HOME.
  CJW_RUNTIME_DIR="$(mktemp -d /tmp/cjs.XXXXXX)"
  export XDG_RUNTIME_DIR="$CJW_RUNTIME_DIR"
}

teardown() {
  if [[ -n "${CJW_RUNTIME_DIR:-}" ]]; then
    rm -rf "$CJW_RUNTIME_DIR"
  fi
}

# §2: spool-path prints exactly one absolute path ending in <id>.out, and does
# NOT create the file (that is the producer's append).
@test "spool-path prints a single absolute path and does not create the file" {
  id="$("$CLOWN_BIN" job start --source moxy --label build)"
  run "$CLOWN_BIN" job spool-path "$id"
  assert_success
  assert_equal "$(wc -l <<<"$output")" "1"
  [[ "$output" == /* ]]
  [[ "$output" == *"/${id}.out" ]]
  [[ ! -e "$output" ]]
}

# §2: disabled-channel signature — empty stdout, exit 0 (mirrors `job start`).
@test "spool-path is empty and exits 0 when CLOWN_DISABLE_JOB_WAKEUP=1" {
  CLOWN_DISABLE_JOB_WAKEUP=1 run "$CLOWN_BIN" job spool-path some-job
  assert_success
  assert_output ""
}

# §1/§2: traversal and grammar-violating ids are usage errors (exit 2). The
# real vector is "/"; "." and ".." are rejected explicitly (clown#123).
@test "spool-path rejects '.', '..', and grammar-violating ids with exit 2" {
  run "$CLOWN_BIN" job spool-path ..
  assert_failure 2
  run "$CLOWN_BIN" job spool-path ../evil
  assert_failure 2
  run "$CLOWN_BIN" job spool-path "a b"
  assert_failure 2
}

# §3: status derives state from the journal — running before, the terminal type
# after, with `ended` and `source` populated.
@test "status reports running, then the terminal state with ended/source" {
  id="$("$CLOWN_BIN" job start --source moxy --label build)"

  run "$CLOWN_BIN" job status "$id" --json
  assert_success
  run jq -r '.state' <<<"$output"
  assert_output "running"

  "$CLOWN_BIN" job done "$id" --state succeeded --message ok
  run "$CLOWN_BIN" job status "$id" --json
  assert_success
  records="$output"
  run jq -r '.state' <<<"$records"
  assert_output "succeeded"
  run jq -r 'has("ended")' <<<"$records"
  assert_output "true"
  run jq -r '.source' <<<"$records"
  assert_output "moxy"
}

# §3: with a spool present, status surfaces spool_bytes, a bounded tail, and a
# last_activity field.
@test "status surfaces the spool tail (bounded to --tail) and last_activity" {
  id="$("$CLOWN_BIN" job start --source moxy --label build)"
  sp="$("$CLOWN_BIN" job spool-path "$id")"
  printf 'l1\nl2\nl3\nl4\n' > "$sp"

  run "$CLOWN_BIN" job status "$id" --tail 2 --json
  assert_success
  records="$output"
  run jq -r '.tail | join(",")' <<<"$records"
  assert_output "l3,l4"
  run jq -r '.spool_bytes' <<<"$records"
  assert_output "12"
  run jq -r 'has("last_activity")' <<<"$records"
  assert_output "true"
}

# §3: no journal for the id => exit 1 (a status of nothing is not a valid
# answer, unlike `job read --job` whose empty stream is).
@test "status on an unknown job exits 1" {
  run "$CLOWN_BIN" job status no-such-job-12345678
  assert_failure 1
}

# §1/§3: status applies the same id validation as spool-path (exit 2).
@test "status rejects '.', '..', and grammar-violating ids with exit 2" {
  run "$CLOWN_BIN" job status ..
  assert_failure 2
  run "$CLOWN_BIN" job status ../evil
  assert_failure 2
}

# §3: the human (non-JSON) form is a one-line header followed by the tail under
# a '---' separator.
@test "status human output has the one-line header and tail separator" {
  id="$("$CLOWN_BIN" job start --source spinclass --label merge)"
  sp="$("$CLOWN_BIN" job spool-path "$id")"
  printf 'building\n' > "$sp"

  run "$CLOWN_BIN" job status "$id"
  assert_success
  assert_line --partial "job ${id} (spinclass): running"
  assert_line "---"
  assert_line "building"
}
