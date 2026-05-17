{
  description = "clown — coding-agent wrapper";

  inputs = {
    # Main nixpkgs is the amarbel-llc fork at master. The fork's
    # overlays.default contributes buildGoApplication, mkGoEnv,
    # gomod2nix (CLI), fetchGgufModel, and other amarbel-packages
    # additions to pkgs. See overlays/amarbel-packages.nix in the fork.
    nixpkgs.url = "github:amarbel-llc/nixpkgs";
    # Secondary pinned views — same SHAs we used against upstream, just
    # served by the fork. Each fork commit upstream's master, so these
    # SHAs are reachable. The overlay is *not* applied to these because
    # they're narrow-purpose (claude-code, codex, llama-cpp at specific
    # versions) and don't need the fork's package additions.
    # nixpkgs-master sources the devshell's `just`. Pinned to upstream
    # NixOS/nixpkgs (not the fork) since this input is generic-purpose
    # and doesn't need the fork's overlay. Held at a pre-just-1.50.0
    # SHA while the cargo-vendor failure on `windows-sys-0.60.2.tar.gz`
    # gets resolved upstream (we tested `97b5957e`, 2026-04-20, the
    # 1.49.0 → 1.50.0 bump commit, and it errors out with `tar exit 2`
    # during the vendor build).
    nixpkgs-master.url = "github:NixOS/nixpkgs/9b53530a5f6887b6903cffeb8a418f3079d6698d";
    utils.url = "https://flakehub.com/f/numtide/flake-utils/0.1.102";
    # Claude Code is held at 2.1.111 (npm-source layout) because the
    # mkPatchedClaudeCode patchPhase substitutes inside
    # `lib/node_modules/@anthropic-ai/claude-code/cli.js` to redirect
    # /etc/claude-code to the managed-settings store path. Upstream
    # restructured to a native binary distribution at 2026-04-18, after
    # which `cli.js` no longer exists. Bumping past 2.1.111 needs the
    # patch logic ported to binary-string substitution.
    nixpkgs-claude-code.url = "github:amarbel-llc/nixpkgs/b2b9662ffe1e9a5702e7bfbd983595dd56147dbf";
    nixpkgs-codex.url = "github:amarbel-llc/nixpkgs/0de8465d2b54ddd962422706d932c3354b4237ec";
    # llama-cpp with Anthropic Messages API (/v1/messages) support — requires
    # PR #17570 (merged 2025-11-28). Build 6981 in nixos-25.11 predates it.
    nixpkgs-llama.url = "github:amarbel-llc/nixpkgs/c0df0d088ab33122b402ea31cb5f7e1df7536036";
    # numtide/llm-agents.nix is the upstream Nix packaging for charmbracelet's
    # crush (and other AI coding agents). We pin it as a flake input so the
    # crush binary path can be burned into clown via `-X buildcfg.CrushCliPath`.
    llm-agents.url = "github:numtide/llm-agents.nix";
    llm-agents.inputs.nixpkgs.follows = "nixpkgs";
    treefmt-nix.url = "github:numtide/treefmt-nix";
    treefmt-nix.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs =
    {
      self,
      nixpkgs,
      nixpkgs-master,
      nixpkgs-claude-code,
      nixpkgs-codex,
      nixpkgs-llama,
      llm-agents,
      treefmt-nix,
      utils,
    }:
    utils.lib.eachDefaultSystem (
      system:
      let
        # The fork's default.nix shim auto-applies its overlay on
        # `import nixpkgs { ... }`, so pkgs gets buildGoApplication,
        # mkGoEnv, gomod2nix (CLI), fetchGgufModel, etc. without an
        # explicit overlays pass. The overlay also pins claude-code
        # at the package level — we route claude-code through
        # pkgs-claude-code (separate input pinned to a pre-shim SHA,
        # so no auto-apply) to keep that pin from overriding our
        # chosen version.
        pkgs = import nixpkgs {
          inherit system;
        };
        pkgs-master = import nixpkgs-master {
          inherit system;
          config.allowUnfree = true;
        };
        pkgs-claude-code = import nixpkgs-claude-code {
          inherit system;
          config.allowUnfree = true;
        };
        pkgs-codex = import nixpkgs-codex {
          inherit system;
          config.allowUnfree = true;
        };
        pkgs-llama = import nixpkgs-llama { inherit system; };
        # llm-agents.nix exposes per-system package outputs; we use its
        # crush package for the crush provider's CLI. Inputs.nixpkgs
        # follows our main `nixpkgs` so we don't pull in a duplicate
        # nixpkgs evaluation.
        pkgs-llm-agents = llm-agents.packages.${system};
        # `nix fmt` entry point. Config lives in ./treefmt.nix.
        treefmtEval = treefmt-nix.lib.evalModule pkgs ./treefmt.nix;
      in
      let
        lib = pkgs.lib;

        # Subagent definitions use TOML frontmatter (+++ delimiters) so Nix
        # can parse config natively via builtins.fromTOML. The markdown body
        # after the closing +++ becomes the agent's system prompt.
        parseAgent =
          file:
          let
            raw = builtins.readFile file;
            parts = builtins.split "\\+\\+\\+" raw;
            # split yields: ["", [], "\ntoml\n", [], "\n\nbody"]
            config = builtins.fromTOML (builtins.elemAt parts 2);
            prompt = builtins.elemAt parts 4;
          in
          {
            name = config.name;
            value = {
              inherit (config) description tools model;
              inherit prompt;
            };
          };

        agentFiles = builtins.attrNames (builtins.readDir ./subagents);
        agents = builtins.listToAttrs (
          map (f: parseAgent (./subagents + "/${f}")) (builtins.filter (f: lib.hasSuffix ".md" f) agentFiles)
        );
        agents-json = builtins.toJSON agents;
        agents-file = pkgs.writeText "clown-agents.json" agents-json;

        disallowed-tools-file = pkgs.writeText "disallowed-tools.txt" ''
          Bash(*)
          Agent(Explore)
          WebFetch
          WebSearch
          Write
          EnterWorktree
          NotebookEdit
          PowerShell
          LSP
          Glob
          Grep
        '';

        clownVersion = lib.trim (builtins.readFile ./version.txt);
        clownRev = self.rev or self.dirtyRev or "dirty";
        clownShortRev = self.shortRev or self.dirtyShortRev or "dirty";
        claudeCodeVersion = pkgs-claude-code.claude-code.version;
        claudeCodeRev = nixpkgs-claude-code.rev or "dirty";
        codexVersion = pkgs-codex.codex.version;
        codexRev = nixpkgs-codex.rev or "dirty";

        # Whole-tree date harvested from flake metadata. Dirty trees also
        # yield a value (current time), so pilot builds of uncommitted
        # edits stamp with today rather than leaving the sentinel literal.
        # Format: YYYYMMDDhhmmss. Converted to mdoc's "Month D, YYYY"
        # convention below, substituted into @MDOCDATE@ at manpage build
        # time.
        flakeDate = self.lastModifiedDate or "19700101000000";
        flakeYear = builtins.substring 0 4 flakeDate;
        flakeMonth = builtins.substring 4 2 flakeDate;
        flakeDay = builtins.substring 6 2 flakeDate;
        monthNames = {
          "01" = "January";
          "02" = "February";
          "03" = "March";
          "04" = "April";
          "05" = "May";
          "06" = "June";
          "07" = "July";
          "08" = "August";
          "09" = "September";
          "10" = "October";
          "11" = "November";
          "12" = "December";
        };
        mdocDate = "${monthNames.${flakeMonth}} ${toString (lib.toIntBase10 flakeDay)}, ${flakeYear}";

        buildGoApplication = pkgs.buildGoApplication;

        goSrc = lib.fileset.toSource {
          root = ./.;
          fileset = lib.fileset.unions [
            ./go.mod
            ./gomod2nix.toml
            ./cmd
            ./internal
          ];
        };

        # Source fileset for the synthetic-plugin derivation. Explicit
        # allowlist excludes the source-tree bin/ (which historically
        # accumulated stale Go build outputs) so the derivation hash
        # stays stable across worktree state.
        syntheticPluginSrc = lib.fileset.toSource {
          root = ./tests/synthetic-plugin;
          fileset = lib.fileset.unions [
            ./tests/synthetic-plugin/clown.json
            ./tests/synthetic-plugin/.claude-plugin
            ./tests/synthetic-plugin/.clown-plugin
            ./tests/synthetic-plugin/agents
          ];
        };

        clown-plugin-host = buildGoApplication {
          pname = "clown-plugin-host";
          version = clownVersion;
          src = goSrc;
          subPackages = [ "cmd/clown-plugin-host" ];
          modules = ./gomod2nix.toml;
          ldflags = [
            "-s"
            "-w"
            "-X main.version=${clownVersion}"
            "-X main.commit=${clownRev}"
            "-X github.com/amarbel-llc/clown/internal/buildcfg.StdioBridgePath=${clown-stdio-bridge}/bin/clown-stdio-bridge"
          ];
        };

        clown-stdio-bridge = buildGoApplication {
          pname = "clown-stdio-bridge";
          version = clownVersion;
          src = goSrc;
          subPackages = [ "cmd/clown-stdio-bridge" ];
          modules = ./gomod2nix.toml;
          ldflags = [
            "-s"
            "-w"
            "-X main.version=${clownVersion}"
            "-X main.commit=${clownRev}"
          ];
        };

        # Mock stdio MCP server used by the test-stdio-bridge integration
        # test. Built as a derivation so the test recipe consumes a store
        # path instead of dropping a binary into the worktree. The
        # buildGoApplication output is wrapped in runCommand to preserve
        # the historical "mock-stdio-mcp" binary name (Go's default
        # would be "mockstdiomcp" — the leaf of the subPackage path).
        mock-stdio-mcp-go = buildGoApplication {
          pname = "mock-stdio-mcp";
          version = clownVersion;
          src = goSrc;
          subPackages = [ "internal/pluginhost/testdata/mockstdiomcp" ];
          modules = ./gomod2nix.toml;
          ldflags = [
            "-s"
            "-w"
          ];
        };

        mock-stdio-mcp = pkgs.runCommand "mock-stdio-mcp" { } ''
          mkdir -p $out/bin
          cp ${mock-stdio-mcp-go}/bin/mockstdiomcp $out/bin/mock-stdio-mcp
        '';

        # Shebang-patched copy of the inspect-compiled helper. The
        # devshell script uses `#!/usr/bin/env bash` for portability,
        # but the nix sandbox has no /usr/bin/env. Lifted from
        # bats.nix so both the bats lane and clown-cover's
        # coverIntegrationCommand reference the same staged helper.
        inspectCompiledPatched = pkgs.runCommand "inspect-compiled" { } ''
          cp ${./tests/scripts/inspect-compiled} $out
          chmod +x $out
          patchShebangs $out
        '';

        # Unified buildGoApplication holding the three binaries the
        # bats integration suite invokes (clown-plugin-host,
        # clown-stdio-bridge, mock-stdio-mcp). Used as the `base` of
        # buildGoCover — pkgs.buildGoCover rebuilds this with `-cover`
        # and runs coverIntegrationCommand against the instrumented
        # output. The rename in postInstall mirrors what the standalone
        # `mock-stdio-mcp` runCommand does (Go's leaf-name policy
        # produces `mockstdiomcp`; the test contract expects
        # `mock-stdio-mcp`).
        #
        # No buildcfg.StdioBridgePath ldflag here: the bats suite never
        # exercises the code path that consumes it (neither test makes
        # plugin-host spawn a stdio-bridge), so an empty value is fine.
        clown-bats-bins = buildGoApplication {
          pname = "clown-bats-bins";
          version = clownVersion;
          src = goSrc;
          subPackages = [
            "cmd/clown-plugin-host"
            "cmd/clown-stdio-bridge"
            "internal/pluginhost/testdata/mockstdiomcp"
          ];
          modules = ./gomod2nix.toml;
          ldflags = [
            "-s"
            "-w"
            "-X main.version=${clownVersion}"
            "-X main.commit=${clownRev}"
          ];
          postInstall = ''
            mv $out/bin/mockstdiomcp $out/bin/mock-stdio-mcp
          '';
        };

        # clown-cover: bats-suite coverage of clown-bats-bins.
        # buildGoCover rebuilds clown-bats-bins with `go build -cover`,
        # runs coverIntegrationCommand under a fresh $GOCOVERDIR, and
        # persists the textfmt profile to $out/coverage.out (plus
        # binary covdata fragments under $out/covdata/).
        #
        # View the report with `go tool cover -html=result/coverage.out`
        # or `just cover-bats-html`. Distinct from `go test -cover`,
        # which only measures unit-test reachability — this lane shows
        # what code paths the bats integration suite exercises through
        # the real CLI.
        #
        # No corresponding `clown-go-cover` (unit) lane today; merging
        # the two via `go tool covdata merge` is a future addition.
        clownCoverIntegrationCommand = ''
          mkdir -p stage/zz-tests_bats
          cp -r ${./tests/bats}/* stage/zz-tests_bats/
          cp ${inspectCompiledPatched} stage/zz-tests_bats/inspect-compiled
          chmod -R u+w stage

          export CLOWN_PLUGIN_HOST_BIN="$out/bin/clown-plugin-host"
          export CLOWN_STDIO_BRIDGE_BIN="$out/bin/clown-stdio-bridge"
          export MOCK_STDIO_MCP_BIN="$out/bin/mock-stdio-mcp"
          export SYNTHETIC_PLUGIN_DIR="${synthetic-plugin}"

          cd stage/zz-tests_bats
          ${pkgs.bats}/bin/bats \
            --jobs $NIX_BUILD_CORES \
            *.bats
          cd "$NIX_BUILD_TOP"
        '';

        clown-cover = pkgs.buildGoCover {
          base = clown-bats-bins;
          extraNativeInstallCheckInputs = with pkgs; [
            curl
            jq
            coreutils
          ];
          coverIntegrationCommand = clownCoverIntegrationCommand;
        };

        # Compiled binary that the synthetic-plugin derivation embeds.
        # Not exposed as a top-level package — consumers should use
        # synthetic-plugin instead, which lays out the full plugin dir.
        mock-mcp-server-go = buildGoApplication {
          pname = "mock-mcp-server";
          version = clownVersion;
          src = goSrc;
          subPackages = [ "internal/pluginhost/testdata/mockserver" ];
          modules = ./gomod2nix.toml;
          ldflags = [
            "-s"
            "-w"
          ];
        };

        # Synthetic plugin used by the test-plugin-host integration test.
        # Combines the static fixture (clown.json, agents, plugin
        # metadata) with a compiled mock-mcp-server binary. The plugin
        # host receives this store path as --plugin-dir.
        #
        # The source clown.json declares the mock server's command as
        # the relative path "bin/mock-mcp-server"; substituteInPlace
        # rewrites that to the absolute store path at build time so the
        # manifest is CWD-independent. --replace-fail errors if the
        # pattern is missing, catching drift in source clown.json edits.
        synthetic-plugin = pkgs.runCommand "synthetic-plugin" { } ''
          mkdir -p $out
          cp -r ${syntheticPluginSrc}/. $out/
          chmod -R u+w $out
          mkdir -p $out/bin
          cp ${mock-mcp-server-go}/bin/mockserver $out/bin/mock-mcp-server
          substituteInPlace $out/clown.json \
            --replace-fail 'bin/mock-mcp-server' "$out/bin/mock-mcp-server"
        '';

        # PreToolUse hook that auto-allows Read/Glob/Grep against
        # /nix/store paths. Wired into mkClownManagedSettings below so
        # every clown-launched claude session inherits the allow.
        clown-hook-allow = buildGoApplication {
          pname = "clown-hook-allow";
          version = clownVersion;
          src = goSrc;
          subPackages = [ "cmd/clown-hook-allow" ];
          modules = ./gomod2nix.toml;
          ldflags = [
            "-s"
            "-w"
          ];
        };

        # Build-time defaults baked into the standalone packages.default
        # and into mkClownPkg / mkCircus when no override is given. Match
        # the historical hardcoded "claude" provider and the builtin
        # claude-anthropic profile (provider=claude, backend=anthropic).
        defaultDefaultProvider = "claude";
        defaultDefaultProfile = "claude-anthropic";

        # tent: podman-wrapped provider container. FDR-0007.
        # Tracer-bullet scope: claude only, opt-in via --tent.
        #
        # Three pieces of build-time state, each with its own platform
        # story:
        #
        # 1. **The container image** (`dockerTools.buildLayeredImage`).
        #    Builds on every platform, but on darwin produces a manifest
        #    that *claims* linux-arm64 while wrapping mach-O binaries —
        #    so it's only useful when the eval system is itself
        #    aarch64-linux. We expose the image as
        #    `packages.aarch64-linux.tent-image` (see the `packages`
        #    block) so darwin hosts can build it via nix-darwin's
        #    linux-builder, but skip baking `tentImageTarball` on darwin
        #    so a regular `nix build` doesn't bake a dangling path. The
        #    POC's `phase4-load-image` recipe does the cross-build and
        #    `podman load` explicitly.
        #
        # 2. **The podman binary** (`pkgs.podman`). Builds and runs on
        #    both linux and darwin — on darwin the binary is a thin
        #    client that proxies to a podman-machine VM. We bake it
        #    unconditionally. `tentPodmanEnabled` is the gate.
        #
        # 3. **The unpatched claude-code binary** (`pkgs-llm-agents
        #    .claude-code`, a fetchurl + binary install). Builds on
        #    every platform, baked unconditionally. `tentClaudeEnabled`
        #    is the gate.
        #
        # The legacy `tentImageEnabled` (linux-only) stays as the gate
        # for `tentImageTarball` and the conditionally-exposed
        # `packages.X.tent-image`.
        tentImageEnabled = pkgs.stdenv.isLinux;
        tentPodmanEnabled = true;
        tentClaudeEnabled = true;
        tentImage =
          if tentImageEnabled then
            pkgs.dockerTools.buildLayeredImage {
              name = "clown-tent";
              tag = clownVersion;
              # The image deliberately does not bake claude in —
              # /nix/store is bind-mounted read-only at runtime and
              # the claude binary is referenced by its store path.
              # The image only needs the tiny set of utilities that
              # podman expects to find inside (and CA certs for
              # HTTPS to api.anthropic.com).
              contents = [
                pkgs.bashInteractive
                pkgs.coreutils
                pkgs.cacert
                pkgs.iana-etc
              ];
              config = {
                Env = [
                  "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
                  "NIX_SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
                  # Pin the in-container locale to C.UTF-8 (built into
                  # glibc 2.35+, no locale archive needed). Avoids the
                  # `setlocale: cannot change locale (en_US.UTF-8)`
                  # warnings every subshell prints when the host's
                  # LC_ALL/LANG leak in but no locale archive is
                  # reachable inside the namespace. The tent.go
                  # DefaultEnvPassthrough deliberately omits LC_ALL
                  # and LANG so these defaults win.
                  "LC_ALL=C.UTF-8"
                  "LANG=C.UTF-8"
                ];
              };
            }
          else
            null;
        # tentImageRef is just a string ("clown-tent:<version>"); bake
        # it on every platform so the runtime knows what tag to look
        # up. On darwin where no tarball is wired in, an image-load
        # miss produces a clear "image not present locally and no
        # tarball is wired in" error instead of an opaque empty-ref
        # failure earlier in the chain.
        tentImageRef = "clown-tent:${clownVersion}";
        tentImageTarball = if tentImageEnabled then "${tentImage}" else "";
        tentPodmanPath = if tentPodmanEnabled then "${pkgs.podman}/bin/podman" else "";

        # tent runs an *unpatched* claude-code from numtide/llm-agents.nix
        # so the inner ring has no managed-settings shim — tent is the
        # boundary. The patched 2.1.111 (npm-source, cli.js-redirected
        # via mkPatchedClaudeCode) stays the default for un-tented clown;
        # see flake.nix:600-621 and FDR-0007 for the rationale. To bump
        # the tent's claude-code, run `nix flake update llm-agents`.
        #
        # The binary baked here runs *inside the linux container*, so it
        # MUST be the linux variant of claude-code regardless of the
        # host system that built clown. On a darwin host, sourcing the
        # path from `llm-agents.packages.<aarch64-darwin>.claude-code`
        # produces a mach-O wrapper script with a darwin-bash shebang
        # that the container's exec rejects with `Exec format error`.
        # Map every system to its linux counterpart and source from
        # there — on darwin this requires nix-darwin's linux-builder
        # (see rcm/tag-darwin/config/nix-darwin/modules/system.nix and
        # the eng-side FDR-0003).
        tentClaudeSystem =
          {
            "aarch64-darwin" = "aarch64-linux";
            "x86_64-darwin" = "x86_64-linux";
            "aarch64-linux" = "aarch64-linux";
            "x86_64-linux" = "x86_64-linux";
          }
          .${system};
        # The top-level `tentClaudeEnabled` above is the default; mkClownGo
        # (and through it, mkClownPkg and mkCircus) takes an
        # `enableTentClaude` parameter that overrides it per-circus. Callers
        # that need to avoid the linux claude-code closure on a darwin
        # builder (e.g. CI without a Linux builder) pass false.
        mkClownGo =
          {
            defaultProvider ? defaultDefaultProvider,
            defaultProfile ? defaultDefaultProfile,
            enableTentClaude ? tentClaudeEnabled,
          }:
          let
            tentClaudeCliPath =
              if enableTentClaude then
                "${llm-agents.packages.${tentClaudeSystem}.claude-code}/bin/claude"
              else
                "";
          in
          buildGoApplication {
            pname = "clown";
            version = clownVersion;
            src = goSrc;
            subPackages = [ "cmd/clown" ];
            modules = ./gomod2nix.toml;
            ldflags = [
              "-s"
              "-w"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.ClaudeCliPath=${claudeCliPath}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.CodexCliPath=${codexCliPath}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.CircusCliPath=${circus-go}/bin/circus"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.AgentsFile=${agents-file}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.DisallowedToolsFile=${disallowed-tools-file}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.SystemPromptAppendD=${./system-prompt-append.d}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.Version=${clownVersion}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.Commit=${clownRev}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.ClaudeCodeVersion=${claudeCodeVersion}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.ClaudeCodeRev=${claudeCodeRev}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.CodexVersion=${codexVersion}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.CodexRev=${codexRev}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.OpencodeCliPath=${pkgs.opencode}/bin/opencode"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.CrushCliPath=${pkgs-llm-agents.crush}/bin/crush"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.ClownboxCliPath=${clownboxCliPath}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.StdioBridgePath=${clown-stdio-bridge}/bin/clown-stdio-bridge"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.DefaultProvider=${defaultProvider}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.DefaultProfile=${defaultProfile}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.PodmanPath=${tentPodmanPath}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.TentImageRef=${tentImageRef}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.TentImageTarball=${tentImageTarball}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.ClaudeTentCliPath=${tentClaudeCliPath}"
            ];
          };

        circus-go = buildGoApplication {
          pname = "circus";
          version = clownVersion;
          src = goSrc;
          subPackages = [ "cmd/circus" ];
          modules = ./gomod2nix.toml;
          ldflags = [
            "-s"
            "-w"
            "-X github.com/amarbel-llc/clown/internal/buildcfg.LlamaServerPath=${llamaServerPath}"
          ];
        };

        # Managed settings burned into the patched claude-code derivation.
        # Lives at the highest precedence tier, so it cannot be overridden by
        # user settings, project settings, or CLI flags. See
        # claude-code-settings(5) for the precedence chain.
        #
        # The `allowBypass` flag controls whether bypass-permissions mode
        # (i.e. `--dangerously-skip-permissions`) is permitted. Naked clown
        # leaves it disabled (no YOLO mode without an external safety net).
        # Clownbox enables it because the bubblewrap sandbox is the safety
        # net — bypassing claude's per-tool prompts is the whole point.
        mkClownManagedSettings =
          {
            allowBypass ? false,
          }:
          pkgs.writeText "clown-managed-settings.json" (
            builtins.toJSON {
              permissions = {
                # Block auto-mode (no prompts, classifier-gated tool calls).
                # Orthogonal to sandboxing — kept on for both variants.
                disableAutoMode = "disable";
              }
              // lib.optionalAttrs (!allowBypass) {
                # Block --dangerously-skip-permissions and bypassPermissions
                # mode. Sandboxed clownbox omits this so its inner claude can
                # actually run in YOLO mode within the sandbox.
                disableBypassPermissionsMode = "disable";
              }
              // {
                # Hard denylist of destructive Bash patterns. Belt-and-
                # suspenders even inside the sandbox: the bind-mount writes
                # the repo, so `rm -rf *` is still destructive within scope.
                deny = [
                  "Bash(rm -rf *)"
                  "Bash(sudo *)"
                  "Bash(curl * | sh)"
                  "Bash(wget * | sh)"
                ];
              };
              # Disable auto-memory. The feature persists cross-session
              # learnings under ~/.claude/projects/<project>/memory/ and
              # auto-loads MEMORY.md into every session's context. Managed-
              # tier setting; per docs, cannot be overridden by user, project,
              # local, or CLI scopes. Applies to both naked and sandboxed
              # variants — orthogonal to bypass-permissions posture.
              autoMemoryEnabled = false;
              # Replace Claude's stock commit/PR attribution with clown's. The
              # system-prompt append (00-identity.md) still tells the model to
              # sign off in chat and non-git contexts; these keys enforce the
              # footer at the CLI level where the prompt can't reach.
              attribution = {
                commit = "Co-Authored-By: Clown <https://github.com/amarbel-llc/clown>";
                pr = "🤡 Generated with [Clown](https://github.com/amarbel-llc/clown)";
              };
              # Auto-allow Read/Glob/Grep against /nix/store paths. The
              # CLI-level --allowed-tools "Read(/nix/store/**)" form is not
              # honored by claude-code 2.1 for the Read tool (verified
              # empirically in 2026-04), so we use a PreToolUse hook
              # instead. The hook returns "allow" only when the relevant
              # path argument is rooted in /nix/store/, and "defer"
              # otherwise — leaving every other permission decision
              # untouched.
              #
              # Schema: each PreToolUse entry must wrap its handlers in a
              # { matcher, hooks } object. The matcher is a regex over the
              # tool name; without it the entry is silently dropped and
              # the hook never fires. The claude-code-hooks(5) example
              # showing a flat [{type, command}] array omits the matcher
              # wrapper and does NOT work in 2.1.
              hooks = {
                PreToolUse = [
                  {
                    matcher = "Read|Glob|Grep";
                    hooks = [
                      {
                        type = "command";
                        command = "${clown-hook-allow}/bin/clown-hook-allow";
                      }
                    ];
                  }
                ];
              };
            }
          );

        clownManagedSettings = mkClownManagedSettings { allowBypass = false; };

        # Patch upstream claude-code to read its managed-settings from a path
        # under its own $out instead of /etc/claude-code, then ship the
        # settings file alongside. This guarantees auto-mode is disabled for
        # every clown invocation without requiring writes to /etc.
        #
        # The "/etc/claude-code" string is hardcoded in cli.js — no env var,
        # no flag. --replace-fail breaks the build if Anthropic ever moves
        # the string, so we catch upgrade drift loudly.
        patchClaudeCodeManagedPath =
          replacement:
          pkgs-claude-code.claude-code.overrideAttrs (old: {
            # The upstream npm tarball places cli.js at the source root.
            # After install, it lands under lib/node_modules/@anthropic-ai/
            # claude-code/cli.js, but patchPhase sees the source layout.
            # Double-quote the replacement so $out expands in bash.
            postPatch = (old.postPatch or "") + ''
              substituteInPlace cli.js \
                --replace-fail '/etc/claude-code' "${replacement}"
            '';
          });

        # Apply the shipped managed-settings file to a path-patched
        # claude-code derivation. Parameterized by which managed-settings
        # JSON to ship — strict for naked clown, permissive for clownbox.
        mkPatchedClaudeCode =
          managedSettings:
          (patchClaudeCodeManagedPath "$out/etc/claude").overrideAttrs (old: {
            postInstall = (old.postInstall or "") + ''
              mkdir -p "$out/etc/claude"
              cp ${managedSettings} "$out/etc/claude/managed-settings.json"
            '';

            doInstallCheck = true;
            installCheckPhase = ''
              cli=$out/lib/node_modules/@anthropic-ai/claude-code/cli.js
              if grep -q '/etc/claude-code' "$cli"; then
                echo "FAIL: /etc/claude-code still present after patch" >&2
                exit 1
              fi
              if ! grep -q "$out/etc/claude" "$cli"; then
                echo "FAIL: patched path $out/etc/claude missing from cli.js" >&2
                exit 1
              fi
              test -f "$out/etc/claude/managed-settings.json"
            '';
          });

        patchedClaudeCode = mkPatchedClaudeCode clownManagedSettings;

        claudeCliPath = "${patchedClaudeCode}/bin/claude";
        codexCliPath = "${pkgs-codex.codex}/bin/codex";
        llamaServerPath = "${pkgs-llama.llama-cpp}/bin/llama-server";

        # clownbox provider is currently disabled at the build level to
        # avoid pulling in the extra patched-claude-code closure plus the
        # numtide/claudebox source. The Go runtime treats an empty
        # ClownboxCliPath as "provider unavailable" and errors at dispatch.
        # To re-enable, restore the claudebox-src flake input,
        # patchedClownboxSrc / clownbox / clownboxCliPath derivations, the
        # sandboxed managed-settings variant, and the ldflag below.
        clownboxCliPath = "";

        # Thin wrapper: sets CLOWN_PLUGIN_META (varies per mkCircus) then
        # execs the Go binary. All flag parsing, provider routing, and
        # plugin-host orchestration live in cmd/clown. The wrapped Go
        # binary varies by (defaultProvider, defaultProfile) — those
        # flags are linker-baked, so each combination is its own
        # derivation.
        mkClownBin =
          {
            pluginMeta,
            clownGoBin,
          }:
          pkgs.writeShellScriptBin "clown" ''
            export CLOWN_PLUGIN_META="${pluginMeta}"
            exec "${clownGoBin}/bin/clown" "$@"
          '';

        clown-completions = pkgs.runCommand "clown-completions" { } ''
          mkdir -p $out/share/fish/vendor_completions.d
          cp ${./completions/clown.fish} $out/share/fish/vendor_completions.d/clown.fish
        '';

        # Clown-owned pages use the @MDOCDATE@ sentinel in .Dd; we stamp
        # them with mdocDate (derived from self.lastModifiedDate) at
        # build time. Codex vendored pages keep their upstream dates.
        clown-manpages =
          pkgs.runCommand "clown-manpages"
            {
              inherit mdocDate;
            }
            ''
              for section in 1 5 7; do
                mkdir -p $out/share/man/man$section
              done
              cp ${./man/man1}/*.1 $out/share/man/man1/
              cp ${./man/man5}/*.5 $out/share/man/man5/
              cp ${./man/man7}/*.7 $out/share/man/man7/
              chmod -R u+w $out/share/man
              for page in \
                  $out/share/man/man1/clown.1 \
                  $out/share/man/man1/clown-plugin-host.1 \
                  $out/share/man/man1/clown-stdio-bridge.1 \
                  $out/share/man/man5/clown-json.5 \
                  $out/share/man/man7/clown-plugin-protocol.7; do
                  sed -i "s/@MDOCDATE@/$mdocDate/g" "$page"
                  if grep -q '@MDOCDATE@' "$page"; then
                      echo "clown-manpages: @MDOCDATE@ left unsubstituted in $page" >&2
                      exit 1
                  fi
              done
            '';

        # The installCheckPhase on patchedClaudeCode (above) verifies at the
        # string level that cli.js no longer contains /etc/claude-code and
        # does contain the patched store path. A runtime test that confirms
        # claude actually *loads* settings from the patched path would be
        # stronger, but claude-code 2.1.111 does not expose managed settings
        # in any externally observable output (diagnostics, --debug, or
        # subcommand output). If a future version adds a settings-dump
        # subcommand or surfaces deny patterns in debug output, a runtime
        # sentinel test should be added here.
        managedSettingsReadTest = patchedClaudeCode;

        emptyPluginMeta = pkgs.runCommand "clown-empty-plugin-meta" { } ''
          mkdir -p $out
          touch $out/plugin-dirs
          touch $out/plugin-fragment-dirs
          touch $out/version-info
        '';

        resolvePlugins =
          plugins:
          let
            hasGlob = s: builtins.match ".*[*?\\[].*" s != null;

            pluginBlocks = lib.concatMapStringsSep "\n" (
              plugin:
              let
                pkg = plugin.flake.packages.${system}.default;
                flakeName = pkg.name;
                flakeRev = plugin.flake.rev or plugin.flake.dirtyRev or "dirty";
                dirBlocks = lib.concatMapStringsSep "\n" (
                  dir:
                  if hasGlob dir then
                    ''
                      glob_count=0
                      for candidate in ${pkg}/${dir}; do
                        if [[ -d "$candidate/.claude-plugin" ]] && [[ -f "$candidate/.claude-plugin/plugin.json" ]]; then
                          echo "$candidate" >> $out/plugin-dirs
                          pname=$(${pkgs.jq}/bin/jq -r '.name // empty' "$candidate/.claude-plugin/plugin.json")
                          pver=$(${pkgs.jq}/bin/jq -r '.version // "-"' "$candidate/.claude-plugin/plugin.json")
                          printf '%-20s %-12s %s\n' "${flakeName}/$pname" "$pver" "${flakeRev}" >> $out/version-info
                          # Per FDR 0003: emit plugin-shipped prompt fragment dir if present.
                          fragdir="$candidate/.clown-plugin/system-prompt-append.d"
                          if [[ -d "$fragdir" ]]; then
                            echo "$fragdir" >> $out/plugin-fragment-dirs
                          fi
                          glob_count=$((glob_count + 1))
                        fi
                      done
                      if [[ $glob_count -eq 0 ]]; then
                        echo "clown: glob matched no plugin directories:" >&2
                        echo "  flake: ${flakeName}" >&2
                        echo "  pattern: ${pkg}/${dir}" >&2
                        exit 1
                      fi
                    ''
                  else
                    ''
                      if [[ ! -d "${pkg}/${dir}/.claude-plugin" ]] || [[ ! -f "${pkg}/${dir}/.claude-plugin/plugin.json" ]]; then
                        echo "clown: plugin directory does not contain .claude-plugin/:" >&2
                        echo "  flake: ${flakeName}" >&2
                        echo "  path: ${pkg}/${dir}" >&2
                        exit 1
                      fi
                      echo "${pkg}/${dir}" >> $out/plugin-dirs
                      pname=$(${pkgs.jq}/bin/jq -r '.name // empty' "${pkg}/${dir}/.claude-plugin/plugin.json")
                      pver=$(${pkgs.jq}/bin/jq -r '.version // "-"' "${pkg}/${dir}/.claude-plugin/plugin.json")
                      printf '%-20s %-12s %s\n' "${flakeName}/$pname" "$pver" "${flakeRev}" >> $out/version-info
                      # Per FDR 0003: emit plugin-shipped prompt fragment dir if present.
                      fragdir="${pkg}/${dir}/.clown-plugin/system-prompt-append.d"
                      if [[ -d "$fragdir" ]]; then
                        echo "$fragdir" >> $out/plugin-fragment-dirs
                      fi
                    ''
                ) plugin.dirs;
              in
              dirBlocks
            ) plugins;
          in
          pkgs.runCommand "clown-plugin-meta" { } ''
            mkdir -p $out
            touch $out/plugin-dirs
            touch $out/plugin-fragment-dirs
            touch $out/version-info
            ${pluginBlocks}
          '';

        mkClownPkg =
          {
            pluginMeta,
            defaultProvider ? defaultDefaultProvider,
            defaultProfile ? defaultDefaultProfile,
            enableTentClaude ? tentClaudeEnabled,
          }:
          let
            clownGoBin = mkClownGo { inherit defaultProvider defaultProfile enableTentClaude; };
          in
          (pkgs.symlinkJoin {
            name = "clown";
            paths = [
              (mkClownBin { inherit pluginMeta clownGoBin; })
              clown-plugin-host
              clown-stdio-bridge
              circus-go
              clown-completions
              clown-manpages
            ];
          }).overrideAttrs
            (old: {
              passthru = (old.passthru or { }) // {
                tests = {
                  managedSettingsRead = managedSettingsReadTest;
                };
              };
            });

        # Race-detector variant of clown-go. Built via the fork's
        # buildGoRace helper — overrides clown-go with CGO_ENABLED=1
        # and `go build -race`, plus a `-race` checkPhase. Surfaced
        # as packages.clown-race; not a release artifact (race-
        # instrumented binaries are slower).
        clown-go-race = pkgs.buildGoRace {
          base = mkClownGo {
            defaultProvider = defaultDefaultProvider;
            defaultProfile = defaultDefaultProfile;
          };
        };

        batsLaneOutputs = import ./bats.nix {
          inherit
            pkgs
            lib
            mkClownGo
            defaultDefaultProvider
            defaultDefaultProfile
            clown-stdio-bridge
            clown-plugin-host
            mock-stdio-mcp
            synthetic-plugin
            inspectCompiledPatched
            ;
        };

        mkCircus =
          {
            plugins ? [ ],
            defaultProvider ? defaultDefaultProvider,
            defaultProfile ? defaultDefaultProfile,
            enableTentClaude ? tentClaudeEnabled,
          }:
          let
            pluginMeta = if plugins == [ ] then emptyPluginMeta else resolvePlugins plugins;
          in
          {
            packages.default = mkClownPkg {
              inherit pluginMeta defaultProvider defaultProfile enableTentClaude;
            };
            devShells.default = pkgs.mkShell {
              packages = [
                (pkgs.mkGoEnv { pwd = ./.; })
                pkgs-master.just
                pkgs.fish
                pkgs-claude-code.claude-code
                pkgs-codex.codex
                pkgs.opencode
                pkgs-llm-agents.crush
                pkgs.bun
                pkgs.mitmproxy
                pkgs.gomod2nix
              ];
            };
            checks = {
              managedSettingsRead = managedSettingsReadTest;
            };
          };
      in
      {
        packages = {
          default = mkClownPkg { pluginMeta = emptyPluginMeta; };
          clown-manpages = clown-manpages;
          clown-race = clown-go-race;
          clown-cover = clown-cover;
          mock-stdio-mcp = mock-stdio-mcp;
          synthetic-plugin = synthetic-plugin;
        }
        // batsLaneOutputs
        # Expose the tent container image as a named package on linux
        # systems so it can be built directly (e.g. as an
        # `aarch64-linux` cross-build from darwin via nix-darwin's
        # linux-builder, then `podman load`-ed into a podman-machine
        # VM). On darwin systems pkgs.stdenv.isLinux is false and
        # tentImage evaluates to null, so the package is omitted
        # rather than surfaced as a broken attribute.
        // lib.optionalAttrs tentImageEnabled {
          tent-image = tentImage;
        };

        checks = {
          managedSettingsRead = managedSettingsReadTest;
          # bats-default runs every *.bats. There's no per-file tag
          # filter today; tests that bind 127.0.0.1 work in the
          # standard nix sandbox and every other Linux sandbox we
          # use. See ADR docs/adrs/0007-drop-net-cap-bats-file-tag.md
          # for the tag history and the conditions that would
          # warrant reintroducing one.
          bats-default = batsLaneOutputs.bats-default;
        };

        devShells.default = pkgs.mkShell {
          packages = [
            # mkGoEnv from the fork's overlay supersedes a bare
            # `pkgs.go`: it materializes the module dependency tree
            # from gomod2nix.toml so `go test ./...` from the
            # devshell resolves modules through the same nix-built
            # vendor closure as buildGoApplication does, instead of
            # reaching out to GOPROXY for every fresh checkout.
            (pkgs.mkGoEnv { pwd = ./.; })
            pkgs-master.just
            pkgs.fish
            pkgs-claude-code.claude-code
            pkgs-codex.codex
            pkgs.opencode
            pkgs-llm-agents.crush
            pkgs.bun
            pkgs.mitmproxy
            pkgs.gomod2nix
          ];
        };

        lib.mkCircus = mkCircus;

        formatter = treefmtEval.config.build.wrapper;
      }
    );
}
