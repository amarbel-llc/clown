# bats file_tags=agent_scope
#
# Host-side confinement test for the agent-scope launcher (clown#244,
# docs/features/0020-agent-scope-confinement.md). Excluded from the
# nix-sandbox bats lane (bats.nix hostOnlyTags): it starts real transient
# systemd USER services, which the builder sandbox has no manager for. Run via:
#
#     just verify-agent-scope
#
# Each test drives `clown agent-scope-run`, the same launcher `clown
# --agent-scope` uses for claude, with a probe script in place of the
# provider. Units are --collect'ed and --wait'ed; nothing outlives a test.

setup() {
  if [[ -z ${CLOWN_BIN:-} || ! -x $CLOWN_BIN ]]; then
    skip "CLOWN_BIN unset (run via 'just verify-agent-scope')"
  fi
  if ! systemctl --user show-environment >/dev/null 2>&1; then
    skip "no systemd user manager reachable"
  fi
  local root
  root="$(git rev-parse --show-toplevel)"
  mkdir -p "$root/.tmp"
  WT="$(mktemp -d "$root/.tmp/bats-agent-scope.XXXXXX")"
  RT="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
  # Outside the worktree: the unit could forge a result it can write.
  RESULT="$BATS_TEST_TMPDIR/agent-unit.json"
  export WT RT RESULT

  sockets='[]'
  if [[ -S ${SSH_AUTH_SOCK:-} ]]; then
    sockets="$(jq -n --arg p "$SSH_AUTH_SOCK" '[{path: $p, purpose: "piggy-agent"}]')"
  fi
  jq -n --arg wt "$WT" --arg home "$HOME" --arg res "$RESULT" --argjson sockets "$sockets" \
    --arg bus "$RT/bus" '{
      version: 1, worktree: $wt, operator_home: $home,
      unit_name_hint: "clown-agent-bats", result_path: $res,
      sockets: $sockets, deny_paths: [$bus],
      env: {SPINCLASS_PROBE: "yes"}
    }' >"$WT/spec.json"

  cat >"$WT/probe.sh" <<'EOF'
#!/usr/bin/env bash
chk() { if eval "$2" >/dev/null 2>&1; then echo "PASS $1"; else echo "FAIL $1"; fi; }
echo "cgroup=$(cut -d: -f3 /proc/self/cgroup)"
echo "home=$HOME"
chk real-home-hidden        "[ ! -e \"$REAL_HOME/.config\" ] && [ ! -e \"$REAL_HOME/.bashrc\" ]"
chk real-home-unwritable    "! touch \"$REAL_HOME/.agent-scope-probe\""
chk bus-absent              "[ ! -e \"$RT_PATH/bus\" ] && [ -z \"\${DBUS_SESSION_BUS_ADDRESS:-}\" ]"
chk busctl-fails            "! timeout 3 busctl --user status"
chk display-absent          "[ -z \"\${DISPLAY:-}\" ] && [ -z \"\${WAYLAND_DISPLAY:-}\" ] && ! ls \"$RT_PATH\"/wayland-* && [ ! -e /tmp/.X11-unix ]"
chk posh-absent             "[ ! -e \"$RT_PATH/posh\" ]"
chk devpts-empty            "[ -z \"\$(ls /dev/pts | grep -v ptmx)\" ]"
chk pidns-private           "[ \$(ls /proc | grep -c '^[0-9]') -lt 10 ]"
chk worktree-writable       "touch \"$WT_PATH/written-from-unit\""
chk spec-env                "[ \"\${SPINCLASS_PROBE:-}\" = yes ]"
if [ -t 0 ]; then
  echo "stdin-tty=yes tty_nr=$(cut -d' ' -f7 /proc/self/stat)"
else
  echo "stdin-tty=no"
fi
sleep 2
EOF
  chmod +x "$WT/probe.sh"
}

teardown() {
  [[ -n ${WT:-} ]] && rm -rf "$WT"
}

run_probe() {
  cd "$WT" || return 1
  "$CLOWN_BIN" agent-scope-run --spec "$WT/spec.json" -- \
    env REAL_HOME="$HOME" RT_PATH="$RT" WT_PATH="$WT" "$WT/probe.sh" </dev/null
}

@test "agent_scope: inside the unit, home, bus, display, ptys and other pids are unreachable" {
  run run_probe
  echo "$output"
  [ "$status" -eq 0 ]
  ! grep -q '^FAIL' <<<"$output"
  grep -q '^PASS real-home-hidden' <<<"$output"
  grep -q '^PASS bus-absent' <<<"$output"
  grep -q '^PASS display-absent' <<<"$output"
  grep -q '^PASS devpts-empty' <<<"$output"
  grep -q '^PASS pidns-private' <<<"$output"
  [ -e "$WT/written-from-unit" ]
  [ ! -e "$HOME/.agent-scope-probe" ]
}

@test "agent_scope: the result names the unit and a cgroup under clown-agent.slice that holds the probe" {
  run run_probe
  echo "$output"
  [ "$status" -eq 0 ]
  [ -f "$RESULT" ]
  cat "$RESULT"
  unit="$(jq -r .unit "$RESULT")"
  cgroup="$(jq -r .cgroup "$RESULT")"
  [ "$(jq -r '.error // empty' "$RESULT")" = "" ]
  [[ $unit == clown-agent-bats-*.service ]]
  [[ $cgroup == */clown-agent.slice/"$unit" ]]
  [ "$(jq -r .uid "$RESULT")" = "$(id -u)" ]
  grep -qx "cgroup=$cgroup" <<<"$output"
}

@test "agent_scope: piggy-agent and the frontend are outside the unit's cgroup" {
  run run_probe
  [ "$status" -eq 0 ]
  cgroup="$(jq -r .cgroup "$RESULT")"
  frontend="$(cut -d: -f3 /proc/self/cgroup)"
  [[ $frontend != "$cgroup"* ]]
  piggy="$(systemctl --user show -p ControlGroup --value piggy-agent.service 2>/dev/null || true)"
  if [[ -z $piggy ]]; then
    skip "piggy-agent.service not running on this host (frontend check passed)"
  fi
  echo "unit=$cgroup piggy=$piggy"
  [[ $piggy != "$cgroup"* ]]
}

@test "agent_scope: the unit does not outlive the launcher" {
  run run_probe
  [ "$status" -eq 0 ]
  unit="$(jq -r .unit "$RESULT")"
  run systemctl --user is-active "$unit"
  [ "$output" != "active" ]
}

@test "agent_scope: an interactive launch relays a fresh pty that becomes the in-unit controlling tty" {
  if ! command -v script >/dev/null 2>&1; then
    skip "script(1) not on PATH"
  fi
  cd "$WT"
  run script -qec "'$CLOWN_BIN' agent-scope-run --spec '$WT/spec.json' -- env REAL_HOME='$HOME' RT_PATH='$RT' WT_PATH='$WT' '$WT/probe.sh'" /dev/null </dev/null
  echo "$output"
  [ "$status" -eq 0 ]
  grep -q 'stdin-tty=yes' <<<"$output"
  ! grep -q 'tty_nr=0' <<<"$output"
  pty="$(jq -r .pty "$RESULT")"
  [[ $pty == /dev/pts/* ]]
}

@test "agent_scope: a spec exposing the operator home is refused before any unit starts" {
  jq --arg h "$HOME" '.binds_rw = [$h] | del(.result_path)' "$WT/spec.json" >"$WT/bad.json"
  run "$CLOWN_BIN" agent-scope-run --spec "$WT/bad.json" -- true
  echo "$output"
  [ "$status" -ne 0 ]
  grep -q 'exposes the whole operator home' <<<"$output"
}
