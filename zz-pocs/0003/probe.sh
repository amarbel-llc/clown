#!/usr/bin/env bash
# Probes the broker composition inside the Nix builder.
#
# Four probes:
#   1. HTTPS to an ALLOWLISTED host through the broker → expect 200 + body.
#   2. HTTPS to a DENIED host through the broker → expect 403 (or proxy error).
#   3. HTTP (plain) to the allowlisted host through the broker — exercises
#      forward-proxy path (vs CONNECT tunneling).
#   4. Bypass: --noproxy to a denied host → expect 200. Demonstrates the
#      advisory-by-convention limit from ADR-0005.
#
# Each probe records a line to $out/results.txt.

set -uo pipefail

OUT="$1"
RESULTS="$OUT/results.txt"
: >"$RESULTS"

record() {
  local name="$1"
  local verdict="$2"
  local detail="${3:-}"
  printf '%-44s %s' "$name" "$verdict" >>"$RESULTS"
  [[ -n $detail ]] && printf '  -- %s' "$detail" >>"$RESULTS"
  printf '\n' >>"$RESULTS"
}

# Fail fast if the broker env isn't set.
if [[ -z "${CLOWN_BROKER_HOST:-}" || -z "${CLOWN_BROKER_PORT:-}" || -z "${CLOWN_BROKER_CA_PEM:-}" ]]; then
  record "env_setup" "MISSING" "CLOWN_BROKER_{HOST,PORT,CA_PEM} not all set"
  echo "=== $RESULTS ==="
  cat "$RESULTS"
  echo "=== end ==="
  exit 0
fi

BROKER="http://${CLOWN_BROKER_HOST}:${CLOWN_BROKER_PORT}"
record "broker_endpoint" "INFO" "$BROKER"
record "broker_ca_path" "INFO" "$CLOWN_BROKER_CA_PEM"

# Verify the CA file is actually in the closure.
if [[ -r "$CLOWN_BROKER_CA_PEM" ]]; then
  record "ca_readable" "OK" "$(wc -l <"$CLOWN_BROKER_CA_PEM" | tr -d ' ') lines"
else
  record "ca_readable" "FAIL" "cannot read $CLOWN_BROKER_CA_PEM"
fi

export HTTPS_PROXY="$BROKER"
export HTTP_PROXY="$BROKER"
export CURL_CA_BUNDLE="$CLOWN_BROKER_CA_PEM"

# Probe 1: HTTPS to allowlisted host.
code=$(curl -sS -o "$OUT/probe1-body.txt" -w "%{http_code}" --max-time 10 \
  "https://httpbun.com/get" 2>&1 || echo "ERR")
if [[ "$code" == "200" ]]; then
  record "allow_https_httpbun_get" "ALLOWED" "http_code=200"
else
  record "allow_https_httpbun_get" "UNEXPECTED" "code=$code"
fi

# Probe 2: HTTPS to denied host, expect 403 from broker.
code=$(curl -sS -o "$OUT/probe2-body.txt" -w "%{http_code}" --max-time 10 \
  "https://example.com/" 2>&1 || echo "ERR")
if [[ "$code" == "403" ]]; then
  record "deny_https_example_com" "BLOCKED" "http_code=403 (broker)"
elif [[ "$code" == "200" ]]; then
  record "deny_https_example_com" "UNEXPECTED" "reached upstream (broker not enforcing)"
else
  record "deny_https_example_com" "BLOCKED" "code=$code"
fi

# Probe 3: plain HTTP via forward-proxy path.
code=$(curl -sS -o "$OUT/probe3-body.txt" -w "%{http_code}" --max-time 10 \
  "http://httpbun.com/get" 2>&1 || echo "ERR")
if [[ "$code" == "200" ]]; then
  record "allow_http_httpbun_get" "ALLOWED" "http_code=200"
else
  record "allow_http_httpbun_get" "UNEXPECTED" "code=$code"
fi

# Probe 4: bypass via --noproxy. Demonstrates the advisory-by-convention
# limit. A misbehaving subagent can route around the broker; cooperative
# threat model accepts this.
code=$(curl -sS -o "$OUT/probe4-body.txt" -w "%{http_code}" --max-time 10 \
  --noproxy '*' "http://example.com/" 2>&1 || echo "ERR")
if [[ "$code" == "200" ]]; then
  record "bypass_noproxy_example_com" "ALLOWED" "reached upstream (expected; advisory limit)"
elif [[ "$code" == "000" ]]; then
  record "bypass_noproxy_example_com" "BLOCKED" "$code — sandbox denying direct egress"
else
  record "bypass_noproxy_example_com" "OTHER" "code=$code"
fi

echo "=== $RESULTS ==="
cat "$RESULTS"
echo "=== end ==="
