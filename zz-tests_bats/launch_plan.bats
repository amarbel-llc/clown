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

  # Stub multiplexer, same shape as clownfile_attach.bats: records its argv to a
  # file so a test can prove the [attach] wrap did — or did not — happen without
  # zmx being installed.
  STUB_BIN="$BATS_TEST_TMPDIR/bin"
  mkdir -p "$STUB_BIN"
  MUX_ARGV="$BATS_TEST_TMPDIR/zmx-argv"
  cat >"$STUB_BIN/zmx" <<EOF
#!$(command -v bash)
printf '%s\n' "\$@" >"$MUX_ARGV"
exit 0
EOF
  chmod +x "$STUB_BIN/zmx"
  export PATH="$STUB_BIN:$PATH" STUB_BIN MUX_ARGV

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
# The temp dirs are numbered per family by first appearance, NOT collapsed to a
# single placeholder. claude is handed two distinct clown-plugin-compile-* dirs
# (the job-monitor and juggler synth dirs); rendering both as one placeholder
# would mean a staging-root migration that wrongly gave them a SHARED path still
# produced an identical golden and passed green — the precise mistake
# "collapse seven temp dirs into one home" invites, and the one this lane exists
# to catch. Identical raw paths still map to identical placeholders, so a
# genuinely repeated path stays visibly repeated.
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
      -e 's#\("--session-id","\)[0-9a-f-]\{36\}#\1<uuid>#g' \
      -e 's#\(/clown/crush/\)[0-9a-f]\{1,\}#\1<hash>#g' |
    # Number each mkdtemp'd dir per family by first appearance. sed cannot do
    # this: it has no memory across matches, so every clown-plugin-compile-*
    # would render as the same placeholder no matter how many distinct dirs
    # there were.
    awk '
      {
        rest = $0
        out = ""
        while (match(rest, /clown-[a-z-]+-[0-9]+/)) {
          out = out substr(rest, 1, RSTART - 1)
          tok = substr(rest, RSTART, RLENGTH)
          rest = substr(rest, RSTART + RLENGTH)
          family = tok
          sub(/-[0-9]+$/, "", family)
          if (!(tok in seen)) {
            count[family]++
            seen[tok] = count[family]
          }
          out = out family "-<rand#" seen[tok] ">"
        }
        print out rest
      }
    '
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

@test "--print-launch-plan runs inline instead of wrapping in the multiplexer" {
  # The regression this pins: maybeReexecMultiplexer runs ~40 lines BEFORE the
  # refusal check below, and reexecArgv() emits only user/selection-derived
  # flags — so --print-launch-plan does not survive into the inner clown. With
  # [attach] enabled (the shipped default) on a tty, clown would wrap itself and
  # the INNER clown would spawn the provider for real: the one outcome the
  # flag's contract rules out. Before the fix this test sees the stub's argv
  # file and an empty stdout.
  #
  # CLOWN_ATTACH_FORCE=1 substitutes for the tty that bats does not have; it is
  # the documented test seam for exactly this gate, and without it the run would
  # take the non-interactive path and pass trivially.
  cat >"$project/clownfile" <<'EOF'
[attach]
multiplexer = "zmx"
start = ["zmx", "attach", "{id}", "{entry}"]
EOF

  cd "$project"
  run env CLOWN_ATTACH_FORCE=1 CLOWN_SESSION_ID=plan-sess \
    "$CLOWN_BIN" --provider claude --print-launch-plan -- --version
  assert_success
  [ ! -f "$MUX_ARGV" ] || {
    echo "multiplexer was invoked with: $(cat "$MUX_ARGV")" >&2
    return 1
  }
  # The plan still reaches OUR stdout — the point of running inline. Asserted
  # structurally rather than against the golden: CLOWN_SESSION_ID pins the
  # identity key, so the injected --session-id is "plan-sess" rather than the
  # uuid the golden's normalizer expects.
  assert_equal "${#lines[@]}" 1
  run jq -r .binary <<<"$output"
  assert_success
  assert_output --partial "/bin/claude"
}

@test "the multiplexer wrap still happens without --print-launch-plan" {
  # The control arm. Without it the previous test cannot distinguish "the flag
  # suppressed the wrap" from "the wrap never fired here for some unrelated
  # reason" (stub not on PATH, CLOWN_ATTACH_FORCE ignored, clownfile not read).
  cat >"$project/clownfile" <<'EOF'
[attach]
multiplexer = "zmx"
start = ["zmx", "attach", "{id}", "{entry}"]
EOF

  cd "$project"
  run env CLOWN_ATTACH_FORCE=1 CLOWN_SESSION_ID=plan-sess \
    "$CLOWN_BIN" --provider claude -- --version
  assert_success
  [ -f "$MUX_ARGV" ]
  run cat "$MUX_ARGV"
  assert_line --index 0 "attach"
  assert_line --index 1 "plan-sess"
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
