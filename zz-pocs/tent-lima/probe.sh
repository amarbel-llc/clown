#!/usr/bin/env bash
# zz-pocs/tent-lima/probe.sh
#
# End-to-end smoke for running clown's tent claude inside Lima.
#
# Steps:
#   1. Verify Lima is on PATH.
#   2. limactl create + start (if not already running). The yaml
#      configures mounts (/nix/store, /nix/var, /etc/nix, $HOME)
#      and ssh.forwardAgent.
#   3. Copy the tent OCI tarball into the VM.
#   4. nerdctl load it.
#   5. Discover the in-tent claude binary path from the running
#      container (by listing the /nix/store entry for claude-code).
#   6. Run `nerdctl run --rm clown-tent:<tag> <claude> -p hello` with
#      mounts mirroring clown's tent argv.
#   7. Verify claude produced output and exited 0.
#
# Exits 0 on success. On failure, prints the failing step and exits
# non-zero so the calling derivation/runner fails.
#
# Env (set by the calling flake's writeShellApplication wrapper):
#   LIMA_INSTANCE     instance name (default: clown-tent-lima)
#   LIMA_YAML         path to lima.yaml
#   TENT_IMAGE        /nix/store/... path to clown-tent.tar.gz
#   TENT_CLAUDE_BIN   /nix/store/...-claude-code-.../bin/claude
#                     (LINUX variant; resolved by the flake from
#                     llm-agents.packages.<linux-system>.claude-code)

set -euo pipefail

: "${LIMA_INSTANCE:=clown-tent-lima}"
: "${LIMA_YAML:?LIMA_YAML must be set}"
: "${TENT_IMAGE:?TENT_IMAGE must be set}"
: "${TENT_CLAUDE_BIN:?TENT_CLAUDE_BIN must be set}"

step() {
    echo
    echo "═══ $* ═══" >&2
}

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

# ─────────────────────────────────────────────────────────────────────
step "1. preflight"

command -v limactl >/dev/null || fail "limactl not on PATH"
test -f "$LIMA_YAML"     || fail "lima yaml not found at $LIMA_YAML"
test -e "$TENT_IMAGE"    || fail "tent image not found at $TENT_IMAGE"

echo ">> limactl: $(command -v limactl)"
echo ">> version: $(limactl --version | head -1)"

# ─────────────────────────────────────────────────────────────────────
step "2. ensure $LIMA_INSTANCE is running"

state="$(limactl list --json 2>/dev/null | jq -r --arg n "$LIMA_INSTANCE" 'select(.name == $n) | .status' || true)"

if [[ -z "$state" ]]; then
    echo ">> $LIMA_INSTANCE doesn't exist; creating from $LIMA_YAML"
    limactl create --name="$LIMA_INSTANCE" --tty=false "$LIMA_YAML"
fi

state="$(limactl list --json 2>/dev/null | jq -r --arg n "$LIMA_INSTANCE" 'select(.name == $n) | .status' || true)"
if [[ "$state" != "Running" ]]; then
    echo ">> starting $LIMA_INSTANCE (current state: ${state:-unknown})"
    limactl start --tty=false "$LIMA_INSTANCE"
fi

echo ">> $LIMA_INSTANCE is running"

# ─────────────────────────────────────────────────────────────────────
step "3. load tent image into containerd"

# Strategy: copy the image tarball into the VM via the /nix/store
# bind-mount (it's already visible inside the VM), then `nerdctl
# load -i` it. `sudo` because nerdctl uses the system containerd by
# default; the lima.yaml has `containerd.system: true`.
in_vm_image_path="$TENT_IMAGE"
echo ">> in-VM image path: $in_vm_image_path"

# Verify the image is visible inside the VM via the /nix/store mount.
if ! limactl shell "$LIMA_INSTANCE" -- test -e "$in_vm_image_path"; then
    fail "in-VM /nix/store does not contain $in_vm_image_path"
fi

# Load.
echo ">> loading via nerdctl..."
limactl shell "$LIMA_INSTANCE" -- sudo nerdctl load -i "$in_vm_image_path"

# Pull out the tag from nerdctl's image list (matches `clown-tent:*`).
image_ref="$(limactl shell "$LIMA_INSTANCE" -- sudo nerdctl images --format '{{.Repository}}:{{.Tag}}' \
              | grep '^clown-tent:' | head -1 || true)"
if [[ -z "$image_ref" ]]; then
    fail "clown-tent image not found in nerdctl image list after load"
fi
echo ">> loaded: $image_ref"

# ─────────────────────────────────────────────────────────────────────
step "4. verify in-container claude binary"

# We don't `ls` to discover — the parent flake bakes the exact linux
# claude path into the wrapper as TENT_CLAUDE_BIN. /nix/store is
# bind-mounted into the VM, so this path is reachable inside any
# container with `-v /nix/store:/nix/store:ro`.
#
# Empirical gotcha (verified during this spike): there are MULTIPLE
# claude-code-* derivations under /nix/store on a typical clown
# install — at minimum a darwin-format 2.1.111 (patched, for
# un-tented clown) and the linux-format 2.1.150 (for tent). A naive
# `ls /nix/store/*-claude-code-*/bin/claude | head -1` would pick
# the wrong one alphabetically and produce `exec format error`.
claude_bin="$TENT_CLAUDE_BIN"
if ! limactl shell "$LIMA_INSTANCE" -- test -x "$claude_bin"; then
    fail "$claude_bin is not executable inside the VM (check /nix/store mount)"
fi
echo ">> claude: $claude_bin"

# ─────────────────────────────────────────────────────────────────────
step "5. run claude -p hello inside the tent"

# Match clown's tent argv shape: /nix/store ro, $HOME rw, $SSH_AUTH_SOCK
# (in-VM path published by Lima's ssh.forwardAgent at /run/host-services
# /ssh-auth.sock), .claude bind-mount for credentials.
ssh_in_vm="/run/host-services/ssh-auth.sock"

set +e
output="$(limactl shell "$LIMA_INSTANCE" -- sudo nerdctl run --rm -i \
    -v /nix/store:/nix/store:ro \
    -v "$HOME/.claude":"$HOME/.claude" \
    -v "$HOME/.claude.json":"$HOME/.claude.json" \
    -v "$ssh_in_vm:$ssh_in_vm" \
    -e "HOME=$HOME" \
    -e "USER=$USER" \
    -e "SSH_AUTH_SOCK=$ssh_in_vm" \
    "$image_ref" \
    "$claude_bin" -p hello 2>&1)"
exit_code=$?
set -e

echo "─── claude output ─────────────────────────────"
echo "$output"
echo "───────────────────────────────────────────────"
echo ">> exit code: $exit_code"

if [[ $exit_code -ne 0 ]]; then
    fail "claude exited non-zero ($exit_code)"
fi
if [[ -z "$output" ]]; then
    fail "claude produced no output"
fi
if echo "$output" | grep -qiE 'not logged in|please run /login'; then
    fail "claude isn't authenticated; run \`just debug-extract-claude-credentials\` first"
fi

# ─────────────────────────────────────────────────────────────────────
step "PASS: tent-lima end-to-end smoke succeeded"

echo
echo "Lima instance left running. Stop with:"
echo "  limactl stop $LIMA_INSTANCE"
echo
echo "Or destroy entirely:"
echo "  limactl delete --force $LIMA_INSTANCE"
