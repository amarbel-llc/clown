#!/usr/bin/env bash
# Drives the probe POC:
#   1. Starts a host-side TCP listener on a loopback port that echoes a banner
#      back to whoever connects. Runs in the background.
#   2. Writes the port to a file the derivation reads at build time.
#   3. nix build the probe derivation. The probe inside the sandbox tries to
#      connect to 127.0.0.1:<port> and grab the banner.
#   4. Tears the listener down.

set -euo pipefail

cd "$(dirname "$0")"

rm -f result

# Pick a port somewhat unlikely to collide with user services.
PORT=53712
BANNER="hello-from-host-$(date -u +%s)"

LISTENER_LOG="$(mktemp -t zz-pocs-0001-listener.XXXXXX)"

cleanup() {
  local pid="${LISTENER_PID:-}"
  if [[ -n $pid ]]; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
}
trap cleanup EXIT

echo "=== host-side listener on 127.0.0.1:$PORT ==="
# `nc -l` on BSD netcat (macOS) spawns a single-shot listener. We need it to
# accept multiple connections across the build, so use a loop. `-k` (keep-alive)
# would be ideal but behavior varies; a shell loop is portable.
(
  while true; do
    printf 'HTTP/1.0 200 OK\r\nContent-Length: %d\r\n\r\n%s\n' \
      $((${#BANNER} + 1)) "$BANNER" |
      nc -l 127.0.0.1 "$PORT" >/dev/null 2>&1 || true
  done
) >"$LISTENER_LOG" 2>&1 &
LISTENER_PID=$!
sleep 0.5

# Verify the listener is accepting host-side before we kick off nix build.
if ! curl -sS --max-time 2 "http://127.0.0.1:$PORT/" | grep -q "$BANNER"; then
  echo "host-side listener check FAILED — banner not observed via curl" >&2
  exit 1
fi
echo "host-side listener is up (banner: $BANNER)"

# Pass port + banner through the nix-eval env via builtins.getEnv in the
# flake. --impure enables that. We bake these into the builder's env as
# derivation attrs (not impureEnvVars — that mechanism didn't propagate
# reliably on this darwin setup).
export CLOWN_PROBE_LOOPBACK_PORT="$PORT"
export CLOWN_PROBE_LOOPBACK_BANNER="$BANNER"

echo
echo "=== nix build ==="
nix build \
  --impure \
  --print-build-logs \
  --no-link \
  --print-out-paths \
  .#default |
  tee /tmp/zz-pocs-0001-outpath

OUTPATH="$(cat /tmp/zz-pocs-0001-outpath)"

echo
echo "=== $OUTPATH/results.txt ==="
cat "$OUTPATH/results.txt"
