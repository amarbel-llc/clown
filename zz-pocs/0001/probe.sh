#!/usr/bin/env bash
# Probe what __impure does NOT permit. Each test records "BLOCKED" or
# "ALLOWED" to $1/results.txt with the probe name and the observed behavior.
#
# The derivation succeeds even if individual probes find no confinement —
# we're collecting data, not asserting. Read $out/results.txt after build.

OUT="$1"
RESULTS="$OUT/results.txt"
: >"$RESULTS"

record() {
  local name="$1"
  local verdict="$2"
  local detail="${3:-}"
  printf '%-40s %s' "$name" "$verdict" >>"$RESULTS"
  [[ -n $detail ]] && printf '  -- %s' "$detail" >>"$RESULTS"
  printf '\n' >>"$RESULTS"
}

# Probe: can we write outside $out? We try a few places. Each should fail
# (BLOCKED = good, ALLOWED = no filesystem confinement for this path).

probe_write() {
  local target="$1"
  local name="$2"
  if echo "probed by zz-pocs/0001" >"$target" 2>/dev/null; then
    record "$name" "ALLOWED" "wrote $target"
    rm -f "$target" 2>/dev/null || true
  else
    record "$name" "BLOCKED"
  fi
}

probe_write "/tmp/zz-pocs-0001-probe" "write_to_slash_tmp"
probe_write "$HOME/.zz-pocs-0001-probe" "write_to_home"
probe_write "/private/tmp/zz-pocs-0001-probe" "write_to_private_tmp_darwin"
probe_write "/Users/sfriedenberg/.zz-pocs-0001-probe" "write_to_real_user_home"
probe_write "/var/tmp/zz-pocs-0001-probe" "write_to_var_tmp"
probe_write "/usr/local/zz-pocs-0001-probe" "write_to_usr_local"

# Probe: can we READ sensitive files outside $out?

probe_read() {
  local target="$1"
  local name="$2"
  if [[ -r $target ]] && head -c 16 "$target" >/dev/null 2>&1; then
    record "$name" "ALLOWED" "read $target"
  else
    record "$name" "BLOCKED"
  fi
}

probe_read "/etc/hosts" "read_etc_hosts"
probe_read "$HOME/.ssh/id_rsa" "read_home_ssh_private_key"
probe_read "/Users/sfriedenberg/.ssh/id_rsa" "read_real_user_ssh_private_key"
probe_read "/etc/passwd" "read_etc_passwd"
probe_read "$HOME/.config/git/config" "read_home_git_config"

# Probe: what's $HOME inside here, and what's in it?
record "observed_HOME" "INFO" "$HOME"
record "observed_PWD" "INFO" "$(pwd)"
record "observed_USER" "INFO" "${USER:-unset}"
record "observed_USERHOME" "INFO" "$(ls -la "$HOME" 2>&1 | head -5 | tr '\n' ';' | sed 's/;$//')"

# Probe: can we enumerate /Users/sfriedenberg?
if ls /Users/sfriedenberg >/dev/null 2>&1; then
  record "list_real_user_home" "ALLOWED" "$(ls /Users/sfriedenberg 2>/dev/null | head -5 | tr '\n' ',' | sed 's/,$//')"
else
  record "list_real_user_home" "BLOCKED"
fi

# Probe: curl availability and network egress. Capture stderr so we can
# distinguish "binary missing" from "network blocked".
if command -v curl >/dev/null 2>&1; then
  record "curl_available" "ALLOWED" "$(curl --version | head -1)"

  curl_out=$(curl -sS --max-time 10 -o /dev/null -w "http_code=%{http_code} errno=%{errormsg}" https://example.com 2>&1 || true)
  if [[ $curl_out == *"http_code=200"* ]]; then
    record "curl_https_example_com" "ALLOWED" "$curl_out"
  else
    record "curl_https_example_com" "BLOCKED" "$curl_out"
  fi

  # Plain HTTP to a well-known host.
  curl_out=$(curl -sS --max-time 10 -o /dev/null -w "http_code=%{http_code}" http://neverssl.com 2>&1 || true)
  record "curl_http_neverssl_com" "?" "$curl_out"

  # Loopback: can we reach 127.0.0.1 if something were listening? We can't
  # know without a listener, but we can observe connection-refused vs. a
  # harder block. Use a port that's definitely nothing.
  curl_out=$(curl -sS --max-time 3 -o /dev/null -w "errno=%{errormsg}" http://127.0.0.1:34567/ 2>&1 || true)
  record "curl_loopback_34567" "?" "$curl_out"
else
  record "curl_available" "BLOCKED" "binary not on PATH"
fi

# Probe: raw TCP outbound, bypassing curl/TLS. Uses bash's /dev/tcp
# pseudo-file. If this connects, the sandbox permits outbound TCP. If it
# times out, the sandbox is blocking outbound regardless of protocol.
raw_tcp_probe() {
  local host="$1"
  local port="$2"
  local name="$3"
  local out
  out=$(
    {
      timeout 5 bash -c "
        exec 3<>/dev/tcp/${host}/${port} || exit 1
        printf 'GET / HTTP/1.0\r\nHost: ${host}\r\n\r\n' >&3
        head -c 64 <&3
      " 2>&1
    } || echo "TIMEOUT_OR_FAIL"
  )
  if [[ $out == *"HTTP/"* ]]; then
    record "$name" "ALLOWED" "got: $(echo "$out" | head -1)"
  else
    record "$name" "BLOCKED" "$out"
  fi
}

raw_tcp_probe "1.1.1.1" 80 "raw_tcp_cloudflare"
raw_tcp_probe "127.0.0.1" 80 "raw_tcp_loopback_80"

# Probe: connect to the host-side listener started by run.sh. This is the
# definitive test of whether the egress-broker-on-loopback pattern works
# from inside an __impure Nix builder.
if [[ -n ${CLOWN_PROBE_LOOPBACK_PORT:-} && -n ${CLOWN_PROBE_LOOPBACK_BANNER:-} ]]; then
  record "probe_loopback_port" "INFO" "$CLOWN_PROBE_LOOPBACK_PORT"
  record "probe_loopback_banner_expected" "INFO" "$CLOWN_PROBE_LOOPBACK_BANNER"

  # Via curl.
  curl_out=$(curl -sS --max-time 5 "http://127.0.0.1:$CLOWN_PROBE_LOOPBACK_PORT/" 2>&1 || echo "CURL_FAILED")
  if [[ $curl_out == *"$CLOWN_PROBE_LOOPBACK_BANNER"* ]]; then
    record "curl_to_host_listener" "ALLOWED" "banner received"
  else
    record "curl_to_host_listener" "BLOCKED" "$curl_out"
  fi

  # Via raw TCP.
  tcp_out=$(
    {
      timeout 5 bash -c "
        exec 3<>/dev/tcp/127.0.0.1/${CLOWN_PROBE_LOOPBACK_PORT} || exit 1
        printf 'GET / HTTP/1.0\r\nHost: 127.0.0.1\r\n\r\n' >&3
        head -c 256 <&3
      " 2>&1
    } || echo "RAW_TCP_FAILED"
  )
  if [[ $tcp_out == *"$CLOWN_PROBE_LOOPBACK_BANNER"* ]]; then
    record "raw_tcp_to_host_listener" "ALLOWED" "banner received"
  else
    record "raw_tcp_to_host_listener" "BLOCKED" "$tcp_out"
  fi
else
  record "loopback_listener_probes" "SKIPPED" "CLOWN_PROBE_LOOPBACK_* env not set"
fi

# Probe: can we spawn a listener on loopback?
if command -v nc >/dev/null 2>&1; then
  record "nc_available" "ALLOWED" "$(which nc)"
  if timeout 2 nc -l 34567 </dev/null >/dev/null 2>&1; then
    record "nc_listen_34567" "ALLOWED"
  else
    record "nc_listen_34567" "BLOCKED"
  fi
else
  record "nc_available" "BLOCKED" "binary not on PATH"
fi

echo "=== $RESULTS ==="
cat "$RESULTS"
echo "=== end ==="
