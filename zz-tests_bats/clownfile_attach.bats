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
  cat >"$STUB_BIN/zmx" <<EOF
#!$(command -v bash)
printf '%s\n' "\$@" >"$MUX_ARGV"
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

  # The stub records argv[1:]; expected full mux argv is:
  #   zmx attach test-sess-id <clownbin> --clown-attach-id test-sess-id \
  #       --provider claude -- --version
  run cat "$MUX_ARGV"
  [ "${lines[0]}" = "attach" ]
  [ "${lines[1]}" = "test-sess-id" ]
  [[ "${lines[2]}" == *clown ]]            # clownExePath(), the spliced {entry}[0]
  [ "${lines[3]}" = "--clown-attach-id" ]
  [ "${lines[4]}" = "test-sess-id" ]       # id pinned as an arg (clown#136 hygiene)
  [ "${lines[5]}" = "--provider" ]
  [ "${lines[6]}" = "claude" ]
  [ "${lines[7]}" = "--" ]
  [ "${lines[8]}" = "--version" ]
}
