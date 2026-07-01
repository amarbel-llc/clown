# Integration test for clownfile [attach] (RFC-0013 §1.3, clown#145).
#
# Asserts the multiplexer self-wrap on boot: with [attach] multiplexer="zmx",
# clown re-execs itself under the configured `start` template, substituting {id}
# and splicing {entry} = [clown, --clown-attach-id, <id>, <orig args>]. A stub
# `zmx` on PATH records its argv and exits 0, so the outer clown's wrap is
# observed without a real multiplexer or any provider launch — the wrap happens
# before provider dispatch.
#
# CLOWN_ATTACH_FORCE=1 bypasses the interactive-TTY gate (bats has no PTY). The
# loop guard (an inner --clown-attach-id run does NOT re-wrap) and the
# disabled/none skip are covered by Go unit tests (cmd/clown/attach_test.go).
#
# Two sandbox gotchas this handles: env reaches clown via `run env VAR=...`
# (a `VAR=v run cmd` prefix does not, since `run` is a function), and the stub's
# shebang uses the resolved bash path (the nix sandbox has no /usr/bin/env, so
# clown's execve of the stub would fail ENOENT with the usual shebang).

setup() {
  load 'lib/common.bash'
  setup_test_home
  require_bin CLOWN_BIN clown

  STUB_BIN="$BATS_TEST_TMPDIR/bin"
  mkdir -p "$STUB_BIN"
  MUX_ARGV="$BATS_TEST_TMPDIR/zmx-argv"
  MUX_GROUP="$BATS_TEST_TMPDIR/zmx-group"
  # The stub records its argv AND the inherited CLOWN_GROUP_ID — clown exports
  # the resolved group-id onto its own env before the re-exec (RFC-0014 §2), so
  # syscall.Exec(os.Environ()) carries it into the multiplexer.
  cat >"$STUB_BIN/zmx" <<EOF
#!$(command -v bash)
printf '%s\n' "\$@" >"$MUX_ARGV"
printf '%s' "\${CLOWN_GROUP_ID-}" >"$MUX_GROUP"
exit 0
EOF
  chmod +x "$STUB_BIN/zmx"
  export PATH="$STUB_BIN:$PATH"
}

@test "clownfile [attach] wraps clown in the multiplexer with {id} + spliced {entry}" {
  cat >"$HOME/clownfile" <<'EOF'
[attach]
multiplexer = "zmx"
start = ["zmx", "attach", "{id}", "{entry}"]
EOF

  cd "$HOME"
  run env CLOWN_ATTACH_FORCE=1 CLOWN_SESSION_ID=test-sess-id \
    "$CLOWN_BIN" --provider claude -- --version
  [ "$status" -eq 0 ]
  [ -f "$MUX_ARGV" ]

  # The spliced {entry} is the RESOLVED argv (reexecArgv), not os.Args (clown#160):
  #   zmx attach test-sess-id <clownbin> --clown-attach-id test-sess-id \
  #       --provider claude -- --session-id <minted-uuid> --version
  # The explicit --provider is what suppresses the inner profile picker; the
  # --session-id decideClaudeSession injects on this fresh start rides in the
  # forwarded tail (a fresh UUID, so its exact value is not asserted).
  run cat "$MUX_ARGV"
  [ "${lines[0]}" = "attach" ]
  [ "${lines[1]}" = "test-sess-id" ]
  [[ "${lines[2]}" == *clown ]]            # clownExePath(), the spliced {entry}[0]
  [ "${lines[3]}" = "--clown-attach-id" ]
  [ "${lines[4]}" = "test-sess-id" ]       # id pinned as an arg (clown#136 hygiene)
  [ "${lines[5]}" = "--provider" ]
  [ "${lines[6]}" = "claude" ]
  [ "${lines[7]}" = "--" ]
  # forwarded tail: --session-id <uuid> --version (injected by decideClaudeSession)
  [ "${lines[8]}" = "--session-id" ]
  [ -n "${lines[9]}" ]                     # the minted UUID (value not asserted)
  [ "${lines[10]}" = "--version" ]
}

# --clown-attach=spawn (RFC-0014 §5) resolves the [attach].spawn template (with
# --detach) rather than start/resume, so the worker detaches and clown returns.
# NOTE: no CLOWN_ATTACH_FORCE here — spawn is a detached launch that is always
# non-interactive (bats has no PTY, matching the real `sc spawn` /dev/null-stdio
# context), and it MUST resolve its template regardless of TTY (clown#161). This
# test exercises the real non-TTY spawn path; a CLOWN_ATTACH_FORCE crutch would
# mask the gate exemption.
@test "clownfile [attach] spawn mode resolves the --detach spawn template" {
  cat >"$HOME/clownfile" <<'EOF'
[attach]
multiplexer = "zmx"
spawn = ["zmx", "attach", "{id}", "--detach", "{entry}"]
EOF

  cd "$HOME"
  run env CLOWN_SESSION_ID=spawn-sess \
    "$CLOWN_BIN" --clown-attach=spawn --provider claude -- --version
  [ "$status" -eq 0 ]
  [ -f "$MUX_ARGV" ]

  run cat "$MUX_ARGV"
  [ "${lines[0]}" = "attach" ]
  [ "${lines[1]}" = "spawn-sess" ]
  [ "${lines[2]}" = "--detach" ]          # the spawn template, not start/resume
  [[ "${lines[3]}" == *clown ]]            # the spliced {entry}[0]
  [ "${lines[4]}" = "--clown-attach-id" ] # loop guard pinned for the inner worker
  [ "${lines[5]}" = "spawn-sess" ]
}

# group-id is env-interpolated and exported as CLOWN_GROUP_ID (RFC-0014 §2), so
# the multiplexer (and the claude subtree / producers) inherit the resolved group.
@test "clownfile [attach] group-id interpolates SPINCLASS_SESSION_ID into CLOWN_GROUP_ID" {
  cat >"$HOME/clownfile" <<'EOF'
[attach]
multiplexer = "zmx"
group-id = "team-${SPINCLASS_SESSION_ID}"
start = ["zmx", "attach", "{id}", "{entry}"]
EOF

  cd "$HOME"
  run env CLOWN_ATTACH_FORCE=1 CLOWN_SESSION_ID=g-sess SPINCLASS_SESSION_ID=repo/branch \
    "$CLOWN_BIN" --provider claude -- --version
  [ "$status" -eq 0 ]
  [ -f "$MUX_GROUP" ]

  run cat "$MUX_GROUP"
  [ "${lines[0]}" = "team-repo/branch" ]  # ${SPINCLASS_SESSION_ID} composed into group-id
}
