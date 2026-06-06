# Conformance suite for clown's job-wakeup channel (RFC-0009).
#
# Exercises the reference producer (`clown job
# start|progress|done|message|read`) and monitor (`clown job-watch`)
# through the real CLI, asserting the on-disk JSONL journal, the
# terminal-once invariant, the wake policy (terminal events and
# `message` wake), the §9 notification-line format (incl. the
# `from`-rendering), the replay / at-least-once guarantee, the
# broadcast-channel condvar semantics, and the
# CLOWN_DISABLE_JOB_WAKEUP kill switch.
#
# No network: this suite touches only the per-channel journal under
# $XDG_STATE_HOME and a unixgram socket under $XDG_RUNTIME_DIR. It runs
# in the standard nix sandbox and every other Linux sandbox we use.
#
# Two gotchas this file handles deliberately:
#
#  1. AF_UNIX sun_path ~108-byte limit. job-watch binds a unixgram
#     socket under $XDG_RUNTIME_DIR. bats-island's setup_test_home roots
#     XDG_RUNTIME_DIR at a deep $BATS_TEST_TMPDIR path that overflows the
#     limit and fails `bind: invalid argument`. So setup() repoints
#     XDG_RUNTIME_DIR at a short /tmp dir (the socket dir must be short;
#     the journal under XDG_STATE_HOME can stay in the deep bats dir) and
#     teardown() removes it.
#
#  2. job-watch blocks (until SIGINT/SIGTERM) and deliberately ignores
#     stdin — Claude Code spawns monitors with an immediately-EOF stdin,
#     so a stdin-EOF shutdown (an earlier revision) killed the monitor at
#     session start. Every invocation here uses `job-watch --once`, which
#     replays unacked waking events deterministically and exits 0 without
#     binding the socket or blocking.

setup() {
  load 'lib/common.bash'

  # bats-island isolation: fresh $HOME + XDG dirs (incl. XDG_STATE_HOME
  # and XDG_RUNTIME_DIR) all under $BATS_TEST_TMPDIR.
  setup_test_home

  require_bin CLOWN_BIN clown

  # Stable channel key so every emit and the monitor resolve the same
  # channel (RFC-0009 §2 resolution order: CLOWN_SESSION_ID wins).
  export CLOWN_SESSION_ID="test/chan"

  # Gotcha 1: short socket dir. setup_test_home's XDG_RUNTIME_DIR is too
  # deep for AF_UNIX sun_path; the journal stays under XDG_STATE_HOME.
  CJW_RUNTIME_DIR="$(mktemp -d /tmp/cjw.XXXXXX)"
  export XDG_RUNTIME_DIR="$CJW_RUNTIME_DIR"
}

teardown() {
  if [[ -n "${CJW_RUNTIME_DIR:-}" ]]; then
    rm -rf "$CJW_RUNTIME_DIR"
  fi
}

# §8: start prints a non-empty job id and exits 0.
@test "job start prints a non-empty job id" {
  run "$CLOWN_BIN" job start --source moxy --label build
  assert_success
  [[ -n "$output" ]]
  # single line, no embedded newline
  assert_equal "$(wc -l <<<"$output")" "1"
}

# §4: start/progress/done write conformant JSONL with seq 0,1,2. Driven
# through `clown job read --job <id> --json` so we assert the records the
# CLI itself reads back.
@test "start/progress/done write JSONL records with seq 0,1,2" {
  id="$("$CLOWN_BIN" job start --source moxy --label build)"
  "$CLOWN_BIN" job progress "$id" --message "halfway"
  "$CLOWN_BIN" job done "$id" --state succeeded --message "nix build ok"

  run "$CLOWN_BIN" job read --job "$id" --json
  assert_success
  # Each `run` clobbers $output, so snapshot the read result before the
  # jq calls below — otherwise the second jq would parse the first jq's
  # output instead of the journal records.
  records="$output"

  # Exactly three records.
  assert_equal "$(wc -l <<<"$records")" "3"

  # seq is 0,1,2 in order.
  run jq -s -r 'map(.seq) | @csv' <<<"$records"
  assert_output "0,1,2"

  # types are started, progress, succeeded in order.
  run jq -s -r 'map(.type) | join(",")' <<<"$records"
  assert_output "started,progress,succeeded"

  # required schema fields on the terminal record.
  run jq -s -r '.[2] | "\(.v)|\(.session)|\(.source)|\(.result_ref // "")"' <<<"$records"
  assert_output "1|test/chan|moxy|"
}

# §4: result_ref rides through to the terminal record.
@test "done --result-ref is recorded on the terminal event" {
  id="$("$CLOWN_BIN" job start --source moxy)"
  "$CLOWN_BIN" job done "$id" --state succeeded --message ok --result-ref "moxy job-read --job $id"

  run "$CLOWN_BIN" job read --job "$id" --json
  assert_success
  run jq -s -r '.[-1].result_ref' <<<"$output"
  assert_output "moxy job-read --job $id"
}

# §5: a second `done` on an already-terminal job exits non-zero.
@test "second done on a terminal job fails" {
  id="$("$CLOWN_BIN" job start --source s)"
  run "$CLOWN_BIN" job done "$id" --state succeeded
  assert_success
  run "$CLOWN_BIN" job done "$id" --state failed
  assert_failure
}

# §8/§5: an invalid terminal state exits non-zero.
@test "done --state wat (invalid state) fails" {
  id="$("$CLOWN_BIN" job start --source s)"
  run "$CLOWN_BIN" job done "$id" --state wat
  assert_failure
}

# §5 + §9: the monitor emits a line for the terminal event but NOT for
# progress, and the line matches the §9 format incl. ` · <result_ref>`.
@test "monitor wakes on terminal event only and formats the §9 line" {
  id="$("$CLOWN_BIN" job start --source moxy --label build)"
  "$CLOWN_BIN" job progress "$id" --message "halfway"
  "$CLOWN_BIN" job done "$id" --state succeeded --message "nix build ok" --result-ref "ref-123"

  # Gotcha 2: --once replays the unacked waking events deterministically,
  # then exits 0 without binding or blocking.
  run "$CLOWN_BIN" job-watch --once
  assert_success

  # Terminal event surfaces with the full §9 line: source job type,
  # ": <message>", and " · <result_ref>".
  assert_line "[clown-job] moxy ${id} succeeded: nix build ok · ref-123"

  # progress never wakes — its message must not appear.
  refute_line --partial "halfway"
  refute_line --partial "progress"
}

# §9: when message is absent the trailing ": " is omitted; when
# result_ref is absent the " · " is omitted.
@test "notification line omits ': ' and ' · ' when message/result_ref absent" {
  id="$("$CLOWN_BIN" job start --source moxy --label bare)"
  "$CLOWN_BIN" job done "$id" --state failed

  run "$CLOWN_BIN" job-watch --once
  assert_success
  assert_line "[clown-job] moxy ${id} failed"
}

# §9: replay / at-least-once. A monitor started AFTER `done` replays the
# unacked terminal event. (Same mechanism as the wake test, asserted
# explicitly against the requirements row.)
@test "monitor started after done replays the unacked terminal event" {
  id="$("$CLOWN_BIN" job start --source spinclass --label merge)"
  "$CLOWN_BIN" job done "$id" --state succeeded --message "merged"

  run "$CLOWN_BIN" job-watch --once
  assert_success
  assert_line "[clown-job] spinclass ${id} succeeded: merged"
}

# §9: at-least-once is bounded by the ack — a second monitor over the
# same channel (ack now persisted) replays nothing.
@test "a second monitor replays nothing once the event is acked" {
  id="$("$CLOWN_BIN" job start --source spinclass --label merge)"
  "$CLOWN_BIN" job done "$id" --state succeeded --message "merged"

  run "$CLOWN_BIN" job-watch --once
  assert_success
  assert_line "[clown-job] spinclass ${id} succeeded: merged"

  # Second pass: ack from the first run gates the replay; no lines.
  # refute_output (not refute_line): on empty output the $lines array is
  # unset under `set -u`, so refute_line would error "lines: parameter
  # not set"; refute_output operates on the $output scalar.
  run "$CLOWN_BIN" job-watch --once
  assert_success
  refute_output --partial "[clown-job]"
}

# §4/§5/§9: a directed `message` is a standalone single-record waking job;
# the monitor surfaces it with the §9 from-rendering, exactly once.
@test "directed message wakes with the from-line" {
  export CLOWN_SESSION_ID="test/msg-directed"
  id="$("$CLOWN_BIN" job message --target test/msg-directed \
    --from test/msg-sender --source spinclass --message "ping")"

  run "$CLOWN_BIN" job-watch --once
  assert_success
  assert_line "[clown-job] spinclass ${id} message from test/msg-sender: ping"

  # acked thereafter: a second monitor pass emits nothing.
  run "$CLOWN_BIN" job-watch --once
  assert_success
  refute_output --partial "[clown-job]"
}

# §8: job message usage errors — --target and --message are required.
@test "job message without --target or --message is a usage error (exit 2)" {
  run "$CLOWN_BIN" job message --message hi
  assert_failure 2
  run "$CLOWN_BIN" job message --target test/chan
  assert_failure 2
}

# §9 condvar pin: a reader's FIRST attach to the broadcast channel
# initializes its per-reader ack at current end (a pre-existing broadcast
# is NOT replayed); a broadcast sent AFTER first attach — while no monitor
# is running — IS delivered on the next attach, exactly once.
@test "broadcast: first attach at end, post-attach broadcast delivered once" {
  export CLOWN_SESSION_ID="test/bcast-reader"

  # Broadcast emitted before this reader ever attached.
  "$CLOWN_BIN" job message --target '*' --from test/bcast-sender \
    --source spinclass --message "pre-attach"

  # First attach: init-at-end, nothing replayed.
  run "$CLOWN_BIN" job-watch --once
  assert_success
  refute_output --partial "pre-attach"

  # Broadcast after attach, monitor down.
  id="$("$CLOWN_BIN" job message --target '*' --from test/bcast-sender \
    --source spinclass --message "post-attach")"

  run "$CLOWN_BIN" job-watch --once
  assert_success
  assert_line "[clown-job] spinclass ${id} message from test/bcast-sender: post-attach"

  # Exactly once: the per-reader broadcast ack gates the replay.
  run "$CLOWN_BIN" job-watch --once
  assert_success
  refute_output --partial "post-attach"
}

# §8: CLOWN_DISABLE_JOB_WAKEUP=1 — job-watch exits 0 immediately and the
# emit subcommands are no-ops that still exit 0.
@test "CLOWN_DISABLE_JOB_WAKEUP=1 makes job-watch exit 0 immediately" {
  CLOWN_DISABLE_JOB_WAKEUP=1 run "$CLOWN_BIN" job-watch --once
  assert_success
  refute_output --partial "[clown-job]"
}

@test "CLOWN_DISABLE_JOB_WAKEUP=1 makes emit subcommands no-op (exit 0)" {
  CLOWN_DISABLE_JOB_WAKEUP=1 run "$CLOWN_BIN" job start --source moxy --label build
  assert_success
  CLOWN_DISABLE_JOB_WAKEUP=1 run "$CLOWN_BIN" job progress some-job --message hi
  assert_success
  CLOWN_DISABLE_JOB_WAKEUP=1 run "$CLOWN_BIN" job done some-job --state succeeded
  assert_success
  CLOWN_DISABLE_JOB_WAKEUP=1 run "$CLOWN_BIN" job message --target '*' --message hi
  assert_success

  # No journal was written for the channel (disabled start is a no-op,
  # so no records exist for any job).
  run "$CLOWN_BIN" job read --json
  assert_success
  refute_output --partial "[clown-job]"
  assert_equal "$output" ""
}
