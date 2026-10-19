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
  # The launcher binds the session's own XMPP credential; the caller's (often
  # a clown session) is not this test's business.
  unset TROUPE_XMPP_USER TROUPE_XMPP_PASSWORD_FILE
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
  [[ -n ${LISTENER_PID:-} ]] && kill "$LISTENER_PID" 2>/dev/null
  [[ -n ${WT:-} ]] && rm -rf "$WT"
}

# start_host_listeners opens, in the FRONTEND, a loopback TCP listener and an
# abstract unix socket, the two things the unit's private netns must hide.
start_host_listeners() {
  ABSTRACT="clown-agent-scope-bats-$$"
  python3 -I -c '
import os, socket, sys, threading
tcp = socket.socket(); tcp.bind(("127.0.0.1", 0)); tcp.listen()
ab = socket.socket(socket.AF_UNIX); ab.bind("\0" + sys.argv[2]); ab.listen()
with open(sys.argv[1], "w") as f: f.write(str(tcp.getsockname()[1]))
def serve(s):
    while True:
        c, _ = s.accept(); c.sendall(b"host-listener\n"); c.close()
threading.Thread(target=serve, args=(ab,), daemon=True).start()
serve(tcp)
' "$WT/port" "$ABSTRACT" &
  LISTENER_PID=$!
  for _ in $(seq 50); do
    [[ -s $WT/port ]] && break
    sleep 0.1
  done
  PORT="$(cat "$WT/port")"
  export ABSTRACT PORT
  # Sanity: both answer from the frontend.
  [[ "$(probe_tcp)" == *host-listener* ]] || return 1
  [[ "$(probe_abstract)" == *host-listener* ]] || return 1
}

# probe_tcp / probe_abstract print what the host listeners send, or nothing.
probe_tcp() {
  python3 -I -c 'import socket,sys; s=socket.create_connection(("127.0.0.1", int(sys.argv[1])), timeout=3); print(s.recv(64))' "$PORT" 2>/dev/null
}
probe_abstract() {
  python3 -I -c 'import socket,sys; s=socket.socket(socket.AF_UNIX); s.settimeout(3); s.connect("\0" + sys.argv[1]); print(s.recv(64))' "$ABSTRACT" 2>/dev/null
}

run_in_unit() {
  cd "$WT" || return 1
  "$CLOWN_BIN" agent-scope-run --spec "$WT/spec.json" -- bash -c "$1" </dev/null
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
  [ "$(grep -c '^FAIL' <<<"$output")" -eq 0 ]
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
  [ "$(grep -c 'tty_nr=0' <<<"$output")" -eq 0 ]
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

@test "agent_scope: an XMPP credential file that does not name its principal is refused (clown#267)" {
  cd "$WT"
  run env TROUPE_XMPP_USER=child TROUPE_XMPP_PASSWORD_FILE="$RT/troupe/accounts/parent.pass" \
    "$CLOWN_BIN" agent-scope-run --spec "$WT/spec.json" -- true
  echo "$output"
  [ "$status" -ne 0 ]
  grep -q "refusing to bind another session's credential" <<<"$output"
}

@test "agent_scope: host loopback services and abstract sockets are unreachable from inside" {
  start_host_listeners
  run run_in_unit "
    $(declare -f probe_tcp probe_abstract)
    PORT=$PORT ABSTRACT=$ABSTRACT
    case \$(probe_tcp) in *host-listener*) echo FAIL host-loopback-reachable ;; *) echo PASS host-loopback-unreachable ;; esac
    case \$(probe_abstract) in *host-listener*) echo FAIL abstract-reachable ;; *) echo PASS abstract-unreachable ;; esac"
  echo "$output"
  [ "$status" -eq 0 ]
  grep -q 'PASS host-loopback-unreachable' <<<"$output"
  grep -q 'PASS abstract-unreachable' <<<"$output"
  [ "$(grep -c '^FAIL' <<<"$output")" -eq 0 ]
}

@test "agent_scope: a forwarded loopback service is reachable from inside, on its own port" {
  start_host_listeners
  cd "$WT"
  run "$CLOWN_BIN" agent-scope-run --spec "$WT/spec.json" --forward "127.0.0.1:$PORT" -- bash -c "
    $(declare -f probe_tcp)
    PORT=$PORT
    case \$(probe_tcp) in *host-listener*) echo PASS forwarded-reachable ;; *) echo FAIL forwarded-unreachable ;; esac"
  echo "$output"
  [ "$status" -eq 0 ]
  grep -q 'PASS forwarded-reachable' <<<"$output"
}

@test "agent_scope: capabilities are reported before launch and echoed in the result" {
  run "$CLOWN_BIN" agent-scope-capabilities
  echo "$output"
  [ "$status" -eq 0 ]
  [ "$(jq -r .version <<<"$output")" = 1 ]
  jq -e '.capabilities | index("private-network") and index("frontend-mcp-sockets")' <<<"$output"
  caps="$output"
  run run_probe
  [ "$status" -eq 0 ]
  [ "$(jq -c .capabilities "$RESULT")" = "$(jq -c .capabilities <<<"$caps")" ]
}

@test "agent_scope: egress reaches allowlisted hosts through the proxy and nothing else" {
  if [[ -n ${CLOWN_AGENT_SCOPE_NO_NETWORK:-} ]]; then
    skip "CLOWN_AGENT_SCOPE_NO_NETWORK set"
  fi
  curl_store="$(readlink -f "$(command -v curl)" | cut -d/ -f1-4)"
  run run_in_unit '
    code=$(curl -s -o /dev/null -w "%{http_code}" -m 20 https://api.anthropic.com/)
    if [ "$code" != 000 ]; then echo "PASS model-api http=$code"; else echo FAIL model-api; fi
    if curl -s -o /dev/null -m 10 https://example.com/; then echo FAIL off-allowlist-reachable; else echo PASS off-allowlist-refused; fi
    if git ls-remote https://github.com/NixOS/nixpkgs.git refs/heads/master >/dev/null; then echo PASS git-https; else echo FAIL git-https; fi
    out=$(eval "$GIT_SSH_COMMAND -o BatchMode=yes -o PreferredAuthentications=none -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -T git@github.com" 2>&1)
    case $out in *"Permission denied"*) echo PASS git-ssh-reaches-forge ;; *) echo "FAIL git-ssh: $out" ;; esac
    if nix store info >/dev/null 2>&1; then echo PASS nix-daemon; else echo FAIL nix-daemon; fi
    if nix path-info --store https://cache.nixos.org '"$curl_store"' >/dev/null; then echo PASS nix-substituter; else echo FAIL nix-substituter; fi'
  echo "$output"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^FAIL' <<<"$output")" -eq 0 ]
  grep -c '^PASS' <<<"$output" | grep -qx 6
}
