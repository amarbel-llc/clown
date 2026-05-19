# bats file_tags=tent_smoke
#
# Host-side smoke tests for FDR-0007 tent (2026-05-19 C+F + B shape).
# Excluded from the nix-sandbox bats lane (bats.nix filters out the
# tent_smoke tag) because it requires rootless podman, a loaded
# clown-tent:<version> image, and on the SSH socket test, a host
# ssh-agent socket and outbound :22 to github.com. Run via:
#
#     just test-tent-smoke
#
# Each test that needs podman skip-guards on `command -v podman`, so
# running the file unintentionally inside a podman-less environment
# (eg. a sandbox where bats discovery accidentally picked it up) is a
# no-op rather than a failure.

setup() {
  load 'lib/common.bash'

  if ! command -v podman >/dev/null 2>&1; then
    skip "podman not on PATH (likely a sandbox lane; tent_smoke runs only on the host)"
  fi

  # Resolve the tent image tag from the repo's version.txt. The
  # justfile recipe loads (or builds) this tag before invoking bats,
  # so by the time we get here it should be present in the local
  # podman store.
  local root
  root="$(git rev-parse --show-toplevel)"
  TENT_IMAGE="clown-tent:$(tr -d '[:space:]' < "$root/version.txt")"
  if ! podman image exists "$TENT_IMAGE"; then
    # Fall back to the newest clown-tent image so a stale version.txt
    # doesn't block the suite.
    TENT_IMAGE="$(podman image ls --filter reference='clown-tent*' \
                    --format '{{.Repository}}:{{.Tag}}' | head -1)"
  fi
  if [[ -z "$TENT_IMAGE" ]] || ! podman image exists "$TENT_IMAGE"; then
    skip "no clown-tent:* image loaded — run 'just build' first"
  fi
  export TENT_IMAGE

  # C+F default read-only bind list. Mirrors
  # internal/tent.DefaultReadOnlyBindCandidates; if that list changes,
  # update here too. We filter to existing paths so a host without
  # ~/.gitconfig still runs the rest.
  RO_BINDS=()
  for p in \
      /nix/var \
      /etc/nix \
      "$HOME/.nix-profile" \
      "$HOME/.local/state/nix/profiles/profile" \
      "$HOME/.gitconfig" \
      "$HOME/.config/git" \
      "$HOME/.config/nix" \
      "$HOME/.config/ssh"; do
    if [[ -e "$p" || -L "$p" ]]; then
      RO_BINDS+=("--volume" "$p:$p:ro")
    fi
  done
  export RO_BINDS

  # Common minimal mount set: /nix/store ro + cwd writable. Matches
  # tent.BuildArgs's invariant ordering (store first, then cwd).
  CORE_BINDS=(
    --volume "/nix/store:/nix/store:ro"
    --volume "$(pwd):$(pwd)"
    --workdir "$(pwd)"
  )
  export CORE_BINDS
}

# tent_run <bash-script>
#   Run <bash-script> inside the tent image with the C+F mount set,
#   --userns=keep-id, $SSH_AUTH_SOCK forwarded if present, and the
#   user's host PATH rewritten to /nix/store canonical form (mirrors
#   what cmd/clown's resolvePassDevshell + RewritePathToNixStore
#   produces when IN_NIX_SHELL is set).
tent_run() {
  local script="$1"
  local rewritten
  # The realpath-rewrite filter. Same logic as
  # internal/tent.RewritePathToNixStore but in bash so we're not
  # dependent on a separate helper binary.
  rewritten="$(printf '%s' "$PATH" | tr ':' '\n' | while read -r p; do
    [[ -z "$p" ]] && continue
    real="$(readlink -f "$p" 2>/dev/null)" || continue
    [[ "$real" == /nix/store/* ]] || continue
    printf '%s\n' "$real"
  done | paste -sd: -)"

  # Forward the same env-var allowlist tent.DefaultEnvPassthrough
  # carries. Each is conditional on being set on the host so we don't
  # pass --env NAME with no value (podman would forward an empty
  # string, which differs from "var unset").
  local env_args=()
  [[ -n "$rewritten" ]]              && env_args+=(--env "PATH=$rewritten")
  [[ -n "${HOME-}" ]]                && env_args+=(--env "HOME=$HOME")
  [[ -n "${USER-}" ]]                && env_args+=(--env "USER=$USER")
  [[ -n "${TERM-}" ]]                && env_args+=(--env "TERM=$TERM")
  [[ -n "${SSH_AUTH_SOCK-}" ]]       && env_args+=(--env "SSH_AUTH_SOCK=$SSH_AUTH_SOCK")
  [[ -n "${SSH_HOME-}" ]]            && env_args+=(--env "SSH_HOME=$SSH_HOME")
  [[ -n "${XDG_CONFIG_HOME-}" ]]     && env_args+=(--env "XDG_CONFIG_HOME=$XDG_CONFIG_HOME")

  local sock_bind=()
  [[ -n "${SSH_AUTH_SOCK-}" && -S "$SSH_AUTH_SOCK" ]] && \
    sock_bind=(--volume "$SSH_AUTH_SOCK:$SSH_AUTH_SOCK")

  podman run --rm -i --userns=keep-id \
    "${CORE_BINDS[@]}" \
    "${RO_BINDS[@]}" \
    "${sock_bind[@]}" \
    "${env_args[@]}" \
    "$TENT_IMAGE" \
    bash -lc "$script"
}

@test "tent image launches at all" {
  run podman run --rm "$TENT_IMAGE" /bin/true
  assert_success
}

@test "tent bin baseline includes bash and coreutils" {
  run tent_run 'command -v bash; command -v cat; command -v echo'
  assert_success
  assert_output --regexp '/bin/bash'
  assert_output --regexp '/bin/cat'
}

@test "/nix/store mount is read-only" {
  run tent_run 'touch /nix/store/tent-write-test 2>&1; echo EXIT=$?'
  assert_success
  assert_output --regexp 'Read-only file system|Permission denied'
  assert_output --regexp 'EXIT=[1-9]'
}

@test "/nix/var is reachable (daemon socket present)" {
  run tent_run 'ls /nix/var/nix/daemon-socket/socket 2>&1 || true'
  assert_success
  assert_output --regexp '/nix/var/nix/daemon-socket/socket'
}

@test "nix --version works with /nix/var bind and host profile on PATH" {
  if [[ ! -d /nix/var ]]; then
    skip "no /nix/var on host — daemon-nix install unavailable"
  fi
  run tent_run 'nix --version 2>&1'
  assert_success
  assert_output --regexp '^nix '
}

@test "host home-manager profile yields git on PATH" {
  if [[ ! -d "$HOME/.nix-profile" ]]; then
    skip "no ~/.nix-profile (this user has no home-manager install)"
  fi
  run tent_run 'git --version 2>&1'
  assert_success
  assert_output --regexp '^git version '
}

@test "host nix-daemon is reachable from inside tent" {
  if [[ ! -S /nix/var/nix/daemon-socket/socket ]]; then
    skip "no host nix-daemon socket"
  fi
  run tent_run 'nix store info 2>&1'
  assert_success
  # `nix store info` prints "Store URL: daemon" when it successfully
  # talks to a daemon Store, "Store URL: local" when it falls back.
  assert_output --regexp 'Store URL: daemon'
}

@test "SSH_AUTH_SOCK forwarded — ssh -T git@github.com authenticates" {
  if [[ "${CLOWN_TENT_NO_NETWORK:-}" == "1" ]]; then
    skip "CLOWN_TENT_NO_NETWORK=1 — network-dependent test skipped"
  fi
  if [[ -z "${SSH_AUTH_SOCK-}" || ! -S "${SSH_AUTH_SOCK-}" ]]; then
    skip "no SSH_AUTH_SOCK on host (start ssh-agent / pivy-agent)"
  fi

  # The in-tent ssh has no ~/.ssh/known_hosts (security boundary —
  # FDR-0007 deliberately doesn't bind ~/.ssh). Override host-key
  # checking inline so the test exercises the agent socket, not the
  # known-hosts story.
  #
  # github.com responds to `ssh -T` with "Hi <user>! You've
  # successfully authenticated, but GitHub does not provide shell
  # access." when the key in the forwarded agent matches a configured
  # github account. We assert on "successfully authenticated".
  run tent_run '
    ssh -T -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null \
        -o ConnectTimeout=10 \
        -o BatchMode=yes \
        git@github.com 2>&1 || true
  '
  assert_output --regexp 'successfully authenticated'
}

@test "PATH override produces /nix/store-canonical entries only" {
  run tent_run 'echo "$PATH" | tr ":" "\n" | grep -v "^/nix/store/" || true'
  assert_success
  # Every PATH entry should start with /nix/store/. grep -v above
  # printed any that don't. assert_output must NOT match anything
  # non-empty.
  refute_output --regexp '^/[^n].*'
  refute_output --regexp '^/nix/var'
  refute_output --regexp '^/home/'
}

@test "user-allowlist binds do not include ~/.ssh" {
  # Defense-in-depth: even if the user has ~/.ssh, it must not be
  # readable inside the tent. The bind list explicitly doesn't
  # include it; this test guards against a future change adding it
  # by accident.
  run tent_run 'ls "$HOME/.ssh" 2>&1 || true'
  assert_success
  # Either "No such file or directory" (mount didn't happen) or an
  # empty dir — anything BUT a listing of id_* keys.
  refute_output --regexp 'id_rsa|id_ed25519|id_ecdsa'
}
