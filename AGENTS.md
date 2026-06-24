# AGENTS.md

This file provides guidance to coding agents working in this repository,
including Codex and Claude Code.

## Overview

Clown is a Nix-packaged wrapper around coding agents (Claude Code, Codex,
local models via `circus`, OpenAI-compatible providers via `opencode`, and
charmbracelet's crush) that injects custom system prompts, applies per-provider
safety defaults, and provides fish shell completions and session management.
A single `clown` binary dispatches to the selected provider via
`--provider <claude|codex|circus|opencode|crush>` (default: `claude`; override
with `CLOWN_PROVIDER` env var). Built entirely with Nix flakes; no standalone
test suite (pre-merge validation via `just build`).

## Agent gotchas specific to this repo

- **Skills vs `.circus/` are different things — don't confuse them.** Skills
  (`/eng:fdr`, `/eng:rfc`, `/init`, etc.) are listed in the session's
  available-skills system reminder and invoked via the `Skill` tool. Don't
  go hunting for them on the filesystem under `.circus/skills/` — that path
  doesn't exist. `.circus/` is exclusively for clown's system-prompt
  injection: `.circus/system-prompt` (replace) and
  `.circus/system-prompt.d/*.md` (append fragments). When asked to
  "create an FDR / ADR / RFC", invoke the matching skill rather than
  reverse-engineering the conventions by copying an existing doc.

## Build Commands

```sh
just build       # Default: nix build --show-trace
just build-go    # Build Go binaries (clown, clown-plugin-host)
just test-go     # Run Go unit tests
just clean       # rm -rf result
```

Format: `nix fmt` (treefmt; config in `treefmt.nix`)

## Smoke recipes for local-model workflows

While FDR-0011 phase 2 (`--backend=circus`, tracked by #87) is still
pending, the `justfile` carries a family of host-side recipes that
drive circus + ringmaster + the harnesses (claude / opencode / crush)
against a tailnet-exposed local model. They share a common shape:
look up the running instance's port via `circus list`, resolve this
host's MagicDNS name via `tailscale status --json`, then either probe
the URL with curl or launch a harness pointed at it. Each fails fast
with friendly errors if ringmaster isn't running, the alias isn't
registered, or tailscale/jq aren't on PATH.

Read the comment header on each recipe for the full mechanism — they
spell out which dispatch path inside clown each one is exercising and
the model-quality caveats. Quick map:

- **`smoke-ringmaster-multi`** — multi-instance lifecycle against
  real `llama-server` children (FDR-0010 criterion 2). Fake-server
  parity lives in `zz-tests_bats/ringmaster.bats`.
- **`smoke-tailnet-url [alias]`** — compute the tailnet URL for a
  running instance and probe `/v1/messages`. Diagnostic only —
  exercises the Anthropic endpoint, not the OpenAI one
  opencode/crush use.
- **`smoke-clown-against-tailnet [alias] [naked]`** — launch
  claude-code against the tailnet URL via the four `ANTHROPIC_*` env
  vars. `--naked` by default; pass `NAKED=0` for the full pipeline.
- **`smoke-opencode-against-tailnet [alias]`** /
  **`smoke-crush-against-tailnet [alias]`** — launch opencode/crush
  via the bare-provider dispatch (no `--profile`). Backs up + writes
  `~/.config/circus/<provider>.toml`, restores on EXIT via a bash
  trap. Talks to llama-server's OpenAI endpoint, so tool calls work
  if the model is OpenAI-function-call-trained.
- **`download-ad-hoc <name> <url>`** — fetch a GGUF by URL (no SHA
  required), then print a ready-to-paste `registry.json` entry with
  the computed SHA-256. Promote-to-registry is a manual JSON edit +
  test-count bump; issue #86 proposes automating the whole flow.
- **`download-qwen-coder`** — wraps `download-ad-hoc` to fetch
  Qwen2.5-Coder-7B-Instruct (Q4_K_M, ~4.7GB). Smallest model where
  tool calls through opencode/crush actually work.

All these recipes will be replaced (or removed) once FDR-0011 phase 2
ships `clown --backend=circus --circus-bind=…`. Until then they're
the supported way to drive local models through clown.

## Dev loop for tent

`clown --tent` on darwin normally targets the eng-managed
`podman-machine-default` VM provisioned by `programs.podman-darwin`
(in `amarbel-llc/eng`). Iterating on tent mount logic that way
requires a full eng round-trip: edit the home-manager module →
`home-manager switch` → `podman machine rm -f` → `launchctl
kickstart` → smoke. Several mount-list gaps shipped only after eng
landed an edit (eng#108 added `$HOME`, eng#112 added `/nix/var`);
the loop is slow.

This flake exposes a self-contained dev loop with one important
caveat: **podman on darwin enforces a strict one-VM-at-a-time
rule**. Verified in upstream source — every darwin provider
(`applehv`, `libkrun`, `qemu`) returns `RequireExclusiveActive() ==
true` and there is no provider config that opts out. So the dev loop
here is a **stop/swap**: `dev-tent-up` stops the eng-managed default
to free the VM slot, runs the dev machine, and `dev-tent-down`
reverses it (stops the dev machine, restarts the default). The
eng-managed VM is **temporarily unavailable** between `up` and
`down`; `down` brings it back. Lima/Colima support concurrent VMs
and are tracked as a follow-up — see the open clown issue for
"explore Lima/Colima as a parallel-VM alternative."

The just recipes are the **paved-path** form (pre-allowlisted; no
permission prompts); the underlying `nix run` invocations are
equivalent if you prefer them directly.

```sh
# End-to-end iteration in one verb: swap to clown-dev → build .#dev
# → run clown --tent -- --version. Add DOWN=1 to swap back at the
# end (restart the eng default).
just smoke-dev-tent             # one-shot e2e; leaves clown-dev up
just smoke-dev-tent DOWN=1      # one-shot e2e + swap back

# Or step by step:
just dev-tent-up                # stop default, start clown-dev
just dev-tent-status            # shows BOTH machines' states
nix build .#dev && ./result/bin/clown --tent -- --version
just dev-tent-down              # stop clown-dev, restart default

# Iterate on the mount list (edit `devTentVolumes` in flake.nix):
just dev-tent-down && just smoke-dev-tent
```

About the volumes: `devTentVolumes` in `flake.nix` is intentionally
**broad** — a single `/:/:rw` bind. The container-level
`--volume` filtering in `tent.BuildArgs` is what actually controls
what the tent sees, not the VM-level mount. With `/` mounted at the
VM, iterating on the bind-candidate list no longer requires
`machine rm && init` cycles. The same principle should be considered
for the eng-managed VM (eng issue tracked separately).

About authentication: claude on darwin stores OAuth tokens in the
macOS Keychain (under `Claude Code-credentials`). The Keychain is a
darwin-only service; it isn't reachable from a linux container.
Claude on linux falls back to a JSON file at `~/.claude/.credentials.json`,
which IS bind-mounted into the tent. Run once after `claude /login`
on the host (and again on token rotation):

```sh
just debug-extract-claude-credentials
```

That triggers a Touch ID prompt, then writes the extracted credentials
to `~/.claude/.credentials.json`. Re-run when in-tent claude reports
`Not logged in · Please run /login`. clown#100's home-manager module
will eventually automate this.

About SSH agent forwarding: podman-machine's virtiofs/9p mount
layer cannot proxy AF_UNIX sockets through the host→VM filesystem
mount (containers/podman#23245, #23785). To get `$SSH_AUTH_SOCK`
into the tent, `dev-tent-machine-up` spawns a background
**ssh-agent forwarder**: it uses `podman machine ssh -- -R …` to
publish the host's socket at `/run/host-services/ssh-auth.sock`
inside the VM (the same well-known path used by Docker Desktop,
OrbStack, and Lima). `tent.BuildArgs` on darwin then bind-mounts
*that* in-VM path into the container. The forwarder exits when the
machine stops; `dev-tent-machine-down` kills it explicitly. If the
forwarder fails to start (e.g. `SSH_AUTH_SOCK` was unset), tent
degrades cleanly to "no agent" rather than emitting a broken bind.
You can also attach a forwarder to the eng-managed default machine:

```sh
nix run .#dev-tent-ssh-forward -- podman-machine-default
```

(holds a foreground `ssh -N`; Ctrl-C to tear it down).

Related issues:
- eng#107 — machine-name rename (closed; landed)
- eng#108 — bind `$HOME` into the VM (closed; landed)
- eng#110 — home-manager activation hook for managed-settings (open)
- eng#111 — activation drift detection (open)
- eng#112 — bind `/nix/var` into the VM (open)
- clown#95 — proper claude-code binary patch (open)
- clown#98 — podman command builder refactor (open)
- clown#99 — explore Lima/Colima for parallel VMs (spike landed
  at `zz-pocs/tent-lima/`; GO recommendation; see below)
- clown#100 — clown-exported home-manager module (partially landed:
  `homeManagerModules.tent-backend-lima`)

## Lima home-manager module: alternative to podman-machine

clown exports `homeManagerModules.tent-backend-lima` — a darwin
LaunchAgent that manages a Lima VM with the mount set and SSH agent
forwarding configuration `clown --tent` expects. The motivation came
out of the clown#99 spike (see `zz-pocs/tent-lima/README.md`):

- **Lima supports parallel VMs on darwin**, unlike podman-machine.
  No stop/swap dance.
- **`ssh.forwardAgent: true` replaces the bespoke `ssh -R` LaunchAgent
  bridge** clown currently runs alongside its dev-tent machine.
- **`nerdctl` is a near-drop-in for `podman run`** for the flag shape
  clown's tent code uses.

A downstream consumer (notably the eng `home-manager` config)
imports it via:

```nix
{ inputs, ... }: {
  imports = [ inputs.clown.homeManagerModules.tent-backend-lima ];
  services.tent-backend-lima = {
    enable = true;
    # Defaults match clown's tent mount expectations. Override if
    # needed:
    # machineName = "clown-tent";  # default
    # mounts = [ ... ];             # default mirrors the eng
    #                              # podman-darwin mount set
    # forwardAgent = true;          # default
  };
}
```

The module:

- Generates a `lima.yaml` from the option values (so the module is
  single-source-of-truth; no separate yaml to keep in sync).
- Hashes the rendered yaml and stores a sentinel under
  `~/.local/state/tent-backend-lima/<name>.hash`. On home-manager
  activation, if the hash differs (mount-list change, sizing change,
  etc.), the launcher **destroys and recreates the VM** — drift
  detection that eng#111 has been tracking on the podman side gets
  built in here from day one.
- Installs `pkgs.lima` into `home.packages` so `limactl` is on PATH
  for the user.
- Installs a LaunchAgent (`com.amarbel-llc.tent-backend-lima`) that
  runs the launcher at login: create-if-missing + start-if-not-
  running. `KeepAlive` only on crash; clean `limactl start` exits
  are respected.
- Refuses to activate on linux with a clear assertion message.

Note: this module manages the VM lifecycle only. clown's own tent
code (`internal/tent/tent.go`) still emits `podman run` argv today;
the migration to `nerdctl` is tracked as future work in clown#99.
Until then, the module is useful as either:

1. The backing VM for a future Lima-based clown tent backend
   (paired with backend-abstraction work in `cmd/clown/`).
2. A direct replacement for `programs.podman-darwin` on hosts where
   parallel-VM and built-in SSH agent forwarding outweigh the
   "clown's tent code still wants podman" coupling.

Cross-references: `zz-pocs/tent-lima/` (the spike), clown#99
(exploration issue), clown#100 (module-ownership tracking).

## Tent backend lever (podman ↔ lima)

`clown --tent` picks its container runtime at build time via the
`tentBackend` parameter on `mkClownGo` / `mkClownPkg` / `mkCircus` in
`flake.nix`. Two values are recognized:

- `"podman"` (default) — talk to podman directly. Uses
  `buildcfg.PodmanPath` and `buildcfg.PodmanMachineName` (which is
  passed as `--connection <name>` before the subcommand).
- `"lima"` — talk to Lima via `limactl shell <machine> -- sudo
  nerdctl ...`. Uses `buildcfg.LimactlPath` and reuses
  `buildcfg.PodmanMachineName` as the Lima instance name. nerdctl has
  no `--connection` equivalent, so the Lima backend drops it.

The Go side abstracts this via the `tent.Backend` interface
(`internal/tent/backend.go`) with three operations —
`ImageExistsArgs`, `LoadImageArgs`, `RunArgs` — and a `Binary()`
helper. `cmd/clown/main.go`'s `newBackend()` resolves
`buildcfg.TentBackend` to a concrete impl and threads it through
`ensureTentImage`, `runTentImageLoad`, and `tentExecutor`. All
mount-list construction lives in the backend-agnostic
`tent.BuildArgs`; both backends delegate to it.

Built packages:

- `packages.default` / `packages.dev` — podman backend (status quo).
- `packages.dev-lima` — lima backend; targets the `clown-tent` Lima
  instance created by `homeManagerModules.tent-backend-lima` (or by
  the local `clown-tent-lima` from `zz-pocs/tent-lima/`).

When adding a third backend, implement the `tent.Backend` interface,
wire its construction in `newBackend()`, add a `tentBackend` value to
the `lib.assertOneOf` in `flake.nix`, and emit any new ldflags from
`mkClownGo`'s `ldflagsList`.

**Forward-looking: TOML profile migration.** This build-time lever is
interim. clown's planned `--profile` work (see
`docs/plans/2026-04-23-profiles-design.md`) will own per-profile
`(provider, backend, model, env)` tuples, at which point `tentBackend`
becomes a profile field and `newBackend()` becomes the profile
resolution sink. The interface is intentionally minimal so that
migration is a wiring change, not a Go-API redesign.

**clownfile (RFC-0013 §1) — the per-instance config layer.** The
`clownfile` (TOML, discovered by ascending $PWD→$HOME via
`internal/clownfile`) is clown's cascading per-directory config. Its
`[profile]` table sets provider/backend/model/env defaults *beneath*
explicit flags/env (precedence `--provider` > `CLOWN_PROVIDER` > clownfile
> `buildcfg.DefaultProvider`): a clownfile provider also suppresses the
picker, `[profile].backend` is the runtime source `newBackend(backend)`
now reads (falling back to `buildcfg.TentBackend`), `[profile].model` is
injected as `--model` for the claude family, and `[profile.env]` is
exported only-if-unset (ambient wins). The named-profile registry
(`--profile`) is a separate mechanism that wins over clownfile defaults.
The `[attach]` table (RFC-0013 §1.3, clown#145) is the multiplexer
self-wrap layer: when `multiplexer` is `zmx` (not `none`), clown re-execs
itself under the configured `start`/`resume` template on boot
(`cmd/clown/attach.go` `maybeReexecMultiplexer`, gated in `runWithFlags`
after the per-instance id is resolved), pinning the id via the hidden
`--clown-attach-id` flag — an arg, not env, so it reuses the clown#136
hygiene and the inner run skips re-wrapping (loop guard). `{id}`/`{entry}`
substitute (`{entry}` splices clown's argv); `resume-title` (default `{group}`,
RFC-0014) emits an OSC-2 title resolving to the group-id, falling back to `{id}`
when ungrouped; `spawn` IS executed for a detached-worker launch when the
orchestrator passes the hidden `--clown-attach=spawn` arg (`clownfile.ModeSpawn`,
RFC-0014 §5: clown owns the detached-spawn executor — the resolved §1.3 open
question), while `spawn-window` stays schema-only. `CLOWN_ATTACH_FORCE=1`
overrides the interactive-TTY gate; a configured-but-uninstalled multiplexer
degrades to inline rather than erroring (clown#146), so the burned-in default is
safe on hosts without the mux. clown ships a **burned-in default clownfile**
(`default-clownfile` at the repo root, path burned in via
`buildcfg.DefaultClownfilePath`) carrying the `[attach]` defaults; `clownfile.Discover`
merges it as the lowest-precedence base, beneath the XDG user-global
`$XDG_CONFIG_HOME/clown/clownfile` (`~/.config/clown/clownfile`, clown#149) and the
`$PWD→$HOME` ascent. The burned-in `multiplexer` defaults to `"none"`, so a worktree
runs **inline** unless a higher-precedence `clownfile` opts into `multiplexer = "zmx"`
(then the burned-in `start`/`resume`/`spawn` templates are inherited); any user
`clownfile` overrides per key. Those zmx templates lock `spawn`/`spawn-entry`
to spinclass's real `[session-entry]` defaults; `start`/`resume` are clown-native.
`[attach].pty-suspend` (a `*bool`, default off, overridable per-clownfile) opts the
interactive provider run into the **escape-to-shell pty proxy** (`internal/ptysuspend`):
clown runs the provider on an inner pty and intercepts the escape key
(`[attach].escape-key`, caret notation, default `^Z`) BEFORE the raw-mode TUI swallows
it (claude takes `^Z` as a `0x1A` byte and never escapes), hands the terminal to
`[attach].escape-command` (argv, env-interpolated, empty ⇒ `$SHELL` in the worktree;
intended default `["sc","exec","${SPINCLASS_SESSION_ID}","$SHELL"]` once spinclass ships
`sc exec`), and resumes the provider on its exit (output paused via a mutex, repaint via
winsize-toggle + `SIGWINCH`). It is a **shell-out, not SIGTSTP** — so it works uniformly
inline AND inside a mux pane (clown owns the pane process either way), per Sasha's
both-modes decision (FDR-0017 ctrl-z recon). Gated in `runProvider` to interactive +
claude-family (not `--tent`/podman, not exec-replacing codex/`--naked`) + `Supported()`
(linux); resolved from the clownfile in `runWithFlags` via `resolvePtyOptions` into
`parsedFlags.ptyOpts` (`ptysuspend.Options`). The burned-in default flips to `true` once
dogfooded.
**The awareness seam (RFC-0014)**: `[attach].group-id` (env-interpolated, default
`"${SPINCLASS_SESSION_ID}"`) is exported as `CLOWN_GROUP_ID` and keys the group
chat channel, presence decoration, and the `{group}` title; clown no longer reads
`SPINCLASS_SESSION_ID` by name (`jobwake.GroupKey()` reads `CLOWN_GROUP_ID`),
`[attach].description` (`CLOWN_GROUP_DESCRIPTION`) is the presence label, and the
presence index is the clown→spinclass half spinclass consumes for liveness +
`sc list`. The spinclass `[session-entry]` takeover (spinclass dropping its mux
defaults + wiring `--clown-attach=spawn`/presence) is the lockstep spinclass-side
half (FDR-0017 Piece 1); remote attach stays spinclass. Man pages: `clownfile(5)`;
RFCs: RFC-0013 §1.3, RFC-0014.

## Architecture

The flake produces a `symlinkJoin` of five components:

1. **`clown` wrapper** (2-line shell script in `flake.nix`): Sets the
   `CLOWN_PLUGIN_META` env var (pointing at the build-time plugin metadata
   directory) and execs the Go binary.

2. **`clown-go`** (`cmd/clown/main.go`, Go binary): The main entrypoint.
   Parses `--provider` and dispatches to Claude or Codex. Walks from `$PWD`
   up to `$HOME` collecting `.circus/` directories for system prompt injection
   (via `internal/circus`). Two prompt modes:
   - **Replace**: Deepest `.circus/system-prompt` file wins
   - **Append**: All `.md` files from `.circus/system-prompt.d/` directories,
     shallowest-first, plus builtin fragments from `system-prompt-append.d/`
   - Per-provider safety defaults (via `internal/provider`):
     - Claude: `--disallowed-tools 'Bash(*)'` (plus `WebFetch`, `WebSearch`,
       `Write`, and others from the build-time disallowed-tools file). The
       built-in `Explore` subagent is NOT disallowed — the `clown-hook-allow`
       PreToolUse hook rewrites an `Agent(Explore)` launch to clown's read-only
       `Discover` subagent (`subagents/discover.md`) via `updatedInput`, so it
       is transparently redirected rather than blocked.
     - Codex: `--sandbox workspace-write`
   - Directly manages HTTP MCP server lifecycle for Claude plugins (via
     `internal/pluginhost`): discovers `clown.json` manifests, launches
     servers, reads handshakes, polls health endpoints, compiles replacement
     plugin manifests with URL-based MCP entries, and passes compiled
     plugin directories to Claude via `--plugin-dir`.
   - Build-time configuration injected via `-ldflags -X` into
     `internal/buildcfg` (provider CLI paths, version strings, agents file,
     system-prompt-append.d path).
   - clown ships the **unpatched** upstream `claude-code` binary and **no
     managed-settings** (clown#133). The former path-redirect strategy
     (patching the binary to read `$out/etc/claude`) was non-viable — it
     corrupted the darwin binary and patched the wrong code path, so claude
     never read the JSON outside `--tent` and the settings were inert
     everywhere. Their intent now lives on mechanisms claude actually reads:
     tool auto-allow via the plugin-hook (`clown-hook-allow` shipped through
     `--plugin-dir`, clown#130), Bash blocking via the
     `--disallowed-tools 'Bash(*)'` CLI flag, and identity/attribution via the
     system-prompt append (`00-identity.md`). Auto-mode/auto-memory blocking is
     **not** currently enforced (the inert managed-settings was its only prior
     home); re-homing it onto a working settings mechanism is a follow-up
     (clown#143).

   **Plugin manifest compilation.** When a plugin has both `clown.json`
   (HTTP MCP servers) and `.claude-plugin/plugin.json` (claude-native
   manifest), `clown` **compiles** each affected plugin dir into a temporary
   staging directory: top-level entries are symlinked back to the source, but
   `.claude-plugin/plugin.json` is rewritten with the `mcpServers` key
   replaced by URL-based entries pointing at the running HTTP servers. This
   preserves plugin identity (`plugin:<name>:<server>`) and original server
   names in Claude Code, which in turn preserves hook matching. Claude is
   handed the staged dir via `--plugin-dir`, so it still loads
   hooks/skills/commands/agents and registers the MCP servers as
   plugin-sourced. Staged dirs are cleaned up on shutdown. The
   `--disable-clown-protocol` flag (and `CLOWN_DISABLE_CLOWN_PROTOCOL=1` env
   var) bypasses the entire pipeline — plugin dirs are passed to claude
   unmodified, and claude's native MCP path is the only one active.

3. **`clown-plugin-host`** (`cmd/clown-plugin-host/main.go`, Go binary):
   Standalone lifecycle manager retained for backward compatibility.
   Functionally identical to `clown`'s built-in plugin management but
   invoked as a separate process with `--` separating its flags from the
   downstream command. `clown` no longer execs into `clown-plugin-host`.
   See `clown-plugin-host(1)` and `clown-json(5)`.

4. **`clown sessions-complete`** (built into the `clown` binary): emits one
   line per resumable session in fish completion format
   (`<clown://provider/id>\t<reldate>  <title-or-id>`). Pass `--pwd-only` to
   filter to sessions whose recorded cwd exactly matches `$PWD`. Used by
   the fish completion script to populate `clown resume` URI suggestions.
   Codex enumeration was dropped when `bin/clown-sessions` (Python) was
   removed; restoring it requires a Go SQLite reader (see issue #27).

5. **`clown-completions`** (`completions/clown.fish`): Provider-aware fish
   completions. Detects `--provider` on the command line (or `CLOWN_PROVIDER`
   env var) and offers Claude or Codex flags/subcommands accordingly.

   **Circus provider.** `--provider circus` runs Claude Code against a local
   `llama-server` daemon instead of Anthropic's API. `cmd/circus` manages the
   daemon lifecycle (pidfile/portfile at `~/.local/state/circus/`, log at
   `~/.local/state/circus/llama-server.log`). On start, if stdout is a pipe
   (i.e., clown launched it), circus emits a clown-protocol handshake
   (`1|1|tcp|<addr>|streamable-http`) and blocks until stdin closes. Clown
   reads the handshake, sets `ANTHROPIC_BASE_URL` to the local server address,
   and sets `ANTHROPIC_CUSTOM_MODEL_OPTION` to bypass Claude Code's model
   validation. `--model <name-or-path>` selects the GGUF model: absolute paths
   pass through; bare names are resolved from `~/.local/share/circus/models/<name>.gguf`.
   When `--model` is omitted (and `CIRCUS_MODEL` is unset), `cmd/clown/circus.go
   pickCircusModel` lists the models directory and either auto-picks (1 model),
   shows a `huh.NewSelect` picker (2+ models on a TTY), refuses with a hint to
   run `circus download` (0 models), or refuses non-interactively (2+ models, no
   TTY). The llama-server binary path is the only model-related ldflag burned
   in (`internal/buildcfg.LlamaServerPath`). A separate `nixpkgs-llama` flake
   input (pinned to nixpkgs master) provides a llama-cpp build with Anthropic
   Messages API support (`/v1/messages`), which predates the nixos-25.11 stable
   pin.

   **Model management.** `circus models` lists installed models (from
   `~/.local/share/circus/models/`). `circus download <name>` fetches a model
   from the baked-in registry (`cmd/circus/registry.json`, embedded via
   `go:embed`), validates SHA256, and installs it atomically via temp-file +
   rename. A charmbracelet/bubbles progress bar renders during download. The
   registry ships with Qwen3 and Gemma3 variants; SHA256 digests in the current
   registry are 64-zero placeholders pending real values from HuggingFace.

   **Opencode provider.** `--provider opencode` runs the `opencode` TUI against
   any OpenAI-compatible backend. Configuration is read from
   `~/.config/circus/opencode.toml` (fields: `url`, `token`), which is user-local
   and never committed to the repo. Clown writes a temporary `opencode.json`
   config (in a `mkdtemp` dir) and passes it to opencode via `XDG_CONFIG_HOME`,
   using the `@ai-sdk/openai-compatible` custom provider. The default model is
   `gpt-4o`; model limits are hardcoded in `cmd/clown/opencode.go`. The opencode
   binary path is burned in at build time via `internal/buildcfg.OpencodeCliPath`.

   **Crush provider.** `--provider crush` runs charmbracelet's crush against
   one of three backends (parallel to opencode's split): the Anthropic API
   passthrough (uses crush's builtin Anthropic provider, authenticates via
   `ANTHROPIC_API_KEY`), an OpenAI-compatible gateway configured via
   `~/.config/circus/crush.toml` (`url`, `token` — same TOML format as
   `opencode.toml`), or the local circus llama-server discovered through
   `~/.local/state/circus/portfile`. Clown writes a temporary `crush.json`
   to a `mkdtemp` dir and points crush at it via `CRUSH_GLOBAL_CONFIG`
   (the documented override env var). The config disables crush's Catwalk
   provider auto-update so launches are reproducible. The crush binary is
   pinned via the `numtide/llm-agents.nix` flake input and burned in via
   `internal/buildcfg.CrushCliPath`. Model defaults: `claude-sonnet-4-5`
   for the anthropic backend, `gpt-4o` for openai-compat. Profile names:
   `crush-anthropic`, `crush-local` (builtin); `crush-gateway` is
   user-defined because it requires URL/token. See `cmd/clown/crush.go`.

   **Profile system (planned).** A future `--profile <name>` flag (and
   `CLOWN_PROFILE` env var) will select named (provider, backend, model) tuples
   from `profiles/builtin.toml` (open, burned in) and
   `~/.config/circus/profiles.toml` (user-local, may contain URLs/tokens). The
   design doc is at `docs/plans/2026-04-23-profiles-design.md` and the
   implementation plan at `docs/plans/2026-04-23-profiles.md`.

   **Job-wakeup channel (`clown job` / `clown job-watch`).** A
   clown-provided background-job + agent-wakeup facility (`cmd/clown/job.go`,
   `cmd/clown/jobmonitor.go`, `internal/jobwake/`): a plugin defers a long task
   to the background and the originating (or a `--target`-ed) clown session is
   woken when it hits a terminal state. Two-layer design — a durable on-disk
   journal (`$XDG_STATE_HOME/clown/jobs/`, the at-least-once source of truth)
   plus a lossy UDS-datagram nudge for sub-second latency; only terminal events
   (`succeeded`/`failed`/`cancelled`/`interrupted`) wake, `started`/`progress`
   are journal-only. The per-instance session key resolves `CLOWN_SESSION_ID` →
   `CLAUDE_SESSION_ID` → a generated UUIDv4 (RFC-0013 §2.3 dropped
   `SPINCLASS_SESSION_ID` from routing — it is now the group decoration naming
   the group channel `ChannelID(SPINCLASS_SESSION_ID)` that every clown under a
   spinclass session watches). clown threads the resolved key EXPLICITLY rather
   than stamping `CLOWN_SESSION_ID` on its own process env (clown#136): per-child
   injection into each plugin MCP server it spawns (`pluginhost.Host.BaseEnv`)
   plus `clown job-watch --session <key>` baked into the synthesized monitor
   command — so the claude subtree (every `Bash` call and subagent) no longer
   inherits the key. An exec-replacing provider (codex / `--naked`) is the
   exception: it becomes the agent, so it receives the value via its env. Plugins
   consume it via the `clown job start|progress|done|read` producer/pull CLI;
   clown registers the `clown job-watch` monitor for the session automatically
   (synthesized `--plugin-dir`). `CLOWN_DISABLE_JOB_WAKEUP=1` is the kill switch. Contract:
   RFC-0009 (`docs/rfcs/0009-job-wakeup-channel.md`); feature treatment:
   FDR-0013 (`docs/features/0013-job-wakeup-channel.md`); man page:
   `clown-job(1)`. Status: `testing` (FDR-0013) — live-proven 2026-06-06 by
   two producers (moxy `get-hubbed.ci-watch`, spinclass async merge/check)
   plus cross-session directed and broadcast `message` wakes.

   **Job output spool + status probe (`clown job spool-path` / `clown job
   status`).** An observability layer over the channel (RFC-0010,
   `docs/rfcs/0010-job-output-spool-and-status.md`; `internal/jobwake/status.go`):
   a producer-written, append-only `<job-id>.out` spool sibling to the journal,
   and a journal+spool-derived status probe. `spool-path` resolves/prints the
   `.out` path (empty+exit0 when disabled, exit2 on a malformed id); `status`
   reports `{state, source, started, ended, elapsed_sec, last_activity,
   spool_bytes, progress, tail}` (`--json` or a human header + bounded tail),
   journal-derived only — it never infers producer liveness, so a hard-crashed
   producer reports `running` with a stale `last_activity` (the RFC-0009 §10
   gap). The spool is reaped with its journal (plus an age-gated orphan sweep).
   Both subcommands validate the job id through the same `validateJobID`
   choke point as the rest of the channel (clown#123). moxy is the motivating
   consumer (its FDR-0005, moxy#341); impl tracked in clown#122.

   **Job-platform MCP tools (`clown job-mcp`).** RFC-0011
   (`docs/rfcs/0011-job-platform-mcp-tools.md`; `cmd/clown/jobmcp.go`) exposes
   the platform to the agent directly as MCP tools — `job_start`,
   `job_progress`, `job_done`, `job_message`, `job_read`, `job_status`,
   `job_spool_path`, `job_wait` (the blocking join, clown#154) — each equivalent
   to the matching `clown job` subcommand
   (one `internal/jobwake` code path). `clown job-mcp` is a hand-rolled stdio
   JSON-RPC server; clown injects it by adding `stdioServers` entries to the
   synthesized `clown-builtin-jobs` plugin (alongside the job-watch monitor),
   which `clown-stdio-bridge` wraps to streamable-HTTP and clown's own
   pluginhost manages — clown self-consuming RFC-0002, no privileged path. The
   catalog is split (clown#144) into two intent-revealing servers via
   `clown job-mcp --surface <name>`: `ringmaster` (job control —
   `job_start`/`job_progress`/`job_done`/`job_read`/`job_status`/`job_spool_path`/`job_wait`,
   and it carries the dynamic system-prompt fragment) and `troupe` (messaging —
   `chat_send`/`chat_read`/`chat_list` and the standalone `job_message`). The
   tools surface as `plugin:clown-builtin-jobs:ringmaster` and
   `plugin:clown-builtin-jobs:troupe`; the auto-allow hook (clown#130) matches the
   plugin segment so both auto-allow. Invoked without `--surface` (direct/dev)
   the server exposes the whole catalog. Injected only for
   `--plugin-dir` providers with the bridge available (nix builds); absent in
   dev builds and when `CLOWN_DISABLE_JOB_WAKEUP=1`. The CLI stays the producer
   front-end; the MCP tools are the agent-facing surface that plugin-private
   job tools (spinclass `session-job-status`/chat, moxy `async-result` status)
   migrate onto. Status: `accepted` (RFC-0011), reviewed by both consumers.

   **Dynamic system-prompt fragments (RFC-0002 §5).** An *optional, runtime*
   upgrade from the build-time static plugin fragments (FDR-0003): a plugin
   server opts in (`httpServers.<n>.systemPromptPath`, or
   `stdioServers.<n>.systemPrompt: true` which Desugar maps to the fixed bridge
   path `/clown/system-prompt`), and after the server is healthy clown
   `GET`s that path and appends the body **last** into claude's
   `--append-system-prompt-file` — after identity/builtin/plugin-static/user
   content. The fetch is best-effort: a non-200/204/timeout degrades to static,
   never failing the launch (`internal/pluginhost` `Host.FetchPromptFragments`;
   the append seam is at the tail of `runManaged` in `cmd/clown/main.go`, fed the
   append-file path now returned by `provider.BuildClaudeArgs`). For a bridged
   stdio server, `clown-stdio-bridge` serves `/clown/system-prompt` by issuing an
   MCP `prompts/get` for the `system-prompt-append` prompt to the wrapped child —
   so the fragment is *child-owned* and computed at request time. The
   `clown-builtin-jobs` server is the dogfood: `clown job-mcp` exposes that prompt
   (`jobSystemPromptFragment`), returning a live orientation fragment built from
   its own tool catalog plus the per-instance session key injected via
   `Host.BaseEnv` (clown#136) — runtime state the static path can't express.
   Scope: the claude family only (providers that run the plugin host and read an
   append file); exec-replacing providers are unaffected. Man page:
   `clown-json(5)` (`systemPromptPath`, `systemPrompt`).

   **Cross-session chat (`clown chat`).** RFC-0013 §3 makes chat a clown
   construct on the job channel (rescope companion to spinclass FDR-0017). A chat
   message is a `chat` record (a waking type distinct from `message`, so the
   own-channel reap leaves it) carrying the one-line subject in the record and
   the full body in the message's RFC-0010 spool. `clown chat send|read|list`
   (CLI) and `chat_send`/`chat_read`/`chat_list` (the same `clown-builtin-jobs`
   server) are the surface: read aggregates the reader's own/group/broadcast
   channels with a per-clown cursor distinct from the wake ack; `list` reads a
   presence index the job-watch monitor registers per session
   (`$XDG_STATE_HOME/clown/presence/`, keyed by per-instance channel, refreshed
   on a ticker, `SPINCLASS_DESCRIPTION` as the readable label). The spinclass
   chat surface (`internal/chat`, `chat-*`, `chatroom`) is deleted in a lockstep
   hard-swap cutover (FDR-0017; accept-the-window). Man page: `clown-chat(1)`.

   **Operator job control (`ringmaster ls|status|tail|cancel`).** The
   `ringmaster` binary (`cmd/ringmaster`, FDR-0010's llama-server
   control-plane daemon) doubles as the human-facing control surface for
   the job channel (clown#124, `cmd/ringmaster/jobs.go`): `ls` lists a
   channel's jobs (`--all` spans every channel on the host), `status`
   mirrors `clown job status`, `tail [-f]` streams the output spool, and
   `cancel` writes the terminal `cancelled` record. All four read/append
   the on-disk journal+spool directly (no daemon required) over the same
   single `internal/jobwake` code path (`ListJobs`/`ListAllJobs`,
   `StatusOfChannel`, `ResolveSpoolChannel`, `DoneChannel`). `cancel` is
   **cooperative**: jobs aren't ringmaster-spawned and the journal carries
   no worker PID (an RFC-0010 decision), so it wakes the owning session's
   monitor and signals the producer to stop rather than killing an OS
   process. A job is addressed either by `--target <session-key>` (hashed
   to a channel) or by `--channel <id>` — the raw channel id `ls --all`
   prints — which lets an operator reach a job in a session whose key it
   doesn't hold (the channel id is a one-way hash, so `--target` can't get
   there); the two are mutually exclusive and `--channel` is validated as
   hex against path traversal (clown#125). Man page: `ringmaster(1)`.

## Nix Conventions

The `amarbel-llc/nixpkgs` fork migrated to a thin overlay flake on
2026-05-01 (`6e349594`); a `default.nix` shim auto-applies the overlay
on `import nixpkgs { ... }` (`ba254c0`). Our primary `nixpkgs` input
tracks fork master and consumes that shim. The four secondary nixpkgs
inputs are still pinned to **pre-migration** (full-fork) SHAs — that's
where their packages live as conventional `pkgs/by-name/` definitions.
Bumps are conservative: pick a newer pre-migration SHA, not the
thin-wrapper master tip.

- `nixpkgs` -> fork master (thin-wrapper era; overlay auto-applied)
- `nixpkgs-master` -> pinned pre-migration SHA, used for
  `pkgs-master.just`
- `nixpkgs-claude-code` -> pinned pre-migration SHA; `pkgs-claude-code`
  is now **unused** (claude-code comes from `pkgs-llm-agents.claude-code`,
  unpatched, since clown#133 removed the managed-settings binary patch).
  The input is kept for reference — see the comment on it in `flake.nix`.
- `nixpkgs-codex` -> pinned pre-migration SHA, used for
  `pkgs-codex.codex`
- `nixpkgs-llama` -> pinned pre-migration SHA for llama-cpp with
  `/v1/messages` support (PR #17570, merged 2025-11-28; not in
  nixos-25.11)
- `llm-agents` -> `numtide/llm-agents.nix`, source of the `crush`
  package (and many other AI coding agents). Its `inputs.nixpkgs`
  follows our main `nixpkgs` so we don't pull in a duplicate
  evaluation.

## Spinclass Integration

Worktree-based development managed by Spinclass. The `sweatfile` configures a
pre-merge hook that runs `just` (i.e., the full build) before merging a worktree
branch into master.
