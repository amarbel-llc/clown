# zz-pocs/0003: mitmproxy as egress broker — validation

## Purpose

Answer two questions:

1. **Does mitmproxy work as our broker in this setup?** Host-side
   `mitmdump` with a policy-loading addon correctly forwards allowlisted
   HTTPS requests, blocks denied hosts, and produces a usable flow log.

2. **Does the full loop work?** `__impure` Nix builder → `HTTPS_PROXY`
   on loopback → mitmdump → real upstream → response back, with TLS via
   a per-run CA in the closure.

## What this POC is *not*

- Not the real broker plugin. The RFC-0005 contract (policy file per
  invocation, clown-plugin-host handshake, teardown protocol) is ringmaster's
  job in the tracer. Here mitmdump is a long-lived host process and the
  policy is hardcoded.
- Not a real LLM agent. `probe.sh` runs curls.
- Not tied to ringmaster. `run.ts` drives `nix build` directly.

## Files

| File | Purpose |
| --- | --- |
| `flake.nix` | `__impure` derivation; pulls broker host/port/CA from `builtins.getEnv`; builder runs `probe.sh`. |
| `probe.sh` | Bash: four curl probes (allowed HTTPS, denied HTTPS, allowed HTTP, `--noproxy` bypass). Records to `$out/results.txt`. |
| `broker/addon.py` | mitmproxy addon. Loads `policy.json`, enforces host allowlist, short-circuits denials with 403. |
| `broker/policy.json` | Hardcoded policy: allow `httpbun.com`, deny everything else. |
| `run.ts` | zx driver: spawns mitmdump, waits for ready, runs `nix build`, prints results, tears down. |
| `run.sh` | Shim that execs `bun run run.ts`. |

## How to run

```
git add zz-pocs/0003/
./zz-pocs/0003/run.sh
```

Requires `bun` and `mitmdump` on PATH. Both ship with the clown devshell.

## Success criteria

- [ ] mitmdump starts, generates CA, listens on `127.0.0.1:<port>`.
- [ ] Host-side smoke test (curl via proxy to httpbun.com) returns 200.
- [ ] `nix build` succeeds.
- [ ] `results.txt` shows:
  - `allow_https_httpbun_get` → ALLOWED
  - `deny_https_example_com` → BLOCKED (via broker 403)
  - `allow_http_httpbun_get` → ALLOWED
  - `bypass_noproxy_example_com` → ALLOWED (expected; demonstrates
    ADR-0005's advisory-by-convention limit)
- [ ] mitmdump's stderr shows clown-broker ALLOW/DENY log lines.
- [ ] Flow log file has nonzero size.

## Expected surprises / known friction

- **mitmdump's CA path**: mitmproxy writes CA into `~/.mitmproxy` by
  default. `run.ts` overrides `HOME` to a per-run tempdir so each run
  gets a fresh CA.
- **`NIX_SSL_CERT_FILE` vs `CURL_CA_BUNDLE`**: different tools respect
  different env vars. We set `CURL_CA_BUNDLE` for the probe curls. When
  the tracer brings in claude-code, it may need additional env vars.
- **Closure weight**: the builder pulls `curl` + `cacert` into the
  closure (~30MB). mitmproxy itself stays on the host, not in the
  closure.
- **TLS interception**: mitmproxy decrypts, inspects, and re-encrypts.
  Subagent sees our per-run CA as the certificate issuer. Intended.

## Disposition

Keeps. The addon and policy file shape inform RFC-0005 compliance
decisions when the real broker is built in the tracer.
