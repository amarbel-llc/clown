# Integration coverage for `clown profile <list|add|edit|remove>` (plan Task
# 10, docs/plans/2026-07-06-openrouter-profiles.md). setup_test_home roots
# $HOME/$XDG_CONFIG_HOME at $BATS_TEST_TMPDIR, so user profiles.toml never
# leaks in or out of a test. Interactive add/edit/remove forms (huh) need a
# real TTY, which bats does not provide; those paths are exercised only far
# enough to confirm the TTY gate fires — the form itself is covered by Go
# unit tests in cmd/clown/profileform_test.go.

setup() {
  load 'lib/common.bash'
  setup_test_home
  require_bin CLOWN_BIN clown
}

@test "profile list shows builtin profiles" {
  run "$CLOWN_BIN" profile list
  assert_success
  assert_output --partial "claude-anthropic"
  assert_output --partial "builtin"
}

@test "profile edit of unknown name fails listing available" {
  run "$CLOWN_BIN" profile edit no-such-profile
  assert_failure
  assert_output --partial "no-such-profile"
}

@test "profile add refuses without a TTY" {
  run "$CLOWN_BIN" profile add </dev/null
  assert_failure
  assert_output --partial "TTY"
}

@test "profile remove of a builtin profile fails" {
  run "$CLOWN_BIN" profile remove claude-anthropic </dev/null
  assert_failure
  assert_output --partial "builtin"
}
