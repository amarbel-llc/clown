# End-to-end lane for the juggler agent-substrate verbs (FDR 0019):
# `spawn --new-run`, `decide`, `spawn --brief` (with and without --wait),
# `handles`, `job-ledger`, `resolve` and `exit-wake`, driven through the BUILT
# juggler binary.
#
# The platform binaries juggler consumes are stand-ins: fake-platform wrapped
# as `ringmaster`, `troupe` and `systemd-run` (a stateful per-target journal,
# a troupe that records MUC posts and wakes, a systemd-run that records its
# argv and succeeds), reached through JUGGLER_{RINGMASTER,TROUPE,
# SYSTEMD_RUN}_BIN, plus a fake OpenRouter Decisions endpoint on loopback
# that `juggler decide` resolves straight from the models file (no daemon).
# fake-platform reuses the Go tests' fakes (internal/jugglerrun/
# jugglerruntest), so this lane and the unit tests agree on the platform's
# shape. systemd never runs the unit: the agent inside it is played by the
# tests, which write the terminal record the way `juggler run` or the post-stop
# hook would.
#
# Binds loopback only; no capability escalation (docs/adrs/0007).

setup_file() {
  load 'lib/common.bash'

  # The tests share one state directory and one fake platform and run in
  # order: a later test reads the run an earlier one created.
  export BATS_NO_PARALLELIZE_WITHIN_FILE=true

  require_bin JUGGLER_BIN juggler
  require_bin FAKE_PLATFORM_BIN fake-platform

  export FAKE_DIR="$BATS_FILE_TMPDIR/fake"
  export FAKE_BIN="$BATS_FILE_TMPDIR/fakebin"
  export DEC_DIR="$BATS_FILE_TMPDIR/decisions"
  mkdir -p "$FAKE_DIR" "$FAKE_BIN" "$DEC_DIR"

  # One tiny wrapper per platform binary name.
  local tool
  for tool in ringmaster troupe systemd-run; do
    printf '#!%s\nJUGGLERRUNTEST_FAKE=%s JUGGLERRUNTEST_DIR=%s exec %s "$@"\n' \
      "$BASH" "$tool" "$FAKE_DIR" "$FAKE_PLATFORM_BIN" >"$FAKE_BIN/$tool"
    chmod +x "$FAKE_BIN/$tool"
  done
  export JUGGLER_RINGMASTER_BIN="$FAKE_BIN/ringmaster"
  export JUGGLER_TROUPE_BIN="$FAKE_BIN/troupe"
  export JUGGLER_SYSTEMD_RUN_BIN="$FAKE_BIN/systemd-run"

  # juggler's lifecycle state and models file live in the scratch dir, never
  # in the real ~/.local/state or ~/.local/share.
  export XDG_STATE_HOME="$BATS_FILE_TMPDIR/state"
  export JUGGLER_MODELS_PATH="$BATS_FILE_TMPDIR/models.toml"
  unset CLOWN_SESSION_ID TROUPE_XMPP_USER TROUPE_XMPP_PASSWORD_FILE TROUPE_XMPP_DOMAIN
  # resolve's teardown requires the minter credential (troupe's privilege-free
  # mint-revoke path); the fake troupe ignores it.
  export TROUPE_MINT_PASSWORD_FILE="$BATS_FILE_TMPDIR/minter.pw" TROUPE_MINT_USER=troupe-minter

  "$FAKE_PLATFORM_BIN" serve-decisions --dir "$DEC_DIR" >"$DEC_DIR/server.log" 2>&1 &
  export DEC_PID=$!
  wait_for_file "$DEC_DIR/port" 5
  if [[ ! -s $DEC_DIR/port ]]; then
    echo "fake decisions server never wrote its port; log:" >&2
    cat "$DEC_DIR/server.log" >&2
    return 1
  fi

  # A decisions-style remote entry pointing at the fake endpoint: resolved
  # from this file directly, so no juggler daemon runs anywhere in this lane.
  cat >"$JUGGLER_MODELS_PATH" <<EOF
[[model]]
name = "jev"
style = "decisions"
url = "http://127.0.0.1:$(<"$DEC_DIR/port")"
token = "tok-test"
model = "typesafe/jev-test"
EOF

  # The brief template the subagent tests spawn from.
  cat >"$BATS_FILE_TMPDIR/issue-filer.toml" <<'EOF'
schema = 1
model = "jev"
system = "You file one issue per actionable item."
tools = ["ring_create_issue"]

[evaluator]
kind = "jq"
program = ".cannot_complete == null"

[limits]
steps = 3
wall_clock = "20s"
EOF
}

teardown_file() {
  if [[ -n ${DEC_PID:-} ]]; then
    kill "$DEC_PID" 2>/dev/null || true
    wait "$DEC_PID" 2>/dev/null || true
  fi
}

setup() {
  load 'lib/common.bash'
}

# --- helpers ---------------------------------------------------------------

# _exec_capture <cmd>...: run cmd, leaving its exit code in $status, stdout in
# $output and stderr in $stderr (stdout carries JSON, so the two are never
# merged the way bats's `run` does).
_exec_capture() {
  status=0
  output=$("$@" 2>"$BATS_TEST_TMPDIR/stderr") || status=$?
  stderr=$(<"$BATS_TEST_TMPDIR/stderr")
}

# run_jug_env VAR=value... -- <juggler args>
run_jug_env() {
  local envs=()
  while [[ $1 != -- ]]; do
    envs+=("$1")
    shift
  done
  shift
  _exec_capture env "${envs[@]}" "$JUGGLER_BIN" "$@"
}

# run_jug <juggler args>: with no principal in the environment.
run_jug() { run_jug_env -- "$@"; }

# run_jug_as <principal> <juggler args>: as that principal (the glue runs as
# the run root, so it passes the root's CLOWN_SESSION_ID).
run_jug_as() {
  local principal=$1
  shift
  run_jug_env "CLOWN_SESSION_ID=$principal" -- "$@"
}

expect_status() {
  if [[ $status -ne $1 ]]; then
    {
      echo "expected exit $1, got $status"
      echo "stdout: $output"
      echo "stderr: $stderr"
    } >&2
    return 1
  fi
}

# field <jq filter>: a field of the last command's stdout JSON (-e: fails on
# false/null).
field() { jq -er "$1" <<<"$output"; }

# expect_json <jq filter>: the filter holds for the last command's stdout.
expect_json() {
  if ! jq -e "$1" <<<"$output" >/dev/null; then
    echo "stdout does not satisfy: $1" >&2
    echo "stdout: $output" >&2
    return 1
  fi
}

# stored_run <name> <jq filter>: a field of a saved `spawn --new-run` object.
stored_run() { jq -er "$2" "$BATS_FILE_TMPDIR/$1.json"; }

# fake_calls <jq filter>: the recorded platform invocations, slurped.
fake_calls() { jq -s "$1" "$FAKE_DIR/calls.jsonl"; }

# journal_types <target> <job>: the job's journal record types, one per line.
journal_types() { jq -r .type "$FAKE_DIR/rm/$1/$2.jsonl"; }

# set_decision <http status> <body>: the Decisions endpoint's next answer.
set_decision() {
  printf '%s' "$1" >"$DEC_DIR/status"
  printf '%s' "$2" >"$DEC_DIR/body"
}

DECIDE_PAYLOAD='{"state":"file an issue on moxy that restart should reconnect","questions":{"route":{"type":"choice","instructions":"Which kind of capture is it?","criteria":{"issue":"File, open or log an issue against a project.","note":"Anything else."}}}}'

# decision_body <confidence>: a usable-shaped answer choosing "issue".
decision_body() {
  printf '{"answers":{"route":{"type":"choice","choice":"issue","probabilities":{"issue":0.9,"note":0.1},"confidence":%s}},"id":"gen-dec-1","model":"typesafe/jev-test","usage":{"cost":0,"input_tokens":5,"output_tokens":1}}' "$1"
}

# launch_child <root principal> <task>: `spawn --brief` without --wait; the
# launch JSON is left in $output.
launch_child() {
  run_jug_as "$1" spawn --brief "$BATS_FILE_TMPDIR/issue-filer.toml" --task - <<<"$2"
  expect_status 0
}

# finish_job_later <root> <job> <state> <message> [result-ref]: after a
# moment, write the job's terminal record the way the agent or the post-stop
# hook would (so a --wait in progress sees it).
finish_job_later() {
  local root=$1 job=$2 state=$3 message=$4 ref=${5:-}
  (
    sleep 0.5
    args=("done" "$job" --target "$root" --state "$state" --message "$message")
    if [[ -n $ref ]]; then args+=(--result-ref "$ref"); fi
    "$FAKE_BIN/ringmaster" "${args[@]}"
  ) >/dev/null 2>&1 3>&- &
}

# abort_on_cancel <root> <job>: play a producer that tears down when a holder
# cancels: once the journal carries cancel-requested, write aborted.
abort_on_cancel() {
  local root=$1 job=$2
  (
    for _ in $(seq 1 100); do
      if grep -q '"type":"cancel-requested"' "$FAKE_DIR/rm/$root/$job.jsonl" 2>/dev/null; then
        "$FAKE_BIN/ringmaster" "done" "$job" --target "$root" --state aborted --message "cancelled by a holder"
        break
      fi
      sleep 0.1
    done
  ) >/dev/null 2>&1 3>&- &
}

# --- spawn --new-run -------------------------------------------------------

@test "spawn --new-run mints a run root, posts the input and starts the run job" {
  run_jug spawn --new-run --run-key rk1 --input - --issuer issuer-1 --room room-1@rooms.test \
    <<<'{"recording":"file an issue on moxy"}'
  expect_status 0
  printf '%s' "$output" >"$BATS_FILE_TMPDIR/rk1.json"

  [[ $(field .run_key) == rk1 ]]
  [[ $(field .room) == room-1@rooms.test ]]
  [[ $(field .existing) == false ]]
  [[ $(field .resolved) == false ]]
  [[ $(field .torn_down) == false ]]
  [[ $(field .root_stanza) == chat-* ]]
  [[ $(field .root_jid) == "$(field .root_principal)@xmpp.test" ]]

  # The credential is by reference: one password file per identity, under the
  # run's own state directory.
  local pw
  pw=$(field .root_credential_ref)
  [[ $pw == "$XDG_STATE_HOME/juggler/runs/rk1/$(field .root_principal).pw" ]]
  [[ -s $pw ]]

  # The run job lives on the issuer's channel; the run input is the room's
  # root stanza.
  run_job=$(field .run_job)
  [[ -f $FAKE_DIR/rm/issuer-1/$run_job.jsonl ]]
  fake_calls '[.[] | select(.tool=="troupe" and .argv[0]=="mint")] | length == 1' | grep -qx true
  jq -se --arg stanza "$(field .root_stanza)" '
    map(select(.id == $stanza)) | length == 1
    and (.[0].room == "room-1@rooms.test")
    and (.[0].subject | fromjson | .type == "run_input" and .run_key == "rk1" and .input.recording == "file an issue on moxy")
  ' "$FAKE_DIR/muc.jsonl" >/dev/null
}

@test "spawn --new-run with the same --run-key returns the existing run and creates nothing" {
  local posts_before
  posts_before=$(wc -l <"$FAKE_DIR/muc.jsonl")

  run_jug spawn --new-run --run-key rk1 --input - --issuer issuer-1 --room room-1@rooms.test \
    <<<'{"recording":"a redelivery"}'
  expect_status 0
  [[ $(field .existing) == true ]]
  [[ $(field .run_job) == "$(stored_run rk1 .run_job)" ]]
  [[ $(field .root_stanza) == "$(stored_run rk1 .root_stanza)" ]]
  [[ $(field .root_principal) == "$(stored_run rk1 .root_principal)" ]]
  [[ $(field .resolved) == false ]]
  [[ $(field .torn_down) == false ]]
  [[ $(wc -l <"$FAKE_DIR/muc.jsonl") -eq $posts_before ]]
}

@test "spawn --new-run --room-domain without --operator-jid is a usage error and mints nothing" {
  local calls_before
  calls_before=$(wc -l <"$FAKE_DIR/calls.jsonl")
  run_jug spawn --new-run --run-key rk-domain --input - --issuer issuer-1 --room-domain rooms.test <<<'x'
  expect_status 1
  [[ $stderr == *"--operator-jid"* ]]
  [[ $(wc -l <"$FAKE_DIR/calls.jsonl") -eq $calls_before ]]
}

# --- decide ----------------------------------------------------------------

@test "decide exits 0 on a usable choice, posts the decision stanza and records a route entry" {
  set_decision 200 "$(decision_body 0.8)"
  local stanza
  stanza=$(stored_run rk1 .root_stanza)

  run_jug decide --run-key rk1 --model jev --room room-1@rooms.test --parent "$stanza" --min-confidence 0.5 \
    <<<"$DECIDE_PAYLOAD"
  expect_status 0
  [[ $(field .answers.route.choice) == issue ]]
  [[ $(field .id) == gen-dec-1 ]]

  # The request went to the registry entry: upstream model id, the entry's
  # token, and the state/questions from stdin.
  [[ $(<"$DEC_DIR/last-auth") == "Bearer tok-test" ]]
  jq -e '.model == "typesafe/jev-test" and (.state | startswith("file an issue")) and (.questions.route.type == "choice")' \
    "$DEC_DIR/last-request" >/dev/null

  # ONE decision stanza, parented on the recording.
  jq -se --arg parent "$stanza" '
    map(select(.source == "juggler-decide")) | length == 1
    and (.[0].subject | fromjson | .type == "decision" and .parent == $parent and .verdict == "usable")
  ' "$FAKE_DIR/muc.jsonl" >/dev/null

  # The route entry is on the run ledger, readable through job-ledger.
  run_jug job-ledger "$(stored_run rk1 .run_job)"
  expect_status 0
  expect_json '[.calls[] | select(.tool == "route")] | length == 1
    and .[0].ok == true and .[0].kind == "route" and .[0].choice == "issue"
    and .[0].verdict == "usable" and (.[0].stanza_id | startswith("chat-"))'
}

@test "decide exits 4 when the choice is below the threshold" {
  set_decision 200 "$(decision_body 0.2)"
  run_jug decide --run-key rk1 --model jev --room room-1@rooms.test --parent "$(stored_run rk1 .root_stanza)" \
    --min-confidence 0.5 <<<"$DECIDE_PAYLOAD"
  expect_status 4
  [[ $(field .answers.route.choice) == issue ]]
  [[ $(field .reason) == *"below threshold"* ]]

  run_jug job-ledger "$(stored_run rk1 .run_job)"
  expect_json '[.calls[] | select(.tool == "route")] | length == 2
    and .[1].ok == false and .[1].verdict == "below-threshold" and .[1].choice == "issue"'
}

@test "decide exits 2 when the endpoint fails and records a failed route entry" {
  set_decision 500 '{"error":{"message":"upstream down"}}'
  run_jug decide --run-key rk1 --model jev --room room-1@rooms.test --parent "$(stored_run rk1 .root_stanza)" \
    <<<"$DECIDE_PAYLOAD"
  expect_status 2
  [[ $(field .http_status) -eq 500 ]]
  [[ $(field .reason) == *"upstream down"* ]]

  run_jug job-ledger "$(stored_run rk1 .run_job)"
  expect_json '[.calls[] | select(.tool == "route")] | length == 3
    and .[2].ok == false and .[2].verdict == "no-choice"'
}

@test "decide exits 3 when the decision stanza cannot be posted" {
  set_decision 200 "$(decision_body 0.8)"
  mkdir -p "$FAKE_DIR/fail"
  : >"$FAKE_DIR/fail/troupe-muc"
  run_jug decide --run-key rk1 --model jev --room room-1@rooms.test --parent "$(stored_run rk1 .root_stanza)" \
    <<<"$DECIDE_PAYLOAD"
  rm -f "$FAKE_DIR/fail/troupe-muc"
  expect_status 3
  # The decision itself is still printed and recorded.
  [[ $(field .answers.route.choice) == issue ]]
  [[ $stderr == *"posting stanza"* ]]
}

@test "decide exits 1 on a usage error before any call" {
  run_jug decide --model jev --parent x <<<"$DECIDE_PAYLOAD"
  expect_status 1
  [[ $stderr == *"--room is required"* ]]
}

# --- spawn --brief ---------------------------------------------------------

@test "spawn --brief launches the child as a transient unit under the run root" {
  local root
  root=$(stored_run rk1 .root_principal)
  launch_child "$root" "task one"

  local jid job principal
  jid=$(field .jid)
  job=$(field .job)
  principal=${jid%@*}
  [[ $(field .room) == room-1@rooms.test ]]
  printf '%s' "$output" >"$BATS_FILE_TMPDIR/child-one.json"

  # The agent job lives on the run root's channel (its parent), and the root
  # holds the first handle.
  [[ -f $FAKE_DIR/rm/$root/$job.jsonl ]]
  run_jug handles "$job"
  expect_status 0
  expect_json 'length == 1 and .[0].holder != "" and .[0].rights == "observe,close" and .[0].status == "accepted"'
  [[ $(jq -r '.[0].holder' <<<"$output") == "$root" ]]

  # The brief is staged for the unit and dropped into the room as a stanza.
  local staged
  staged=$(ls "$XDG_STATE_HOME"/juggler/runs/rk1/children/*.brief.toml)
  grep -qF "$principal" "$staged"
  grep -qF "task one" "$staged"
  jq -se --arg principal "$principal" --arg job "$job" '
    map(select(.subject | fromjson | .type == "brief" and .principal == $principal and .job == $job)) | length == 1
  ' "$FAKE_DIR/muc.jsonl" >/dev/null

  # The unit: RuntimeMaxSec is the 20s wall clock plus the 30s default grace,
  # ExecStopPost is the exit-wake hook (the sole emitter), and the unit's
  # environment is the CHILD's identity, never the spawner's.
  local unit
  unit=$(fake_calls '[.[] | select(.tool == "systemd-run")][-1]')
  jq -e --arg p "$principal" --arg job "$job" --arg root "$root" '
    (.argv | index("--unit") as $i | .[$i + 1] == "juggler-agent-" + $p)
    and (.argv | index("--collect") != null)
    and (.argv | index("--property=Type=exec") != null)
    and (.argv | index("--property=Delegate=yes") != null)
    and (.argv | index("--property=RuntimeMaxSec=50") != null)
    and ([.argv[] | select(startswith("--property=ExecStopPost="))] | length == 1)
    and ([.argv[] | select(startswith("--property=ExecStopPost="))][0] | contains("exit-wake --job " + $job + " --target " + $root))
    and (.argv | index("--setenv=CLOWN_SESSION_ID=" + $p) != null)
    and (.argv | index("--setenv=TROUPE_XMPP_USER=" + $p) != null)
    and (.argv | index("--setenv=TROUPE_XMPP_DOMAIN=xmpp.test") != null)
    and ([.argv[] | select(startswith("--setenv=TROUPE_XMPP_PASSWORD_FILE="))] | length == 1)
    and (.argv | index("--setenv=XDG_STATE_HOME=" + env.XDG_STATE_HOME) != null)
    and (.argv | index("--setenv=JUGGLER_MODELS_PATH=" + env.JUGGLER_MODELS_PATH) != null)
    and (.argv | index("--setenv=JUGGLER_RINGMASTER_BIN=" + env.JUGGLER_RINGMASTER_BIN) != null)
    and ([.argv[] | select(startswith("--setenv=CLOWN_SESSION_ID=")) ] | length == 1)
    and (.argv | index("run") != null and index("--brief") != null and index("--job") != null)
  ' <<<"$unit" >/dev/null
  # The systemd-run client process never saw the spawner's principal.
  jq -e '.env == {}' <<<"$unit" >/dev/null
}

@test "spawn --brief with the same brief and task returns the same child and starts no second unit" {
  local root units_before
  root=$(stored_run rk1 .root_principal)
  units_before=$(fake_calls '[.[] | select(.tool == "systemd-run")] | length')

  launch_child "$root" "task one"
  [[ $(field .job) == "$(jq -r .job "$BATS_FILE_TMPDIR/child-one.json")" ]]
  [[ $(fake_calls '[.[] | select(.tool == "systemd-run")] | length') -eq $units_before ]]
}

@test "spawn --brief --wait mirrors a succeeded job: exit 0, reason normal, artifacts from the ledger" {
  local root job spool
  root=$(stored_run rk1 .root_principal)
  job=$(jq -r .job "$BATS_FILE_TMPDIR/child-one.json")
  spool="$FAKE_DIR/rm/$root/$job.out"
  printf '%s' '{"schema":1,"calls":[{"tool":"ring_create_issue","kind":"issue","ok":true,"uris":["https://forge.test/o/r/issues/1"]}],"end":{"reason":"end_turn"},"cannot_complete":null,"steps":2,"elapsed_ms":10}' >"$spool"
  finish_job_later "$root" "$job" succeeded "evaluator passed" "$spool"

  run_jug_as "$root" spawn --brief "$BATS_FILE_TMPDIR/issue-filer.toml" --task - --wait --timeout 30s <<<"task one"
  expect_status 0
  [[ $(field .job) == "$job" ]]
  [[ $(field .state) == succeeded ]]
  [[ $(field .reason) == normal ]]
  [[ $(field .ledger) == "$spool" ]]
  expect_json '.artifacts == [{"tool":"ring_create_issue","kind":"issue","uris":["https://forge.test/o/r/issues/1"]}] and .cannot_complete == null'
}

@test "spawn --brief --wait exits 2 for a failed job and carries cannot_complete" {
  local root job spool
  root=$(stored_run rk1 .root_principal)
  launch_child "$root" "task two"
  job=$(field .job)
  spool="$FAKE_DIR/rm/$root/$job.out"
  printf '%s' '{"schema":1,"calls":[],"end":{"reason":"cannot_complete"},"cannot_complete":{"reason":"no repo matches"},"steps":1,"elapsed_ms":5}' >"$spool"
  finish_job_later "$root" "$job" failed 'cannot_complete: "no repo matches"' "$spool"

  run_jug_as "$root" spawn --brief "$BATS_FILE_TMPDIR/issue-filer.toml" --task - --wait --timeout 30s <<<"task two"
  expect_status 2
  [[ $(field .state) == failed ]]
  [[ $(field .reason) == failed ]]
  [[ $(field .message) == *"no repo matches"* ]]
  expect_json '.cannot_complete == {"reason":"no repo matches"} and .artifacts == []'
}

@test "spawn --brief --wait exits 3 for an aborted job" {
  local root job
  root=$(stored_run rk1 .root_principal)
  launch_child "$root" "task aborted"
  job=$(field .job)
  finish_job_later "$root" "$job" aborted "cancelled by a holder"

  run_jug_as "$root" spawn --brief "$BATS_FILE_TMPDIR/issue-filer.toml" --task - --wait --timeout 30s <<<"task aborted"
  expect_status 3
  [[ $(field .state) == aborted ]]
  [[ $(field .reason) == shutdown ]]
}

@test "spawn --brief --wait exits 4 for an interrupted job" {
  local root job
  root=$(stored_run rk1 .root_principal)
  launch_child "$root" "task interrupted"
  job=$(field .job)
  finish_job_later "$root" "$job" interrupted "crash: unit result exit-code"

  run_jug_as "$root" spawn --brief "$BATS_FILE_TMPDIR/issue-filer.toml" --task - --wait --timeout 30s <<<"task interrupted"
  expect_status 4
  [[ $(field .state) == interrupted ]]
  [[ $(field .reason) == crash ]]
}

@test "spawn --brief --wait at --timeout cancels the job and exits 3 when it aborts within the stop grace" {
  local root job
  root=$(stored_run rk1 .root_principal)
  launch_child "$root" "task timeout-abort"
  job=$(field .job)
  abort_on_cancel "$root" "$job"

  run_jug_as "$root" spawn --brief "$BATS_FILE_TMPDIR/issue-filer.toml" --task - --wait --timeout 1s --stop-grace 10s \
    <<<"task timeout-abort"
  expect_status 3
  [[ $(field .state) == aborted ]]
  [[ $(field .reason) == shutdown ]]
  journal_types "$root" "$job" | grep -qx cancel-requested
  fake_calls '[.[] | select(.tool == "ringmaster" and .argv[0] == "cancel" and .argv[1] == "'"$job"'")] | length >= 1' | grep -qx true
}

@test "spawn --brief --wait at --timeout exits 5 when the cancelled job is still running after the stop grace" {
  local root job
  root=$(stored_run rk1 .root_principal)
  launch_child "$root" "task stuck"
  job=$(field .job)
  printf '%s' "$job" >"$BATS_FILE_TMPDIR/stuck-job"

  run_jug_as "$root" spawn --brief "$BATS_FILE_TMPDIR/issue-filer.toml" --task - --wait --timeout 1s --stop-grace 1s \
    <<<"task stuck"
  expect_status 5
  [[ $(field .state) == running ]]
  journal_types "$root" "$job" | grep -qx cancel-requested
}

# --- job-ledger ------------------------------------------------------------

@test "job-ledger prints an agent job's result spool" {
  local job
  job=$(jq -r .job "$BATS_FILE_TMPDIR/child-one.json")
  run_jug job-ledger "$job"
  expect_status 0
  expect_json '.calls[0].tool == "ring_create_issue" and .end.reason == "end_turn"'
}

@test "job-ledger on an unknown job without --target exits 1" {
  run_jug job-ledger nope-12345678
  expect_status 1
  [[ $stderr == *"--target"* ]]
}

# --- resolve ---------------------------------------------------------------

@test "resolve without the minter credential refuses before touching anything" {
  local calls_before
  calls_before=$(wc -l <"$FAKE_DIR/calls.jsonl")
  run_jug_env TROUPE_MINT_PASSWORD_FILE= TROUPE_MINT_USER= -- resolve "$(stored_run rk1 .run_job)" \
    --state failed --reason x --result-line "rk1: x" --canary-room canary@rooms.test
  expect_status 1
  [[ $stderr == *TROUPE_MINT_PASSWORD_FILE* && $stderr == *--keep-accounts* ]]
  [[ $(wc -l <"$FAKE_DIR/calls.jsonl") -eq $calls_before ]]
}

@test "resolve stops live children, records the fallback and wakes the issuer" {
  local root run_job live
  root=$(stored_run rk1 .root_principal)
  run_job=$(stored_run rk1 .run_job)
  launch_child "$root" "task live"
  live=$(field .job)
  abort_on_cancel "$root" "$live"

  run_jug resolve "$run_job" --state failed --reason "router below threshold" \
    --fallback-artifacts '[{"tool":"orgzly_note","kind":"note","uris":["orgzly://note/1"]}]' --stop-grace 2s
  expect_status 0
  [[ $(field .state) == failed ]]
  [[ $(field .reason) == failed ]]
  [[ $(field .already_resolved) == false ]]
  expect_json '.woken == ["issuer-1"]'

  # Two children were still live: one aborted inside the grace, the stuck one
  # did not. Each is a subagent_stop entry, then the fallback closes the ledger.
  run_jug job-ledger "$run_job"
  expect_status 0
  local stuck
  stuck=$(<"$BATS_FILE_TMPDIR/stuck-job")
  expect_json '([.calls[] | select(.tool == "subagent_stop")] | length) == 2
    and ([.calls[] | select(.tool == "subagent_stop" and .ok == true and .stopped_by_resolve == true and .job == "'"$live"'")] | length) == 1
    and ([.calls[] | select(.tool == "subagent_stop" and .ok == false and .job == "'"$stuck"'")] | length) == 1'
  expect_json '([.calls[] | select(.tool == "fallback")] | length) == 1
    and ([.calls[] | select(.tool == "fallback")][0] | .ok == true and .ran == true
      and .uris == ["orgzly://note/1"] and .reason == "router below threshold")
    and .resolved.state == "failed"'

  # Teardown follows the fallback: the stuck child keeps its account, so the
  # root keeps its own and the run is not torn down.
  expect_json '([.calls[] | select(.tool == "teardown" and .ok == false and .reason == "still running")] | length) == 1
    and ([.calls[] | select(.tool == "teardown")][-1] | .principal == "'"$root"'" and .ok == false)'
  [[ -s $(stored_run rk1 .root_credential_ref) ]]

  # The run job is terminal failed, and the issuer got ONE wake.
  [[ $(journal_types issuer-1 "$run_job" | tail -n1) == failed ]]
  jq -se --arg job "$run_job" '
    map(select(.target == "issuer-1" and (.message | startswith("exit " + $job + " failed reason=failed")))) | length == 1
  ' "$FAKE_DIR/wakes.jsonl" >/dev/null
}

@test "resolve on an already-resolved run is a no-op that reports the stored verdict" {
  local wakes_before
  wakes_before=$(wc -l <"$FAKE_DIR/wakes.jsonl")
  run_jug resolve "$(stored_run rk1 .run_job)" --state succeeded --reason "second try"
  expect_status 0
  [[ $stderr == *"already resolved"* ]]
  [[ $(field .state) == failed ]]
  [[ $(field .already_resolved) == true ]]
  [[ $(wc -l <"$FAKE_DIR/wakes.jsonl") -eq $wakes_before ]]
}

@test "spawn --brief on a resolved run is refused" {
  run_jug_as "$(stored_run rk1 .root_principal)" spawn --brief "$BATS_FILE_TMPDIR/issue-filer.toml" --task - <<<"too late"
  expect_status 1
  [[ $stderr == *"already resolved"* ]]
}

# --- exit-wake -------------------------------------------------------------

@test "exit-wake writes the terminal the unit result implies and wakes the holder once" {
  run_jug spawn --new-run --run-key rk2 --input - --issuer issuer-2 --room room-2@rooms.test <<<'{"recording":"second"}'
  expect_status 0
  printf '%s' "$output" >"$BATS_FILE_TMPDIR/rk2.json"
  local root
  root=$(stored_run rk2 .root_principal)

  # SERVICE_RESULT=timeout: RuntimeMaxSec expired -> failed / failed.
  launch_child "$root" "wake timeout"
  local job
  job=$(field .job)
  printf '%s' "$job" >"$BATS_FILE_TMPDIR/wake-timeout-job"
  run_jug_env SERVICE_RESULT=timeout -- exit-wake --job "$job" --target "$root"
  expect_status 0
  [[ $(field .state) == failed ]]
  [[ $(field .reason) == failed ]]
  [[ $(field .wrote_terminal) == true ]]
  expect_json ".woken == [\"$root\"]"
  [[ $(journal_types "$root" "$job" | tail -n1) == failed ]]
  jq -se --arg job "$job" --arg root "$root" '
    map(select(.target == $root and (.message | startswith("exit " + $job + " failed reason=failed")))) | length == 1
  ' "$FAKE_DIR/wakes.jsonl" >/dev/null
  [[ -f $XDG_STATE_HOME/juggler/exit-wakes/$job ]]

  # A second run of the hook is idempotent: no second terminal, no second wake.
  local wakes_before
  wakes_before=$(wc -l <"$FAKE_DIR/wakes.jsonl")
  run_jug_env SERVICE_RESULT=timeout -- exit-wake --job "$job" --target "$root"
  expect_status 0
  [[ $(field .already_woken) == true ]]
  [[ $(field .wrote_terminal) == false ]]
  expect_json '.woken == []'
  [[ $(wc -l <"$FAKE_DIR/wakes.jsonl") -eq $wakes_before ]]
}

@test "exit-wake maps a signal death to interrupted/killed" {
  local root job
  root=$(stored_run rk2 .root_principal)
  launch_child "$root" "wake signal"
  job=$(field .job)
  run_jug_env SERVICE_RESULT=signal EXIT_STATUS=KILL -- exit-wake --job "$job" --target "$root"
  expect_status 0
  [[ $(field .state) == interrupted ]]
  [[ $(field .reason) == killed ]]
  [[ $(field .message) == *"signal KILL"* ]]
  [[ $(journal_types "$root" "$job" | tail -n1) == interrupted ]]
}

@test "exit-wake maps a non-zero exit to interrupted/crash" {
  local root job
  root=$(stored_run rk2 .root_principal)
  launch_child "$root" "wake exit-code"
  job=$(field .job)
  run_jug_env SERVICE_RESULT=exit-code EXIT_CODE=exited EXIT_STATUS=3 -- exit-wake --job "$job" --target "$root"
  expect_status 0
  [[ $(field .state) == interrupted ]]
  [[ $(field .reason) == crash ]]
  [[ $(field .message) == *"exit code exited"* ]]
}

@test "exit-wake relays the verdict the agent already wrote and does not rewrite it" {
  local root job
  root=$(stored_run rk2 .root_principal)
  launch_child "$root" "wake success"
  job=$(field .job)
  "$FAKE_BIN/ringmaster" "done" "$job" --target "$root" --state succeeded --message "evaluator passed"

  run_jug_env SERVICE_RESULT=success -- exit-wake --job "$job" --target "$root"
  expect_status 0
  [[ $(field .state) == succeeded ]]
  [[ $(field .reason) == normal ]]
  [[ $(field .wrote_terminal) == false ]]
  expect_json ".woken == [\"$root\"]"
  [[ $(journal_types "$root" "$job" | grep -cE '^(succeeded|failed|aborted|interrupted)$') -eq 1 ]]
}

# --- room provisioning and teardown ----------------------------------------

# calls_since <n> <jq filter>: the filter over the platform calls recorded
# after the first n.
calls_since() { jq -s ".[$1:] | $2" "$FAKE_DIR/calls.jsonl"; }

@test "spawn --new-run --room-domain --operator-jid creates <run-key>@<domain> as the root with the operator as owner" {
  run_jug spawn --new-run --run-key rk3 --input - --issuer issuer-4 --room-domain rooms.test \
    --operator-jid operator@xmpp.test <<<'{"recording":"third"}'
  expect_status 0
  printf '%s' "$output" >"$BATS_FILE_TMPDIR/rk3.json"
  [[ $(field .room) == rk3@rooms.test ]]
  [[ $(field .operator_jid) == operator@xmpp.test ]]

  local root
  root=$(field .root_principal)
  fake_calls '[.[] | select(.tool == "troupe" and .argv[0] == "muc" and .argv[1] == "create")]' >"$BATS_TEST_TMPDIR/create.json"
  jq -e --arg root "$root" --arg pw "$(field .root_credential_ref)" '
    length == 1
    and .[0].argv == ["muc", "create", "--room", "rk3@rooms.test", "--owner", "operator@xmpp.test"]
    and .[0].env.TROUPE_XMPP_USER == $root and .[0].env.TROUPE_XMPP_PASSWORD_FILE == $pw
  ' "$BATS_TEST_TMPDIR/create.json" >/dev/null
  jq -e --arg root "$root@xmpp.test" '.affiliations == {($root): "owner", "operator@xmpp.test": "owner"}' \
    "$FAKE_DIR/rooms/rk3@rooms.test.json" >/dev/null
}

@test "spawn --brief into a created room affiliates the child as a member before the unit starts" {
  local root calls_before
  root=$(stored_run rk3 .root_principal)
  calls_before=$(wc -l <"$FAKE_DIR/calls.jsonl")
  launch_child "$root" "task rooms"
  printf '%s' "$output" >"$BATS_FILE_TMPDIR/child-rooms.json"
  local jid
  jid=$(field .jid)

  calls_since "$calls_before" '[.[] | select((.tool == "troupe" and .argv[0] == "muc" and .argv[1] == "affiliate") or .tool == "systemd-run") | .tool + " " + (.argv | join(" "))]' \
    >"$BATS_TEST_TMPDIR/order.json"
  jq -e --arg jid "$jid" '
    length == 2
    and .[0] == "troupe muc affiliate --room rk3@rooms.test --affiliation member --jid " + $jid
    and (.[1] | startswith("systemd-run "))
  ' "$BATS_TEST_TMPDIR/order.json" >/dev/null
  jq -e --arg jid "$jid" '.affiliations[$jid] == "member"' "$FAKE_DIR/rooms/rk3@rooms.test.json" >/dev/null
}

@test "resolve --result-line --canary-room posts the canary first, then revokes every account" {
  local root run_job job child_jid child_pw root_pw calls_before
  root=$(stored_run rk3 .root_principal)
  run_job=$(stored_run rk3 .run_job)
  root_pw=$(stored_run rk3 .root_credential_ref)
  job=$(jq -r .job "$BATS_FILE_TMPDIR/child-rooms.json")
  child_jid=$(jq -r .jid "$BATS_FILE_TMPDIR/child-rooms.json")
  child_pw="$XDG_STATE_HOME/juggler/runs/rk3/${child_jid%@*}.pw"
  [[ -s $child_pw ]]
  "$FAKE_BIN/ringmaster" "done" "$job" --target "$root" --state succeeded --message "evaluator passed"

  calls_before=$(wc -l <"$FAKE_DIR/calls.jsonl")
  run_jug resolve "$run_job" --state succeeded --reason "issue filed" \
    --result-line "rk3: issue filed" --canary-room canary@rooms.test
  expect_status 0
  [[ $(field .torn_down) == true ]]
  expect_json '.canary.room == "canary@rooms.test" and .canary.posted == true and (.canary.stanza | startswith("chat-"))'
  local canary_stanza
  canary_stanza=$(field .canary.stanza)

  # The canary post is resolve's FIRST troupe call, made as the run root.
  calls_since "$calls_before" '[.[] | select(.tool == "troupe")]' >"$BATS_TEST_TMPDIR/troupe.json"
  jq -e --arg root "$root" '
    .[0].argv[0:4] == ["muc", "send", "--room", "canary@rooms.test"]
    and (.[0].argv | index("rk3: issue filed") != null and index("juggler-resolve") != null)
    and .[0].env.TROUPE_XMPP_USER == $root
  ' "$BATS_TEST_TMPDIR/troupe.json" >/dev/null

  # Each child: de-affiliated, then revoked; then the root steps down and is
  # revoked. The password files are gone; only the operator owns the room.
  jq -e --arg cj "$child_jid" --arg rj "$root@xmpp.test" --arg root "$root" --arg cpw "$child_pw" --arg rpw "$root_pw" '
    [.[] | select(.argv[0] != "message") | .argv | join(" ")][1:] == [
      "muc affiliate --room rk3@rooms.test --affiliation none --jid " + $cj,
      "mint-revoke --session-key " + ($cj | split("@")[0]) + " --password-file " + $cpw,
      "muc affiliate --room rk3@rooms.test --affiliation none --jid " + $rj,
      "mint-revoke --session-key " + $root + " --password-file " + $rpw
    ]
  ' "$BATS_TEST_TMPDIR/troupe.json" >/dev/null
  [[ ! -e $child_pw ]]
  [[ ! -e $root_pw ]]
  jq -e '.affiliations == {"operator@xmpp.test": "owner"}' "$FAKE_DIR/rooms/rk3@rooms.test.json" >/dev/null

  run_jug job-ledger "$run_job"
  expect_json '.calls[0].tool == "canary" and .calls[0].ok == true
    and ([.calls[] | select(.tool == "teardown" and .kind == "account" and .ok == true)] | length) == 2'

  # A redelivery sees the run finished; resolving again touches nothing.
  run_jug spawn --new-run --run-key rk3 --input - --issuer issuer-4 --room-domain rooms.test \
    --operator-jid operator@xmpp.test <<<'{"recording":"third"}'
  expect_status 0
  [[ $(field .existing) == true ]]
  [[ $(field .resolved) == true ]]
  [[ $(field .torn_down) == true ]]

  calls_before=$(wc -l <"$FAKE_DIR/calls.jsonl")
  run_jug resolve "$run_job" --state succeeded --reason "again" --result-line "x" --canary-room canary@rooms.test
  expect_status 0
  [[ $(field .already_resolved) == true ]]
  [[ $(field .canary.posted) == true ]]
  [[ $(field .canary.stanza) == "$canary_stanza" ]]
  expect_json 'has("teardown") | not'
  [[ $(wc -l <"$FAKE_DIR/calls.jsonl") -eq $calls_before ]]
}

@test "spawn --new-run that fails after creating the room is resumed by a retry with the same --run-key" {
  mkdir -p "$FAKE_DIR/fail"
  : >"$FAKE_DIR/fail/troupe-muc-send"
  run_jug spawn --new-run --run-key rk4 --input - --issuer issuer-5 --room-domain rooms.test \
    --operator-jid operator@xmpp.test <<<'{"recording":"fourth"}'
  rm -f "$FAKE_DIR/fail/troupe-muc-send"
  expect_status 1
  [[ $stderr == *"posting the run input"* ]]
  # stdout names the pending run, so the glue knows a retry resumes it.
  expect_json '.run_key == "rk4" and .pending == true and .room == "rk4@rooms.test" and (.root_principal | length) == 36'
  local pending_root
  pending_root=$(field .root_principal)

  # The root is kept for the retry: a pending record, its password file, no revoke.
  local record=$XDG_STATE_HOME/juggler/runs/rk4.json root
  jq -e '.pending == true and .room_created == true and .run_job == ""' "$record" >/dev/null
  root=$(jq -r .root_principal "$record")
  [[ $root == "$pending_root" ]]
  [[ -s $XDG_STATE_HOME/juggler/runs/rk4/$root.pw ]]
  fake_calls '[.[] | select(.tool == "troupe" and .argv[0] == "mint-revoke" and (.argv | index("'"$root"'") != null))] | length == 0' | grep -qx true

  run_jug spawn --new-run --run-key rk4 --input - --issuer issuer-5 --room-domain rooms.test \
    --operator-jid operator@xmpp.test <<<'{"recording":"fourth"}'
  expect_status 0
  [[ $(field .existing) == false ]]
  [[ $(field .resumed) == true ]]
  [[ $(field .root_principal) == "$root" ]]
  [[ $(field .room) == rk4@rooms.test ]]
  jq -e '.pending == null' "$record" >/dev/null

  # The same root created the room twice (idempotently); one run job is open.
  fake_calls '[.[] | select(.tool == "troupe" and .argv[0] == "muc" and .argv[1] == "create" and .argv[3] == "rk4@rooms.test") | .env.TROUPE_XMPP_USER] | . == ["'"$root"'", "'"$root"'"]' | grep -qx true
  [[ $(ls "$FAKE_DIR/rm/issuer-5/"*.jsonl | wc -l) -eq 1 ]]
}

# --- binary resolution -----------------------------------------------------

@test "a --ringmaster flag overrides JUGGLER_RINGMASTER_BIN" {
  run_jug spawn --new-run --run-key rk-flag --input - --issuer issuer-3 --room room-3@rooms.test \
    --ringmaster /nonexistent/ringmaster <<<'x'
  expect_status 1
  [[ $stderr == *"ringmaster"* ]]
}
