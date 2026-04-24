# zz-pocs/0001: Nix builder sandbox boundary probes

## Purpose

Answer: **what does the Nix builder sandbox actually enforce under
`__impure = true` on darwin?**

This started life as "sandcastle inside a Nix builder" and pivoted twice:

1. First the darwin nix-daemon config needed fixing (`impure-derivations`
   and `allowed-impure-host-deps` not enabled by default). Tracked on
   [amarbel-llc/eng#41](https://github.com/amarbel-llc/eng/issues/41),
   since resolved.

2. Then sandcastle itself couldn't run inside the Nix builder: macOS
   refuses to nest `sandbox-exec`. Confirmed by a minimal-profile probe —
   even `(allow default)` returns `Operation not permitted`.

At that point the design question became: do we *need* sandcastle? The
Nix builder sandbox is already kernel-enforced. The probes in this POC
were added to find out what it already denies and where the gaps are.

The findings drove [ADR-0005](../../docs/adrs/0005-nix-builder-as-sandbox.md):
drop sandcastle, rely on the Nix builder sandbox + egress broker + closure
narrowing.

## Findings

See `probe.sh`. Key results on darwin under `__impure`:

- **Filesystem confinement: strong.** Writes to real user home,
  `/usr/local`, `/etc`, SSH keys — all blocked. Writes to `/tmp`,
  `/private/tmp`, `/var/tmp` "succeed" but are remapped to per-invocation
  scratch (don't leak to host).
- **Home directory is `/homeless-shelter` (nonexistent).** No accidental
  access to the real `$HOME`.
- **`/etc/hosts` and `/etc/passwd` are readable.** Standard; neither
  contains secrets.
- **Loopback to a host-side listener works.** Both `curl` and raw
  `/dev/tcp` can connect to `127.0.0.1:<port>` if something on the host is
  listening. This is the channel the egress broker uses.
- **External network is not deterministically denied.** `__impure`
  inherits the host netns on darwin. The broker is advisory-by-convention.

## Files

| File | Purpose |
| --- | --- |
| `flake.nix` | `__impure` derivation with a trivial `buildPhase` that runs `probe.sh`. Pulls host env vars (`CLOWN_PROBE_LOOPBACK_*`) via `builtins.getEnv` and bakes them into the builder. |
| `probe.sh` | Battery of filesystem + network probes. Records "BLOCKED" / "ALLOWED" per test to `$out/results.txt`. |
| `run.sh` | Driver. Spawns a host-side nc listener on `127.0.0.1:53712` that responds with a known banner, then kicks off `nix build`. The probe inside tries to reach the listener and verify the banner came through. |

## How to run

```
git add zz-pocs/0001/
./zz-pocs/0001/run.sh
```

The flake must be git-tracked for Nix to see it.

## What this POC does *not* prove

- **Linux parity.** Run again on a linux host. Flagged in FDR-0001's open
  questions.
- **The broker works end-to-end.** We proved loopback reaches a *dumb* nc
  listener. mitmproxy-specific behavior (TLS termination with per-
  invocation CA, allowlist enforcement, audit logging) is the next POC
  (proposed `zz-pocs/0003`).
- **Everything else.** See FDR-0001 open questions.

## Disposition

Keep as documentation for the ADR-0005 decision. The probes are useful
for re-running when environment changes (new macOS version, new nix
version, linux host). Not load-bearing for other POCs.
