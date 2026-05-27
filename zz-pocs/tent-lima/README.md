# zz-pocs/tent-lima

Spike: validate that **Lima** is a viable replacement for `podman-machine`
as the backing VM for `clown --tent` on darwin aarch64.

Tracks **clown#99** (explore Lima/Colima as a parallel-VM alternative
to podman-machine for the dev-tent loop).

## Status: PROVEN, recommend GO

The end-to-end smoke succeeded on 2026-05-27:

- `podman-machine-default` was running the entire time (Currently running).
- `clown-tent-lima` (the Lima VM) was created, started, image-loaded,
  and exercised in the same session.
- claude responded to `claude -p hello` inside the Lima VM.

Both VMs ran **concurrently**, validating the core hypothesis behind
this spike.

## Hypothesis (as proposed) → result

| Claim | Result |
|---|---|
| Lima allows parallel VMs alongside podman-machine on darwin | ✅ Verified end-to-end |
| Lima's `ssh.forwardAgent: true` replaces clown's bespoke `ssh -R` LaunchAgent bridge | ✅ Socket published at `/run/host-services/ssh-auth.sock` inside the VM, no extra bridge required |
| Lima's virtiofs mount surfaces /nix/store cleanly | ✅ Verified; `nerdctl run -v /nix/store:/nix/store:ro` works |
| `dockerTools.buildLayeredImage` outputs load into Lima's containerd via `nerdctl load -i` | ✅ Loaded identical clown-tent:0.3.6 tarball that podman uses |
| claude can auth inside the Lima VM with the same `~/.claude/.credentials.json` setup | ✅ Same `debug-extract-claude-credentials` flow works |

## Gotchas surfaced during the spike

### Gotcha #1: Lima auto-rewrites `mountPoint: $HOME`

`limactl create` reserves `/home/<user>.guest` as the in-VM user's
home directory. If the yaml sets:

```yaml
mounts:
- location: "{{.Home}}"
  mountPoint: "{{.Home}}"
  writable: true
```

Lima rewrites `mountPoint` to `/home/<user>.guest` and rejects with
`field "mounts[N].mountPoint" is the reserved internal home directory`.
**Fix:** omit `mountPoint` for the home mount; let Lima resolve it
itself.

### Gotcha #2: multiple claude-code derivations under /nix/store

A typical clown install has at least two `claude-code-*` store paths:

- `claude-code-2.1.111` (darwin Mach-O, npm-source-patched, for un-tented clown)
- `claude-code-2.1.150` (linux ELF / Mach-O variant from llm-agents, for tent)

A naive `ls /nix/store/*-claude-code-*/bin/claude | head -1` inside
the Lima VM picks the wrong one alphabetically and produces
`exec format error` inside the linux container. **Fix:** plumb the
exact `tentClaudeCliPath` from the parent flake via
`clown.inputs.llm-agents.packages.<linux-system>.claude-code` rather
than discovering by `ls`. Same root cause class as **clown#95**
(darwin-built bash baked into a hook that runs inside the linux VM).

### No gotcha (worth noting)

- `nerdctl` argv was a drop-in replacement for `podman run --rm -i -v ... -e ...`. No syntax differences hit during the spike.
- `--network=host` was not needed (Lima's default networking handled outbound to `console.anthropic.com` via Lima's gvisor-tap-vsock fork — same TCP/IP stack idea as podman-machine's gvproxy).
- `--userns=keep-id` was not needed (Lima's default user namespace handling Just Works).

## Performance observations

Times measured during this spike (first cold run):

| Phase | Duration |
|---|---|
| `limactl create` (downloads Ubuntu image + nerdctl tarball) | ~22s |
| `limactl start` (VM boot through "READY") | ~46s |
| `nerdctl load -i clown-tent.tar.gz` | <1s |
| `nerdctl run --rm` (cold container start, including claude API call) | ~5s |

Comparable to podman-machine's `phase4-load-image` + first tent run,
but Lima's first run downloads an Ubuntu cloud image AND nerdctl
archive — subsequent runs reuse cached versions.

## Trade-offs vs the current podman-machine path

**Wins:**
- True parallel VMs; no stop/swap.
- SSH agent forwarding is a single `ssh.forwardAgent: true` yaml line; no bespoke LaunchAgent.
- Lima's `portForwards` supports generic AF_UNIX forwarding (`guestSocket`/`hostSocket`), so if future clown features need to expose a custom host socket, Lima provides a first-class config knob.
- `userns=keep-id` issues (that hung darwin for us) don't apply.

**Costs:**
- New flake input (`lima` from nixpkgs; already packaged at 2.1.1).
- Different CLI inside the VM (`nerdctl` instead of `podman`). For
  clown's existing tent code, this means `cmd/clown/main.go`'s
  podman invocations and `internal/tent/tent.go`'s `BuildArgs` need
  a backend abstraction.
- Lima auto-rewrites the home mount; we have to work around it (see
  Gotcha #1).
- Lima downloads ~600MB on first run (Ubuntu image + nerdctl archive).
- Lima's `vmType: vz` requires macOS 13+. (Same constraint as podman applehv; not a new concession.)

## Recommendation

**GO.** Implement clown#99 fully: migrate the dev-loop machine
management to Lima. Keep podman-machine support as a backwards-
compatibility path until clown#100 (home-manager module) lands and
production tent paths migrate too.

Concrete next steps (not in this POC):

1. Add a `Backend` interface in `internal/tent/` covering image-load,
   container-run, image-exists. Implement `podman` and `nerdctl`
   variants behind it.
2. Move `dev-tent-machine-*` and SSH-forwarder apps to use Lima.
3. Update `cmd/clown/main.go` to discover which backend the baked
   `PodmanMachineName` corresponds to (or rename it to a
   backend-agnostic `MachineName` + `MachineBackend` pair).
4. Document the migration in AGENTS.md.

## How to run

Prerequisites:

- nix-darwin's `nix.linux-builder.enable = true` (cross-build the
  tent image from darwin).
- `~/.claude/.credentials.json` populated (run
  `just debug-extract-claude-credentials` in the main worktree).

Run the smoke:

```sh
cd zz-pocs/tent-lima
nix run .#default --impure
```

`--impure` is required because the smoke probes the host's
`$SSH_AUTH_SOCK`, `~/.claude`, and the running Lima daemon — none
reachable from the pure nix sandbox.

Cleanup:

```sh
limactl stop clown-tent-lima
limactl delete --force clown-tent-lima
```

## Files

- `flake.nix` — derivations for `tent-lima-smoke` (the runner),
  `tent-lima-tools` (lima + yaml + scripts as a bundle).
- `lima.yaml` — VM config: mounts, ssh.forwardAgent, containerd.
- `probe.sh` — end-to-end smoke script invoked by the runner.

## References

- Primary: `clown#99`
- Lima docs: https://lima-vm.io/docs/
- Lima multi-instance: https://github.com/lima-vm/lima/discussions/14079
- Lima ssh.forwardAgent: docs/yaml.md → `ssh:` section
- Container/podman parity issue: containers/podman#23245 (virtiofs cannot proxy AF_UNIX)
