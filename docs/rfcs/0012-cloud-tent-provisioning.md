---
status: exploring
date: 2026-06-09
---

# Cloud Tent Provisioning Contract (clown ↔ eng)

## Abstract

This specification defines the contract between the **image producer** (eng,
FDR-0005) and the **orchestrator** (clown's `--tent` CLI, FDR-0014) for
standing up a cloud-hosted tent: a NixOS VM that boots already joined to the
operator's Tailscale tailnet. It fixes the interface — image format, the
first-boot auth-key delivery path, the network/firewall posture, the hostname
and tailnet conventions, and the OpenTofu input/output surface — so the two
repos build, version, and ship independently. It deliberately does **not**
specify how the image is built internally (eng FDR-0005) nor what agent
workload runs inside the tent (clown FDR-0014).

## Introduction

clown FDR-0014 repoints tents from local containers to cloud VMs. The split of
responsibility (decided 2026-06-09): **eng = image, clown = CLI.** eng emits a
reproducible NixOS image per provider; clown drives provisioning via OpenTofu
using the `digitalocean` and `tailscale` providers, following the
`friedenberg/ts-do-piho` prior art. Two repos implementing one capability is
exactly the case an RFC exists for (eng AGENTS.md: "RFC specifies the interface
contract when multiple repos implement it").

The contract must let a destroyed tent leave no tailnet residue, must never put
the Tailscale auth key where it can be recovered (Nix store, image, git, tofu
state-at-rest in plaintext), and must keep the guest reachable *only* over the
tailnet.

## Requirements Language

The key words MUST, MUST NOT, SHOULD, SHOULD NOT, and MAY are to be
interpreted as in RFC 2119.

## 1. Image artifact

1.1. eng MUST publish, per provider, a flake package producing an uploadable
disk image:
- DigitalOcean: `packages.x86_64-linux.tent-cloud-image-digitalocean`,
  `nixos-generators` `format = "do"` (gzipped raw disk).
- Hetzner (deferred): `…-hetzner`, a Hetzner-consumable format.

1.2. The image MUST be reproducible from a pinned `flake.lock` and MUST NOT
contain any tailnet identifier, auth key, or operator secret.

1.3. The image MUST boot with: `services.tailscale` enabled, an SSH daemon
reachable over the tailnet, a serial console (for provider-console recovery),
and the agent runtime clown FDR-0014 requires (the latter referenced, not
specified here).

## 2. First-boot auth-key delivery

2.1. The Tailscale auth key MUST be delivered to the guest at provision time
through the provider's **user-data / metadata** channel, NOT baked into the
image.

2.2. The guest MUST land the key at a single well-known path, tmpfs-backed,
mode 0600: **`/run/tailscale-authkey`**. `services.tailscale.authKeyFile`
points there.

2.3. The key MUST be **ephemeral**, **pre-authorized**, and **single-use**
(`reusable = false`, `ephemeral = true`, `preauthorized = true` on the
`tailscale_tailnet_key` resource). Ephemeral guarantees a destroyed tent
self-evicts from the tailnet; single-use guarantees a leaked image cannot
re-join.

2.4. The key MUST be minted in the **operator's** tailnet, via the operator's
Tailscale credentials (`TAILSCALE_API_KEY` / OAuth client) supplied to the
`tailscale` OpenTofu provider. "The current user's tailnet by default" is
realized by *which credentials mint the key*, never by a tailnet id in the
image (2.2 of FDR-0005).

## 3. Network posture

3.1. The provisioner MUST attach a provider firewall that is **deny-all
inbound except Tailscale**, mirroring ts-do-piho:
- inbound UDP 41641 from `0.0.0.0/0, ::/0` (Tailscale WireGuard),
- inbound UDP 3478 from `100.64.0.0/10` (STUN, tailnet CGNAT range),
- outbound unrestricted (ICMP, TCP, UDP) so the node can reach DERP/the
  control plane.

3.2. There MUST be no inbound public SSH (port 22). Operator SSH access is
over Tailscale (Tailscale SSH, `--ssh` in `extraUpFlags`).

3.3. A short-lived bootstrap SSH key MAY be attached for the
provider-`remote-exec`/user-data path before Tailscale is up (ts-do-piho mints
a `tls_private_key` for exactly this), but it MUST NOT remain a standing public
ingress after first boot.

## 4. Hostname / identity convention

4.1. The droplet name and the Tailscale hostname MUST share a deterministic,
collision-resistant convention so the operator (and `clown --tent status`) can
map a tailnet node back to a tent. Proposed: `tent-<short-session-or-rand>`;
exact scheme TBD in `proposed`.

4.2. `extraUpFlags` MUST set `--hostname` to that name so MagicDNS resolves the
tent predictably.

## 5. OpenTofu surface

5.1. clown ships the HCL (provider config + droplet + firewall + tailnet key),
adapted from ts-do-piho, and invokes `tofu` (Nix-pinned). The HCL MUST expose:
- **inputs (variables)**: tent name, region/size (provider-specific, with
  defaults), and the image reference (id or URL, per §6).
- **outputs**: the tent's tailnet hostname/IP and the droplet id, which clown
  reads back to connect and to destroy.

5.2. tofu state contains the minted (single-use, ephemeral) key and MUST be
encrypted at rest (git-secret today, matching ts-do-piho; sops/agenix is
future work, eng#77). A destroyed tent's key is already useless (single-use +
ephemeral), bounding the blast radius of a state leak.

5.3. `clown --tent down` MUST run `tofu destroy` and SHOULD verify the tailnet
node is gone (ephemeral expiry is the backstop).

## 6. Image distribution (open)

6.1. For DigitalOcean the image reference is either a custom-image-by-URL
(image gzip hosted at a reachable URL, `doctl compute image create`) or an
in-region snapshot. The contract requires only that the tofu `image` input
accepts a stable id; which mechanism produces that id is resolved in the
experimental phase (FDR-0005 open question 1).

## 7. Versioning

7.1. eng and clown version independently. A breaking change to any MUST clause
in §§2–4 (the auth-key path, the firewall shape, the hostname flag) is a
contract change requiring a bump to this RFC and coordinated releases. The
image format (§1) and the tofu surface (§5) MAY evolve without an RFC bump as
long as §§2–4 hold.

## References

- clown FDR-0014 — cloud tent (orchestrator side)
- eng FDR-0005 — NixOS cloud tent image (producer side)
- `friedenberg/ts-do-piho` — DO + Tailscale + OpenTofu prior art (HCL shapes
  for the droplet, ephemeral tailnet key, and firewall reproduced here)
- RFC 2119 — requirements language
