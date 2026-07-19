---
status: accepted
date: 2026-05-09
---

# ADR-0007: Drop the `net_cap` bats file_tag (no Linux sandbox denies loopback)

## Context

`zz-tests_bats/stdio_bridge.bats` and `zz-tests_bats/plugin_host.bats` carried a
`# bats file_tags=net_cap` directive at the top. The auto-discovery in
`bats.nix` produces one lane per unique tag, so this gave us a separate
`bats-net_cap` derivation alongside `bats-default`. The original framing
was sandbox-portability: those tests bind `127.0.0.1`, and the comment
explained that the tag protected against environments where the nix
sandbox might not grant loopback — call this the "Hydra-style remote
build with `sandbox = pure`" scenario gestured at in the issue.

`bats-default` had `filter = ""` (no exclusion), so the tag was already
purely informational: `net_cap`-tagged files ran in *both* lanes. The
question is whether the tag should ever filter, and if so, what
environment is it filtering for.

Issue: [#47](https://github.com/amarbel-llc/clown/issues/47).

## Options Considered

### Option A: Mirror madder — `bats-default` filters out `net_cap`

Set `bats-default = mkClownBatsLane { filter = "!net_cap"; };` so the
default lane is portable to environments that don't grant loopback;
`bats-net_cap` becomes the explicit opt-in for environments that do.
This mirrors the pattern in `amarbel-llc/madder/go/default.nix`.

Worth noting: madder's reason for filtering is structurally different
from clown's. Madder's `net_cap` tests need an external
`madder-test-sftp-server` harness that's only built in the devshell,
not in the nix-driven lanes, so filtering is a hard requirement there.
Clown's tests don't need any external harness — server and client live
in the same test process tree.

### Option B: Keep the tag as documentation only

Leave the tag in place, leave `bats-default` unfiltered, and rewrite the
comments to acknowledge that the tag is purely informational. Future
contributors keep the convention if they ever add another loopback test.

### Option C (this ADR): Drop the tag entirely

Remove the `# bats file_tags=net_cap` directive from both files, let the
auto-generated `bats-net_cap` lane disappear, point the corresponding
`justfile` recipes at `bats-default`, and update comments to explain
why no tag is needed. Re-tag if and when a loopback-denying environment
is actually introduced.

## Sandbox Analysis

The deciding factor is empirical: what Linux sandbox tooling actually
denies loopback to a server-and-client pair living in the same process
tree?

**Mainstream tools surveyed** (all create a fresh network namespace; in
all of them the kernel brings up `lo` by default):

- **Nix's own build sandbox** (`sandbox = true`, the Linux default and
  what Hydra runs). Each derivation gets its own
  net/mount/PID/user/UTS/IPC namespaces. `lo` is up. `127.0.0.1` binds
  succeed.
- **bubblewrap (bwrap)**, used by Flatpak and by fence's Linux backend.
  Same model: unshare `--net`, `lo` is up.
- **systemd-run with sandbox properties**
  (`-p PrivateNetwork=yes -p ReadOnlyPaths=…`). Same.
- **firejail**, **nsjail**, **`unshare(1)` from coreutils**. Same.
- **Use-Tusk/fence** (numtide/llm-agents.nix `#fence`). Tracer-bullet
  test recorded in `.tmp/fence-loopback-test.bash` (deleted with the
  worktree). Result: bind to `127.0.0.1` succeeds inside fence; only
  connections to *host* loopback (a listener outside the sandbox) fail
  (`ECONNREFUSED`, because fence's namespace has its own `127.0.0.1`).
  Server-and-client in the same fenced process tree work normally.

To actually take loopback DOWN inside a netns requires
`ip link set dev lo down`, which needs `CAP_NET_ADMIN` inside the
namespace, which needs `unshare --user --map-root-user` plumbing.
No widely-used CI or sandbox preset does this. The "sandbox that
denies loopback" the original tag was written against does not appear
to exist in our environment or in any environment we plan to ship to.

Two related fence findings, recorded for traceability:

- The documented `network.allowLocalBinding` field is silently dropped
  by `fence config show` in the version we tested (0.1.57, packaged in
  numtide/llm-agents.nix). Either docs vs. version drift or an
  unimplemented field. Not load-bearing for this ADR — fence isn't
  doing what the original tag was hedging against either way.
- Fence issue [Use-Tusk/fence#128] (closed, fix in 0.1.50) confirms
  that `allowLocalOutbound` only began working on Linux in mid-April;
  before that, all localhost outbound was blocked unconditionally. The
  fix added `allowLocalOutboundPorts` for the bridge configuration.

[Use-Tusk/fence#128]: https://github.com/Use-Tusk/fence/issues/128

## Decision

Option C. Drop the tag.

Rationale: the tag's stated purpose was to protect against an
environment that, on inspection, doesn't exist in any tooling we use
or plan to use. Keeping it as documentation (Option B) preserves a
piece of fiction in the test files. Filtering it (Option A) makes
`bats-default` weaker without a corresponding environment that
benefits.

## Consequences

### Good

- `bats-default` keeps running every integration test, and its build
  hash is the same artifact developers and CI already exercise — no
  silent reduction in default-lane coverage.
- One less concept (`bats-net_cap`) for new contributors to learn. The
  comment update redirects readers to this ADR if they wonder why the
  pattern was removed.
- The `mkClownBatsLane` machinery still auto-generates per-tag lanes;
  the moment a real tag appears (e.g., `slow`, `flaky`,
  `requires-tor`), the corresponding lane materializes without further
  flake plumbing.

### Bad

- If a future build environment is introduced that does deny loopback
  to a single-process-tree server+client (e.g., a heavily customized
  remote Hydra builder, a security-research sandbox), `stdio_bridge.bats`
  and `plugin_host.bats` will fail there with no preexisting filter
  protecting the default lane. Mitigation: re-tag at that point.
  Re-introducing a tag is a one-line change per file plus a follow-up
  ADR; the cost of staying nimble is small.
- The auto-generated `bats-net_cap` derivation disappears from
  `nix flake show` output. Any downstream tooling that referenced it
  by name will break. None known in this repo or its consumers; the
  internal references in `justfile` were updated in the same commit
  that dropped the tags.

## More Information

- Resolves [#47](https://github.com/amarbel-llc/clown/issues/47)
- Implementation: commit dropping the tags is the predecessor of this
  ADR's commit (`build(bats): drop net_cap file_tags from integration
  tests`).
- Reference pattern: `amarbel-llc/madder/go/default.nix`
  (`bats-default = mkBatsLane { filter = "!net_cap"; };`).
- Related ADR: [ADR-0005](./0005-nix-builder-as-sandbox.md) — describes
  what the nix builder sandbox actually enforces (loopback is reachable;
  external network is not deterministically blocked).
