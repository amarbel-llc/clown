# Tent on Darwin — v0 Handoff

**Date:** 2026-05-11
**Related FDR:** [`docs/features/0007-tent.md`](../features/0007-tent.md) (status: `testing`)
**Related issues:** #68, #69, #70 (all closed)
**Related external work:** `amarbel-llc/eng` FDR-0003 (podman-darwin platform)

This doc captures the state of `clown --tent` on darwin at the v0 milestone so a
fresh session can pick up the next stage of work without re-deriving the
problem space.

## What's shipped

`clown --tent` on darwin runs claude-code inside a linux container via
`podman-machine`. End-to-end smoke test (eng-side POC
`zz-pocs/podman-darwin/justfile` phase 5) passes:

    plugin:synthetic-test:mock-mcp:
        http://host.containers.internal:57247/mcp (HTTP) - ✓ Connected

The path through the system, top to bottom:

1. **`clown` (mac-side)** parses `--tent`, picks the linux variant of
   claude-code via `tentClaudeCliPath` (sourced from
   `llm-agents.packages.aarch64-linux.claude-code` on a darwin host).
2. **`newTentExecutor`** preflights: rootless-podman checks short-circuit on
   non-linux, `~/.claude/`, `~/.config/claude/`, and `~/.claude.json` are
   pre-created if missing.
3. **`ensureTentImage`** checks `podman image exists clown-tent:<version>`.
   On darwin, no tarball is baked into the clown binary (`tentImageTarball`
   is empty); the user pre-loads via the eng POC's `phase4-load-image`
   recipe, which cross-builds via nix-darwin's `linux-builder` and
   `podman load`s the result.
4. **Plugin-host** spawns HTTP MCP servers on `127.0.0.1:<ephemeral>` on
   the mac and compiles a plugin manifest. When tent is active on darwin,
   `pluginhost.Host.URLHostRewrite = "host.containers.internal"` swaps
   the host portion of URLs written into the compiled manifest. The host's
   own dial path (healthchecks, shutdown) still uses
   `127.0.0.1:<port>` — only the URL handed to claude-inside-tent gets
   rewritten.
5. **podman-machine** routes `host.containers.internal` from inside the
   VM back to the mac's loopback via gvproxy. claude-inside-tent dials
   `host.containers.internal:<port>/mcp` and reaches the mac-side plugin
   server.
6. **clown's mock-mcp-server** (used by the `synthetic-plugin` fixture)
   handles MCP protocol round-trips correctly (`initialize`,
   `tools/list`, `tools/call`, `notifications/initialized`).

## How to run

### Prerequisites

- macOS aarch64 host with the eng-side podman-darwin POC running. See
  the eng repo's FDR-0003 and `zz-pocs/podman-darwin/` for the
  bootstrap path (LaunchAgent + applehv-provider podman machine
  with `/nix/store` bind-mounted into the VM).
- nix-darwin's `nix.linux-builder.enable = true` switched in. Needed to
  produce the linux/aarch64 closure (image, claude-code binary) on a
  darwin builder.

### Smoke test

```sh
# 1. From clown's checkout, build the linux/aarch64 tent image
nix build .#packages.aarch64-linux.tent-image --no-link --print-out-paths
# (or, easier, run the POC recipe that does this + podman load:)

# 2. Drive the POC's phase 4 + phase 5
cd ~/eng/.worktrees/<your-worktree>/zz-pocs/podman-darwin
just phase4-load-image       # cross-build + podman load (one-shot, idempotent)
just phase4-clown-smoke      # `clown --tent -- --version` → "2.1.138 (Claude Code)"
just phase5-plugin-smoke     # plugin reachable via host.containers.internal
just phase5-tent-interactive # hands you a real interactive claude session
```

### What the user sees end-to-end

    $ clown --tent -- mcp list
    Starting claude inside tent…
    Checking MCP server health…

    plugin:synthetic-test:mock-mcp:
        http://host.containers.internal:57247/mcp (HTTP) - ✓ Connected

## What's next (gating tent → `accepted`)

In rough priority order:

1. **Make tent the default execution path.** Currently `--tent` is opt-in;
   the un-tented path is the default for backwards compatibility. Once
   issue #62 (claude-code 2.1.123+ bump) is resolved — which is the
   external dependency that blocked making tent default in the first
   place — flip the polarity: tent on by default, `--no-tent` and
   FDR-0009's `--naked` are the named escapes. The unbypassable-settings
   invariant becomes enforceable via tent-the-boundary, not via the
   cli.js patch.

2. **Codex / crush / opencode / circus support under tent.** Currently
   `--tent` is gated to `provider=claude`. Each other provider needs:
   its CLI baked in as a linux variant (analogous to
   `tentClaudeCliPath`), profile-aware config injection that survives
   the bind-mount boundary, and any provider-specific environment
   variables added to `tent.DefaultEnvPassthrough`.

3. **Image distribution on darwin.** Today the user runs `phase4-load-image`
   manually before first use. Options for productionizing:
   - Ship a pre-built linux/aarch64 tarball as a GitHub release asset and
     have clown `podman load` it on-demand (mirrors the linux flow but
     decoupled from build-time).
   - Publish to a registry and have clown `podman pull` on first use.
   - Document `nix build .#packages.aarch64-linux.tent-image` as the
     prerequisite (worst UX, smallest scope).

4. **CI for the darwin tent path.** No automation today — promotion to
   `accepted` should include either a GitHub Actions macOS runner that
   exercises phase 5, or a documented manual gate.

5. **Plugin-host loopback story documented across platforms.** The
   `URLHostRewrite` mechanism exists but is only consumed in one place
   (darwin tent). Document the matrix in the FDR or a dedicated section
   in the README before this becomes someone else's problem.

## Open issues / risks

- **`claude mcp list` ergonomics.** The current connectivity check
  inside `claude mcp list` does a real `initialize` round-trip. That's
  what surfaced the broken mock-mcp-server (which only handled
  `tools/list`); fixed in commit 7e0550b but worth knowing — any future
  test fixture must implement the full MCP handshake or claude will
  report it as `✗ Failed to connect` regardless of network reachability.
- **No protocol round-trip test against the HTTP mock.** `plugin_host.bats`
  only checks the compiled manifest *shape*. `stdio_bridge.bats` tests
  protocol round-trips against the *stdio* mock. The HTTP mock now
  matches the stdio mock's protocol behavior, but there's no bats lane
  exercising it end-to-end. The POC's phase 5 is the only round-trip
  test.
- **`tentImageRef` is baked at clown build time, but `tentImageTarball`
  is only available on linux builds.** A darwin clown can advertise the
  tag in its error messages ("image clown-tent:0.2.21 not present
  locally and no tarball is wired in") but cannot bootstrap the image
  itself. That's intentional — the eng POC handles loading — but means
  a fresh darwin clown without the eng POC fails. The error message
  doesn't currently point users at the eng POC; could.
- **The mac UID (501) and the in-container UID under `--userns=keep-id`
  differ on darwin** (the VM-side UID, typically 1000, is what gets
  mapped). Files created inside the container are owned by 1000 on
  the bind-mount source. This hasn't caused observable problems yet
  but is a latent surprise.

## Commits that made this work

In order, on master, leading up to the v0 milestone:

- `3767490` — `feat(tent): wire ClaudeTentCliPath on darwin`
- `44c4bc2` — `feat(tent): wire PodmanPath + TentImageRef on darwin`
- `4b9a5e9` — `feat(tent): pre-create ~/.claude and ~/.config/claude`
- `97568d5` — `feat(tent): source ClaudeTentCliPath from linux variant`
- `bf2a183` — `feat(pluginhost): rewrite manifest URLs to host.containers.internal`
- `7e0550b` — `fix(mockserver): handle MCP initialize`

## Reference

- FDR-0007 §"Update (2026-05-11)" entries for the chronological narrative.
- Eng-side: `~/eng/<worktree>/docs/features/0003-podman-darwin-platform.md`
- POC recipes: `~/eng/<worktree>/zz-pocs/podman-darwin/justfile` (phases 4 and 5).
