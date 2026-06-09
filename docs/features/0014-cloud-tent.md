---
status: exploring
date: 2026-06-09
supersedes: FDR-0007, FDR-0012
promotion-criteria: >
  exploring -> proposed: this record + RFC-0012 (clown↔eng provisioning
  contract) reviewed; eng FDR-0005 (the NixOS image) accepted in `proposed`;
  the deprecation plan for the local podman/lima tent is agreed (kept building,
  marked deprecated, removed in a named follow-up).
  proposed -> experimental: `clown --tent up` provisions a DigitalOcean droplet
  from the eng-built NixOS image, the droplet auto-joins the operator's tailnet,
  and `clown --tent` reaches the agent loop on it; `clown --tent down` destroys
  it and the ephemeral node self-evicts. The local podman/lima path still builds
  but is no longer the documented tent.
  experimental -> testing: provision/connect/destroy is reliable across repeated
  cycles, the auth key never lands in the store or image, and the working-tree
  delivery story (how the user's repo reaches the cloud tent) is documented and
  tested.
  testing -> accepted: cloud tent is the default meaning of `--tent`; the local
  podman/lima implementation, the `tentBackend` build lever, the lima
  home-manager module, and the dev-tent recipes are removed; Hetzner builds from
  the same eng module set.
---

# Cloud Tent — Tents as Cloud-Hosted NixOS VMs

## Problem Statement

FDR-0007 defined **tent** as "the environment the harness sees" — the trust
boundary that enforces clown's unbypassable-settings invariant by controlling
the filesystem, network, and command surface around the agent. The chosen
substrate was a **local container**: podman on linux, podman-machine on darwin,
with a lima alternative (FDR-0007, clown#99/#100). FDR-0012 then spent its
entire problem statement documenting how badly that substrate fits darwin —
the host Nix store holds Mach-O binaries that cannot execute in a linux
container, every shebang-less subprocess in a plugin hook hits
`Exec format error` (#44), the mount list grew reactively (eng#107/108/112),
and `--tent-pass-devshell` races silently rewrite PATH.

The local-container substrate carries structural costs that no amount of
mount-list tuning removes:

1. **Cross-arch impedance** (FDR-0012): a linux container on a darwin host is
   a permanent arch-mismatch hazard. The eng dev loop documents the pain — a
   full `home-manager switch` → `podman machine rm -f` → `launchctl kickstart`
   round-trip just to change a bind mount.
2. **One-VM-at-a-time on darwin**: podman-machine enforces
   `RequireExclusiveActive()` across every darwin provider, so clown's own
   dev loop is a "stop/swap" dance that takes the eng-managed VM offline
   (AGENTS.md "Dev loop for tent"). Lima was explored specifically to escape
   this (clown#99).
3. **Host-coupling**: the tent's capabilities are bounded by the host's
   hypervisor, its Nix store, its podman-machine config — the boundary is only
   as strong and as portable as the laptop it runs on. ssh-agent forwarding
   needs a bespoke `ssh -R` bridge because virtiofs can't proxy AF_UNIX
   (containers/podman#23245).

The pivot: **stop hosting the tent on the operator's machine. Host it in the
cloud.** A tent becomes a cloud VM — DigitalOcean first, Hetzner later — booted
from a reproducible NixOS image (eng FDR-0005) that joins the operator's
tailnet on first boot. The containment boundary is now a whole machine the
operator does not own the kernel of, reachable only over Tailscale; the
cross-arch problem disappears (the guest is always linux/amd64 regardless of a
darwin host); the one-VM rule disappears (provision as many tents as you'll
pay for); and the host's only job is to run the orchestration CLI and a
Tailscale client.

This supersedes FDR-0007's "local container" substrate and the entire
FDR-0012 narrow-userspace effort (which was a response to the local-container
arch problem that the cloud substrate eliminates by construction). The *policy
goal* of FDR-0007 — bound the agent's blast radius so broad Bash permissions
are safe — is preserved; only the substrate changes.

## What a cloud tent is

```
operator's machine                         cloud (DigitalOcean)
┌────────────────────┐                     ┌──────────────────────────┐
│ clown --tent (CLI) │                     │  NixOS droplet (the tent) │
│   └─ OpenTofu      │── DO API ──────────▶│   - eng-built image       │
│   └─ tailscale     │── TS API (mint key)▶│   - services.tailscale    │
│   client           │                     │   - the agent loop        │
└─────────┬──────────┘                     └────────────┬─────────────┘
          └──────────────  Tailscale  ──────────────────┘
                    (only reachable over the tailnet)
```

- **eng** owns the image (eng FDR-0005): a NixOS system that boots configured
  to join the tailnet via an ephemeral, pre-authorized, single-use auth key
  delivered through provider user-data into guest tmpfs.
- **clown** owns the CLI and the provisioning. `clown --tent` drives OpenTofu
  (the chosen orchestration engine) to: register the image, mint the tailnet
  key (operator's tailnet, via the operator's Tailscale credentials), create
  the droplet + a deny-all-but-Tailscale firewall, and tear it all down.
- The contract between the two repos — image format, the user-data auth-key
  path, firewall expectations, hostname convention — is **RFC-0012**, so eng
  and clown version independently.

## Interface (sketch — to be firmed in `proposed`)

```sh
clown --tent up            # tofu apply: image + authkey + droplet + firewall
clown --tent               # connect to the running tent, run the agent there
clown --tent down          # tofu destroy: droplet gone; ephemeral node self-evicts
clown --tent status        # tofu/tailscale state of the current tent
```

`--tent` with no subcommand keeps its FDR-0007 meaning ("run the agent in the
tent") but now resolves to the cloud VM. `--no-tent` / FDR-0009 `--naked`
remain the escapes. Whether the cloud tent is per-session-ephemeral or a
longer-lived reattachable VM is an open question (below).

Provisioning is **OpenTofu**, not native Go: the ts-do-piho prior art
(`friedenberg/ts-do-piho`) already encodes the DO-droplet + tailnet-key +
firewall shape in tofu HCL with the `digitalocean` and `tailscale` providers,
and tofu's state model handles the create/destroy lifecycle clown would
otherwise hand-roll. clown ships the HCL and shells out to `tofu`
(discovered/pinned via Nix), reading droplet IP / tailnet name back from tofu
outputs.

## Deprecation of the local-container tent

Per the pivot decision, the local podman/lima implementation is **deprecated,
not deleted yet** — it keeps building while the cloud path is proven, and is
removed in a named follow-up (gated by this FDR reaching `accepted`). The
surface to retire, for the eventual removal PR:

- **Go**: `internal/tent/` (the `tent.Backend` interface —
  `ImageExistsArgs`/`LoadImageArgs`/`RunArgs`/`Binary()` — its podman and lima
  impls, and `tent.BuildArgs`); `cmd/clown/`'s `newBackend()`,
  `ensureTentImage`, `runTentImageLoad`, and `tentExecutor` wiring.
- **buildcfg**: `PodmanPath`, `PodmanMachineName`, `LimactlPath`, `TentBackend`
  ldflags.
- **flake**: the `tentBackend` parameter on `mkClownGo`/`mkClownPkg`/`mkCircus`,
  `packages.dev` / `packages.dev-lima`, `devTentVolumes`, and
  `homeManagerModules.tent-backend-lima`.
- **justfile**: the `dev-tent-*` / `smoke-dev-tent` family and
  `dev-tent-ssh-forward`.
- **POCs/tests**: `zz-pocs/tent-lima/`, the tent bats.
- **eng**: `programs.podman-darwin` and the `enable-tent-claude` identity
  field become cloud-tent-irrelevant once removal lands (coordinate via eng).

Until removal, AGENTS.md's "Dev loop for tent", "Lima home-manager module",
and "Tent backend lever" sections gain a one-line "**deprecated — see
FDR-0014**" banner rather than being rewritten.

## Non-goals / open questions

1. **Working-tree delivery.** How the operator's local repo reaches the cloud
   tent (push a branch and clone? mutagen/rsync over Tailscale SSH? work
   entirely cloud-side?) is the biggest unanswered design question and is
   deferred to `proposed`. The local tent bind-mounted `$PWD`; a cloud tent
   cannot.
2. **Ephemeral vs. reattachable.** Per-session throwaway droplets (clean, but
   slow cold-start and repeated clone cost) vs. a long-lived per-operator tent
   you reattach to. Likely a knob.
3. **Cost / lifecycle hygiene.** Cloud tents cost money and leak if not
   destroyed; `down` must be reliable and a sweep for orphaned droplets is
   probably required.
4. **Unbypassable-settings on a VM the operator controls.** FDR-0007's
   invariant assumed clown controlled the container. On a cloud VM the operator
   has root via the provider console — the threat model shifts from "contain
   the agent on the user's trusted machine" to "isolate the agent's blast
   radius onto a disposable machine." This needs to be restated explicitly.
5. **Auth / credentials in-tent.** Claude OAuth credentials, signing keys, and
   ssh-agent reaching a cloud VM is a different (and in some ways simpler, via
   Tailscale SSH) problem than the local bind-mount + `ssh -R` bridge.

## References

- eng FDR-0005 — NixOS cloud tent image (DigitalOcean first)
- clown RFC-0012 — clown↔eng cloud-tent provisioning contract
- FDR-0007 — tent (superseded substrate: local container)
- FDR-0012 — tent narrow userspace + hook tunnel (superseded: a response to the
  local-container arch problem the cloud substrate removes)
- FDR-0009 — `--naked` emergency bypass (the named escape; unchanged)
- `friedenberg/ts-do-piho` — DO + Tailscale + OpenTofu prior art
