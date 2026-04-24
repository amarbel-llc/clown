---
status: accepted
date: 2026-04-24
---

# ADR-0005: Nix Builder Sandbox as the Confinement Layer (no sandcastle)

## Context

FDR-0001 calls for the sandboxed subagent to run inside a chroot with
read-only binds, a separate netns routed only through the egress broker, and
a cooperative-threat-model confinement. The question is which runtime
performs the confinement.

An earlier version of this ADR adopted `sandcastle` (from `amarbel-llc/bob`)
as the runtime. Experimental verification (`zz-pocs/0001`) showed that
sandcastle cannot be invoked from inside a Nix builder on darwin:
`sandbox-exec` refuses to nest. The earlier composition is infeasible on
the darwin dev target.

We then probed what the Nix builder sandbox itself actually enforces under
`__impure = true`. Findings from `zz-pocs/0001` on darwin (`nix 2.32.0`):

- Writes to the real user's home, `/usr/local`, `/etc`, and similar are
  blocked.
- Reads of `~/.ssh/id_rsa`, arbitrary user files, etc. are blocked.
- Enumeration of `/Users/<user>` is blocked; `$HOME` inside the builder is
  `/homeless-shelter` (nonexistent).
- Writes to `/tmp`, `/private/tmp`, `/var/tmp` "succeed" but are remapped to
  the per-invocation builder scratch dir; they don't leak to the host.
- Reads of `/etc/hosts`, `/etc/passwd` succeed (standard for nix builds;
  neither file contains secrets).
- External network is *not* deterministically blocked on darwin — we
  observed a plain-HTTP fetch to `neverssl.com` return 200. `__impure`
  inherits the host netns.
- A host-side TCP listener on `127.0.0.1:<port>` is reachable from inside
  the builder via both `curl` and raw `/dev/tcp`. The broker-on-loopback
  pattern is viable.

## Options Considered

### Option A: sandcastle inside the Nix builder

Original choice. `sandcastle` wraps Anthropic's sandbox tool and dispatches
to `bwrap` on linux / `sandbox-exec` on darwin.

Rejected because:

- **darwin: impossible.** macOS refuses to nest sandbox-exec. A builder
  already inside Nix's `sandbox-exec` cannot `sandbox_apply()` again even
  with an allow-all inner profile. Confirmed experimentally — the `zz-pocs/
  0001` minimal-profile probe returns `Operation not permitted`.
- **linux: fragile.** The bob survey flags nested user-namespace issues on
  the linux side as well.

### Option B: sandcastle outside the Nix builder (outer-driver composition)

Ringmaster runs sandcastle on the host against a Nix-realized closure.
Derivation stages inputs; ringmaster composes sandcastle against `$out`.

Considered. It would work, but its advantages over Option D (this ADR's
decision) are marginal — mostly finer-grained filesystem rules than Nix
gives us out of the box. The cost is orchestration complexity in ringmaster
and a second sandcastle dependency the POC-0002 architecture doesn't need.

### Option C: Direct bwrap / sandbox-exec invocation

Hand-rolled per-platform sandbox composition. Even more work than Option B,
same tradeoffs. Rejected.

### Option D (this ADR): Nix builder sandbox as the confinement, no sandcastle

The Nix builder sandbox already does:

- Filesystem confinement (kernel-enforced on both darwin and linux).
- Read-only `/nix/store`.
- Closure-narrowed `$PATH` via `nativeBuildInputs`.
- Per-invocation scratch dir.

What it does *not* do:

- Per-subagent network policy (allow-list of specific domains).
- Secret injection into upstream requests.
- Audit logging at the request level.
- Rate limiting.

Those gaps are exactly what the egress broker (RFC-0005) handles. Since
the subagent can only reach loopback + whatever the host permits (and we
point `HTTPS_PROXY` at the broker), the broker becomes the network
enforcement point.

### Option E: Firecracker microVM per invocation

Strongest isolation; rejected for v1 per FDR-0001 cost/complexity reasoning.
A future FDR may introduce it as a stricter tier, reusing everything else
specified here.

## Decision

Adopt Option D — rely on the Nix builder sandbox for filesystem + closure
confinement, the egress broker for network policy, and closure-narrowed
`nativeBuildInputs` for tool restriction. Sandcastle is dropped from the
composition.

## Consequences

### The composition becomes

- **Filesystem:** Nix's builder sandbox. Subagent can write only to `$out`
  and the per-invocation scratch; reads are limited to `/nix/store` + a
  small set of harmless system paths.
- **Tools:** closure narrowing via `nativeBuildInputs`. If `curl` isn't in
  the closure, the subagent can't invoke it.
- **Network egress:** pointed at a host-side egress broker on loopback.
  `HTTPS_PROXY` and a CA bundle bind-mounted into the closure.
- **Capability manifest:** the derivation's `.drv` hash, exactly as before.
- **Outer orchestration:** ringmaster.

### What we give up

- **Finer-than-Nix filesystem policy.** Sandcastle would have let us say
  things like "allow writes only to these specific paths inside $out".
  Nix's sandbox is less expressive. For the cooperative threat model, this
  is fine — the subagent isn't adversarial.
- **Kernel-enforced network policy.** The Nix darwin sandbox does not
  systematically deny external network (we observed external HTTP
  succeeding). The broker becomes an advisory-by-convention control; a
  misbehaving subagent could in principle route around `HTTPS_PROXY` and
  reach external hosts directly. The cooperative threat model makes this
  acceptable; an adversarial model requires something stronger (Firecracker,
  gVisor, or a host-level firewall per invocation).
- **Cross-platform policy uniformity.** Since we rely on each platform's
  Nix builder sandbox directly, subtle semantic differences between darwin
  and linux builders may surface. We accept this in exchange for avoiding
  the sandcastle nesting problem entirely.

### Implications for ringmaster

Simpler than the sandcastle-based design. Ringmaster's tool-call handler:

1. Spawn the egress broker on `127.0.0.1:<ephemeral-port>` with the
   subagent's policy.
2. Generate an `__impure` derivation that:
   - Copies the workspace into `$out`.
   - Sets `HTTPS_PROXY=http://127.0.0.1:<port>` and
     `NIX_SSL_CERT_FILE=<broker-ca>`.
   - Invokes the agent with the subagent's declared closure on `$PATH`.
3. `nix build` it.
4. Return `$out` as the `out_ref`.
5. Tear down the broker.

No sandcastle. No outer-driver composition. The Nix build *is* the sandbox.

### Implications for cross-platform

- **linux:** Nix builder sandbox uses namespaces (mount, PID, IPC, UTS, net,
  user). Confinement is stronger than darwin's. Network behavior under
  `__impure` may differ — worth re-running `zz-pocs/0001`'s probe there
  before tracer/v1 work targets linux.
- **darwin:** confirmed by `zz-pocs/0001`. Works.

### If we later want the stricter tier

A future FDR can introduce sandcastle or Firecracker as an optional layer
for subagents that need it (`sandbox.strict = true` or similar). Nothing in
this ADR precludes that. The derivation stays the same; the runtime swap
happens in ringmaster's tool-call handler.

## References

- `zz-pocs/0001` — empirical probes behind this decision.
- FDR-0001 §Security Model — describes what each layer enforces.
- RFC-0005 — the egress broker protocol that pairs with this ADR.
- Nix manual, "Advanced Attributes" — `__impure`, sandbox semantics.
