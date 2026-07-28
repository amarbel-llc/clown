# bats file_tags=launch_plan

# Characterization coverage for the launch path, ahead of the containment-
# primitive refactor (docs/plans/2026-07-28-containment-primitive-design.md).
#
# clown's launch path is nearly pure — flags plus config produce a command —
# so `--print-launch-plan` dumps {binary, args, env, files} as JSON and exits
# without spawning. The fixtures under golden/ are that dump captured from the
# PRE-refactor code. Bundling argv+env into one `Command` and collapsing the
# seven per-launch temp dirs into one staging root must reproduce them
# byte-for-byte; a diff here is the refactor changing observable behavior.
#
# That is also why these tests assert equality against a whole file rather
# than `--partial` on interesting substrings: a characterization test's job is
# to notice the change nobody predicted, and a substring assertion only
# notices the ones someone thought to write down.
#
# The claude arm is the control (the design doc's point 2): its behavior must
# not change at all. There is deliberately NO tent arm — tent is shelved, and
# writing a golden before verifying its suspected appendFile bug would enshrine
# broken behavior as the contract.

setup() {
  load 'lib/common.bash'
  setup_test_home
  require_bin CLOWN_BIN clown

  # opencode and crush with no --profile fall back to reading these files and
  # error out if they are absent. Same shape as provider_mcp.bats: the URL is
  # never dialed, since --print-launch-plan exits before anything is spawned.
  clown_config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/clown"
  mkdir -p "$clown_config_dir"
  cat >"$clown_config_dir/opencode.toml" <<'EOF'
url = "http://127.0.0.1:1/v1"
token = "local"
EOF
  cat >"$clown_config_dir/crush.toml" <<'EOF'
url = "http://127.0.0.1:1/v1"
token = "local"
EOF

  # A project directory is the unit of hermeticity: clown resolves the
  # clownfile from cwd, and crush's data-dir name is a hash of it.
  project="$BATS_TEST_TMPDIR/project"
  mkdir -p "$project"
  export clown_config_dir project
}

# normalize_plan rewrites the run-to-run-variable parts of a launch plan into
# stable placeholders, so the fixture pins the SHAPE of the invocation rather
# than the identity of one particular run.
#
# Every substitution corresponds to something that provably differs between two
# consecutive invocations on the same machine, or between two machines —
# verified by capturing every provider twice before writing these:
#
#   - store paths        rebuild/version bump of the provider package
#   - sandbox roots      $HOME / $BATS_TEST_TMPDIR / $TMPDIR (bats-island)
#   - clown-*-<digits>   the seven mkdtemp'd per-launch dirs this refactor is
#                        about to collapse into a single staging root
#   - --session-id       a fresh uuid per launch (decideClaudeSession)
#   - crush data dir     a hash of the absolute project path
#
# Plus one thing that does NOT vary per run but is elided anyway: the --agents
# payload, a ~7 KB JSON blob of built-in subagent definitions baked in at build
# time. It is inert with respect to this refactor (BuildClaudeArgs assembles it
# from buildcfg.AgentsFile, nowhere near argv/env bundling or temp-dir
# placement), and keeping it would mean every edit to an agent's prompt text
# breaks this lane — exactly the "fixtures flap for reasons unrelated to the
# code under test" failure the whole normalizer exists to prevent. The --agents
# FLAG and its position in argv are still pinned; only the payload is elided.
#
# Nothing else is touched. Argv order and the complete env key set survive
# normalization, because those are precisely what the refactor could break.
normalize_plan() {
  jq -c '
    .args as $a
    | .args = [ range(0; ($a | length)) as $i
                | if $i > 0 and $a[$i - 1] == "--agents" then "<elided>" else $a[$i] end ]
  ' |
    # Longest prefix first. $HOME lives under $BATS_TEST_TMPDIR, which lives
    # under $TMPDIR; substituting an outer root first rewrites the prefix of
    # the inner ones so they can never match again (which is how the first
    # draft of this leaked a bats-run-XXXXXX name into the crush plan).
    sed \
      -e "s#$HOME#<home>#g" \
      -e "s#$BATS_TEST_TMPDIR#<test>#g" \
      -e "s#${TMPDIR:-/tmp}#<tmp>#g" \
      -e 's#/nix/store/[a-z0-9]\{32\}-[^/"]*#<store>#g' \
      -e 's#clown-\([a-z-]*\)-[0-9]\{1,\}#clown-\1-<rand>#g' \
      -e 's#\("--session-id","\)[0-9a-f-]\{36\}#\1<uuid>#g' \
      -e 's#\(/clown/crush/\)[0-9a-f]\{1,\}#\1<hash>#g'
}

# assert_matches_golden <name> — compare the normalized plan on stdout with
# zz-tests_bats/golden/launch-plan-<name>.json.
#
# On mismatch it prints the normalized ACTUAL, which is how a fixture gets
# (re)generated: run the lane, lift the actual out of the build log, and paste
# it in — but only once you have confirmed the diff is an INTENDED behavior
# change. Regenerating to make the lane green is how a characterization test
# stops meaning anything.
assert_matches_golden() {
  local name="$1"
  local golden="$BATS_TEST_DIRNAME/golden/launch-plan-${name}.json"

  # Validate before normalizing: normalize_plan pipes through jq, so invalid
  # JSON would otherwise surface as a confusing empty-vs-golden diff rather
  # than "the plan wasn't JSON".
  if ! jq -e . >/dev/null 2>&1 <<<"$output"; then
    echo "launch plan is not valid JSON: $output" >&2
    return 1
  fi

  local actual
  actual="$(normalize_plan <<<"$output")"

  if [[ ! -f $golden ]]; then
    echo "missing golden: $golden" >&2
    echo "actual (normalized): $actual" >&2
    return 1
  fi

  if [[ $actual != "$(cat "$golden")" ]]; then
    echo "launch plan differs from $golden" >&2
    echo "expected: $(cat "$golden")" >&2
    echo "actual:   $actual" >&2
    return 1
  fi
}

# run_plan <provider> — dump the launch plan for one provider from the project
# dir. `-- --version` is the forwarded argv; it is never executed, but it keeps
# the fixture's args array non-empty so an argv-dropping regression is visible.
run_plan() {
  cd "$project" || return 1
  run timeout 120 "$CLOWN_BIN" --provider "$1" --print-launch-plan -- --version
}

@test "claude launch plan matches its golden" {
  run_plan claude
  assert_success
  assert_matches_golden claude
}

@test "opencode launch plan matches its golden" {
  run_plan opencode
  assert_success
  assert_matches_golden opencode
}

@test "crush launch plan matches its golden" {
  run_plan crush
  assert_success
  assert_matches_golden crush
}

@test "--print-launch-plan does not spawn the provider" {
  # The whole seam rests on this: if the provider actually ran, the fixtures
  # would be describing a side effect rather than a plan. `--version` is the
  # cheapest observable — every one of these binaries prints its version and
  # nothing else — so its ABSENCE from stdout is the proof.
  run_plan opencode
  assert_success
  refute_output --regexp '^[0-9]+\.[0-9]+\.[0-9]+$'
  # And stdout is exactly one line: the plan, with no trailing resume hint or
  # human chatter that a consumer would have to strip.
  assert_equal "${#lines[@]}" 1
}

@test "--print-launch-plan refuses the exec-replacing paths" {
  # --naked and codex syscall.Exec the provider instead of spawning a child, so
  # they never reach the runProvider seam where the plan is built. Accepting the
  # flag there and ignoring it would LAUNCH the provider — the single outcome
  # the flag's contract rules out — so it must refuse instead.
  cd "$project"
  run timeout 120 "$CLOWN_BIN" --provider claude --naked --print-launch-plan -- --version
  assert_failure
  assert_output --partial "not supported with --naked"
}

# NOT covered in this lane: secret redaction. It is a real requirement — these
# fixtures are committed, so an unredacted token would enter git history
# permanently — but no provider currently puts a credential into the plan's
# env (opencode contributes OPENCODE_CONFIG / OPENCODE_DISABLE_PROJECT_CONFIG,
# crush contributes CRUSH_GLOBAL_CONFIG, claude contributes nothing), so a bats
# arm asserting "the token is absent" would pass whether or not redaction
# exists. It is unit-tested exhaustively instead, at the one place that can
# construct the input: TestLaunchPlanJSON_RedactsSecrets in
# cmd/clown/launchplan_test.go. Add an arm here the moment a provider starts
# passing a token through bindResult.Env.
