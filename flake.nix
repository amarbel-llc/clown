{
  description = "clown — coding-agent wrapper";

  inputs = {
    # Main nixpkgs is the amarbel-llc fork at master. The fork's
    # overlays.default contributes buildGoApplication, mkGoEnv,
    # gomod2nix (CLI), fetchGgufModel, and other amarbel-packages
    # additions to pkgs. See overlays/amarbel-packages.nix in the fork.
    nixpkgs.url = "github:amarbel-llc/igloo";
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
    nixpkgs-master.url = "github:NixOS/nixpkgs/d233902339c02a9c334e7e593de68855ad26c4cb";
    utils.url = "https://flakehub.com/f/numtide/flake-utils/0.1.102";
    # nixpkgs-claude-code is kept for reference but claude-code is now
    # sourced from llm-agents (2.1.150+). The old npm-source derivation
    # (2.1.111) used a JS patchPhase on cli.js; the new binary derivation
    # uses a postInstall binary string substitution on the Bun bundle.
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
    llm-agents.inputs.treefmt-nix.follows = "treefmt-nix";
    treefmt-nix.url = "github:numtide/treefmt-nix";
    treefmt-nix.inputs.nixpkgs.follows = "nixpkgs";
    # amarbel-llc/bats provides the batsLane build helper (formerly
    # pkgs.testers.batsLane in the full-fork era) and the bats-libs
    # bundle (bats-support, bats-assert, bats-emo, bats-island). The
    # fork's thin-overlay master no longer ships the testers helper,
    # so the bats flake is the canonical source.
    bats.url = "github:amarbel-llc/bats";
    bats.inputs.nixpkgs.follows = "nixpkgs";
    bats.inputs.nixpkgs-master.follows = "nixpkgs-master";
    bats.inputs.utils.follows = "utils";
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
      bats,
    }:
    (utils.lib.eachDefaultSystem (
      system:
      let
        # The fork's default.nix shim auto-applies its overlay on
        # `import nixpkgs { ... }`, so pkgs gets buildGoApplication,
        # mkGoEnv, gomod2nix (CLI), fetchGgufModel, etc. without an
        # explicit overlays pass.
        pkgs = import nixpkgs {
          inherit system;
        };
        pkgs-master = import nixpkgs-master {
          inherit system;
          config.allowUnfree = true;
        };
        # pkgs-claude-code is unused since the switch to llm-agents for
        # claude-code 2.1.150+. The nixpkgs-claude-code input (2.1.111
        # pre-binary-distribution era) is kept so the flake input stays
        # reachable; remove both together when the old SHA is no longer needed.
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
        claudeCodeVersion = pkgs-llm-agents.claude-code.version;
        claudeCodeRev = llm-agents.rev or "dirty";
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

        # bats-libs bundle (bats-support, bats-assert, bats-emo, bats-island).
        # Lifted to the outer scope so both bats.nix's lane builder and
        # clown-cover's coverIntegrationCommand can stage the same helper
        # set. batsLibPath is `${bats-libs}/share/bats`.
        batsLibs = bats.packages.${system}.bats-libs;

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
          cp -r ${./zz-tests_bats}/* stage/zz-tests_bats/
          cp ${inspectCompiledPatched} stage/zz-tests_bats/inspect-compiled
          chmod -R u+w stage

          export CLOWN_PLUGIN_HOST_BIN="$out/bin/clown-plugin-host"
          export CLOWN_STDIO_BRIDGE_BIN="$out/bin/clown-stdio-bridge"
          export MOCK_STDIO_MCP_BIN="$out/bin/mock-stdio-mcp"
          export SYNTHETIC_PLUGIN_DIR="${synthetic-plugin}"
          # common.bash bats_load_library calls resolve through this path.
          export BATS_LIB_PATH="${batsLibs.batsLibPath}"

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
            # bats-island's setup_test_home invokes `git config`; provide
            # git on PATH so the lane's per-test isolation hook works.
            git
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

        # Dev-loop podman-machine bindings. The `dev-tent-machine-{up,
        # down,status}` flake apps spin up an isolated podman-machine
        # named devTentMachineName with the volume(s) below. Use
        # `packages.dev` to build a clown that targets this machine.
        # See AGENTS.md § Dev loop for tent for the workflow.
        #
        # *Why this mount set:* we originally tried a single `/:/:rw`
        # mount on the theory that container-level `podman run
        # --volume` would do the actual filtering. applehv silently
        # **dropped** that mount at start time — the JSON config
        # accepted it but virtiofsd never honored it, and the VM's
        # mount table showed no virtiofs entries at all. The eng-side
        # `programs.podman-darwin` module documents the
        # known-working set (/nix/store ro, /nix/var rw, /etc/nix ro,
        # $HOME rw) so we mirror that. The script appends `$HOME`
        # at machine-init time because nix can't reach into runtime
        # user state.
        devTentMachineName = "clown-dev";
        devTentVolumes = [
          "/nix/store:/nix/store:ro,security_model=none"
          "/nix/var:/nix/var:rw,security_model=none"
          "/etc/nix:/etc/nix:ro,security_model=none"
          # $HOME is appended in the launch script at runtime; see
          # devTentMachineUp's volume-assembly block.
        ];

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
            # podmanMachineName burns a `--connection <name>` flag into
            # every podman invocation made by `--tent`. Empty (the
            # default) means clown defers to podman's own configured
            # default connection. Set this on per-circus builds that
            # want to target an isolated dev-loop machine instead of
            # the user's eng-managed podman-machine-default — see the
            # `dev-tent-machine-*` flake apps and `packages.dev`.
            podmanMachineName ? "",
            # tentBackend selects the container runtime that drives
            # --tent. "podman" is the status quo and the default;
            # "lima" routes everything through `limactl shell
            # <machine> -- sudo nerdctl ...`. See clown#99 (Lima spike)
            # and internal/tent/backend.go for the runtime side. A
            # future TOML profile system will own this selection per-
            # profile; today's build-time lever mirrors
            # podmanMachineName's shape so the migration is mechanical.
            tentBackend ? "podman",
          }:
          let
            tentClaudeCliPath =
              if enableTentClaude then
                "${llm-agents.packages.${tentClaudeSystem}.claude-code}/bin/claude"
              else
                "";
            # Assert the backend is one we know about. nix evaluates
            # this assertion lazily on first use of the binding.
            _ = lib.assertOneOf "tentBackend" tentBackend [
              "podman"
              "lima"
            ];
            limactlPath =
              if tentBackend == "lima" then "${pkgs.lima}/bin/limactl" else "";
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
              # ${self} interpolates the source-tree store path captured
              # by the flake. Used at runtime as the flake-uri for
              # `nix build <self>#packages.<linux-system>.tent-image`
              # when the local podman image store doesn't already have
              # the tag and no tarball was baked in (darwin, or future
              # profiles). The dependency keeps the source tree alive
              # in /nix/store as long as the clown derivation is.
              "-X github.com/amarbel-llc/clown/internal/buildcfg.TentImageFlakeRef=${self}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.ClaudeTentCliPath=${tentClaudeCliPath}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.PodmanMachineName=${podmanMachineName}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.TentBackend=${tentBackend}"
              "-X github.com/amarbel-llc/clown/internal/buildcfg.LimactlPath=${limactlPath}"
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

        # ringmaster: control-plane daemon for llama-server instances.
        # See FDR-0010. circus is its CLI client; clown will be one too
        # (FDR-0011 / plan 2). LlamaServerPath is burned in so the
        # daemon knows what binary to exec when StartInstance fires —
        # symmetric to circus-go above. Without this the dispatch
        # returns "launcher not configured" for any start/stop call.
        ringmaster-go = buildGoApplication {
          pname = "ringmaster";
          version = clownVersion;
          src = goSrc;
          subPackages = [ "cmd/ringmaster" ];
          modules = ./gomod2nix.toml;
          ldflags = [
            "-s"
            "-w"
            "-X github.com/amarbel-llc/clown/internal/buildcfg.LlamaServerPath=${llamaServerPath}"
          ];
        };

        # fake-llama-server: a stand-in for llama-server used by the
        # ringmaster bats e2e lane. Real llama-cpp is too heavy and
        # too slow to spin up under bats — and we'd never run it
        # against a real GGUF inside the nix sandbox. This serves
        # /health (200 OK) and /v1/models, which is all the launcher
        # waits on. Same source as cmd/ringmaster/testdata/fake-llama-server.
        fake-llama-server-go = buildGoApplication {
          pname = "fake-llama-server";
          version = clownVersion;
          src = goSrc;
          subPackages = [ "cmd/ringmaster/testdata/fake-llama-server" ];
          modules = ./gomod2nix.toml;
          ldflags = [
            "-s"
            "-w"
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

        # Was: patch upstream claude-code to read its managed-settings
        # from a path under its own $out instead of /etc/claude-code,
        # via perl -0777 in-place binary substitution on the Bun bundle.
        # Now: no-op. The substitution corrupted the binary on darwin
        # (Mach-O segment offsets and code signature both invalidated by
        # length expansion) and patched the wrong code path anyway —
        # /etc/claude-code is the linux-only default branch in the bun
        # bundle; darwin uses /Library/Application Support/ClaudeCode,
        # windows uses C:\Program Files\ClaudeCode. See clown#95 for
        # the full analysis and the planned proper fix
        # (length-preserving substitution + ad-hoc re-signing on darwin,
        # plus a separate patch for the macos path).
        #
        # Today the unbypassable-settings invariant lives inside --tent
        # only, per FDR-0007's "tent IS the boundary" framing. Un-tented
        # clown ships the managed-settings JSON next to the binary but
        # the binary's load path is unchanged, so on darwin and linux
        # both, claude reads no managed-settings outside the tent. When
        # tent goes default (clown#62), this whole mkPatchedClaudeCode
        # pipeline can be deleted.
        patchClaudeCodeManagedPath = _replacement: pkgs-llm-agents.claude-code;

        # Ship the managed-settings JSON alongside the (unpatched)
        # claude-code binary. Today the binary doesn't read this file
        # outside --tent (see patchClaudeCodeManagedPath above), but
        # shipping it preserves the on-disk layout that other code and
        # tests expect, and is the right destination once clown#95 lands
        # a working binary patch.
        mkPatchedClaudeCode =
          managedSettings:
          (patchClaudeCodeManagedPath "$out/etc/claude").overrideAttrs (old: {
            postInstall = (old.postInstall or "") + ''
              mkdir -p "$out/etc/claude"
              cp ${managedSettings} "$out/etc/claude/managed-settings.json"
            '';

            # Without the binary patch in place, only the JSON layout is
            # worth asserting. The full binary-string check (no
            # /etc/claude-code, yes $out/etc/claude) is gated on clown#95.
            doInstallCheck = true;
            installCheckPhase = ''
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
                  $out/share/man/man1/circus.1 \
                  $out/share/man/man1/ringmaster.1 \
                  $out/share/man/man5/clown-json.5 \
                  $out/share/man/man7/clown-plugin-protocol.7 \
                  $out/share/man/man7/ringmaster.7 \
                  $out/share/man/man7/ringmaster-testing.7; do
                  sed -i "s/@MDOCDATE@/$mdocDate/g" "$page"
                  if grep -q '@MDOCDATE@' "$page"; then
                      echo "clown-manpages: @MDOCDATE@ left unsubstituted in $page" >&2
                      exit 1
                  fi
              done
            '';

        # `managedSettingsRead` is wired as a flake check so the bundled
        # managed-settings JSON ships alongside the (unpatched-for-now)
        # claude-code binary. The historical string-level assertions
        # (no /etc/claude-code, yes $out/etc/claude in the bundle) are
        # currently disabled — see patchClaudeCodeManagedPath above and
        # clown#95 for the corruption analysis and the planned proper
        # fix. Once the binary patch is back in place, restore those
        # assertions in mkPatchedClaudeCode's installCheckPhase.
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
            podmanMachineName ? "",
            tentBackend ? "podman",
          }:
          let
            clownGoBin = mkClownGo {
              inherit
                defaultProvider
                defaultProfile
                enableTentClaude
                podmanMachineName
                tentBackend
                ;
            };
          in
          (pkgs.symlinkJoin {
            name = "clown";
            paths = [
              (mkClownBin { inherit pluginMeta clownGoBin; })
              clown-plugin-host
              clown-stdio-bridge
              circus-go
              ringmaster-go
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
          batsLane = bats.lib.${system}.batsLane;
          bats-libs = batsLibs;
          # ringmaster e2e lane consumes the daemon, the circus
          # client, and a Go fake llama-server. The fake is a
          # tiny http server stand-in compiled from
          # cmd/ringmaster/testdata/fake-llama-server — same source
          # the launcher_test.go and server_test.go fixtures use.
          ringmaster = ringmaster-go;
          circus = circus-go;
          fake-llama-server = fake-llama-server-go;
        };

        mkCircus =
          {
            plugins ? [ ],
            defaultProvider ? defaultDefaultProvider,
            defaultProfile ? defaultDefaultProfile,
            enableTentClaude ? tentClaudeEnabled,
            # See mkClownGo for the podmanMachineName contract.
            # Downstream consumers building their own circus can set
            # this to target a dev-loop machine.
            podmanMachineName ? "",
            # See mkClownGo for the tentBackend contract. Recognized:
            # "podman" (default) and "lima".
            tentBackend ? "podman",
          }:
          let
            pluginMeta = if plugins == [ ] then emptyPluginMeta else resolvePlugins plugins;
          in
          {
            packages.default = mkClownPkg {
              inherit
                pluginMeta
                defaultProvider
                defaultProfile
                enableTentClaude
                podmanMachineName
                tentBackend
                ;
            };
            devShells.default = pkgs.mkShell {
              packages = [
                (pkgs.mkGoEnv { pwd = ./.; })
                pkgs-master.just
                pkgs.fish
                pkgs-llm-agents.claude-code
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

        # Dev-loop podman-machine apps. `nix run .#dev-tent-machine-up`
        # spins up an isolated machine for testing. Because podman on
        # darwin enforces a strict one-VM-at-a-time rule (see AGENTS.md
        # § Dev loop for tent), this is a **stop/swap** workflow:
        # `up` stops the eng-managed `podman-machine-default` if it's
        # running, then starts `clown-dev`. `down` reverses (stop
        # clown-dev, restart default). The eng-managed default is
        # therefore *temporarily unavailable* while clown-dev is up;
        # `down` brings it back. This is the same workflow the podman
        # maintainers themselves recommend for "test machine changes
        # without nuking my prod machine" — see the clown follow-up
        # issue tracking Lima/Colima as a parallel-VM alternative.
        devTentMachineDefault = "podman-machine-default";
        devTentMachineUp = pkgs.writeShellApplication {
          name = "dev-tent-machine-up";
          runtimeInputs = [ pkgs.podman ];
          text = ''
            NAME=${devTentMachineName}
            DEFAULT=${devTentMachineDefault}
            # 1. Stop the eng-managed default if it's running. podman
            #    enforces one-VM-at-a-time on darwin; we have to clear
            #    the slot before starting clown-dev. Quiet on "not
            #    running" / "doesn't exist"; those are non-errors.
            if podman machine inspect "$DEFAULT" --format '{{.State}}' 2>/dev/null | grep -qx running; then
              echo ">> stopping $DEFAULT to free the VM slot" >&2
              podman machine stop "$DEFAULT" >/dev/null
            fi
            # 2. Init clown-dev if absent. Re-init is intentional on
            #    mount-list changes — `dev-tent-down` removes it.
            #    $HOME is appended at runtime because nix can't reach
            #    into per-user state (the same machine-init
            #    derivation is fine for every user; the VM config
            #    differs by $HOME but the derivation doesn't).
            if ! podman machine inspect "$NAME" >/dev/null 2>&1; then
              HOME_VOLUME="$HOME:$HOME:rw,security_model=none"
              echo ">> initializing podman machine $NAME" >&2
              podman machine init "$NAME" \
                ${lib.concatMapStringsSep " \\\n                " (v: "--volume ${lib.escapeShellArg v}") devTentVolumes} \
                --volume "$HOME_VOLUME"
            fi
            # 3. Start it if not already running. `podman machine start`
            #    errors out (non-zero) when the machine is already
            #    running, so guard the call.
            state="$(podman machine inspect "$NAME" --format '{{.State}}' 2>/dev/null || echo unknown)"
            if [[ "$state" = "running" ]]; then
              echo ">> $NAME is already running" >&2
            else
              echo ">> starting $NAME" >&2
              podman machine start "$NAME"
            fi
            # 3b. Verify the configured mounts actually surfaced inside
            #     the VM. applehv has been observed to accept a mount
            #     at init-config-write time but silently drop it at
            #     start (e.g. for "/", which virtiofsd refuses). Mount
            #     drops result in `podman run --volume host:host` later
            #     failing with `statfs: no such file or directory` —
            #     better to fail loudly here.
            for src in /nix/store /nix/var /etc/nix "$HOME"; do
              if ! podman machine ssh "$NAME" -- test -e "$src" 2>/dev/null; then
                echo "FAIL: configured mount $src did not surface inside the VM" >&2
                echo "      (applehv may have dropped it silently at virtiofsd init)" >&2
                exit 1
              fi
            done
            # 4. Spawn the SSH agent forwarder in the background. Tent
            #    expects the host's $SSH_AUTH_SOCK to be reachable at
            #    devTentSSHSockInVM (= /run/host-services/ssh-auth.sock)
            #    inside the VM. Idempotent: if the previous-run pidfile
            #    points at a still-alive process, we leave it alone.
            mkdir -p "$(dirname "${devTentSSHForwardPidFile}")"
            existing_pid=""
            if [[ -f "${devTentSSHForwardPidFile}" ]]; then
              existing_pid="$(cat "${devTentSSHForwardPidFile}" 2>/dev/null || true)"
              if [[ -n "$existing_pid" ]] && ! kill -0 "$existing_pid" 2>/dev/null; then
                existing_pid=""  # stale pidfile; clear it
                rm -f "${devTentSSHForwardPidFile}"
              fi
            fi
            if [[ -n "$existing_pid" ]]; then
              echo ">> ssh-agent forwarder already running (pid $existing_pid)" >&2
            elif [[ -n "''${SSH_AUTH_SOCK:-}" && -S "''${SSH_AUTH_SOCK}" ]]; then
              FWD_LOG=".tmp/podman/$NAME-ssh-forward.log"
              echo ">> launching ssh-agent forwarder in background (log: $FWD_LOG)" >&2
              ${devTentSSHForward}/bin/dev-tent-ssh-forward "$NAME" >"$FWD_LOG" 2>&1 &
              echo $! > "${devTentSSHForwardPidFile}"
              # Wait for the in-VM socket to appear so subsequent
              # `podman run --volume` invocations don't race against
              # the forwarder's setup. Bail out loudly if the
              # forwarder dies before the socket shows up.
              for _attempt in 1 2 3 4 5 6 7 8 9 10; do
                if podman machine ssh "$NAME" -- test -S "${devTentSSHSockInVM}" 2>/dev/null; then
                  break
                fi
                fwd_pid="$(cat "${devTentSSHForwardPidFile}" 2>/dev/null)"
                if [[ -z "$fwd_pid" ]] || ! kill -0 "$fwd_pid" 2>/dev/null; then
                  echo "FAIL: ssh-agent forwarder died before publishing the socket" >&2
                  echo "      see $FWD_LOG for details" >&2
                  cat "$FWD_LOG" >&2 || true
                  exit 1
                fi
                sleep 0.5
              done
              if ! podman machine ssh "$NAME" -- test -S "${devTentSSHSockInVM}" 2>/dev/null; then
                echo "FAIL: in-VM socket ${devTentSSHSockInVM} did not appear within 5s" >&2
                echo "      see $FWD_LOG for details" >&2
                cat "$FWD_LOG" >&2 || true
                exit 1
              fi
              echo ">> ssh-agent forwarder ready" >&2
            else
              echo ">> SSH_AUTH_SOCK is empty or missing; skipping forwarder" >&2
              echo ">>   in-tent git push / signed commits will not work" >&2
            fi
            echo ">> $NAME is up. Build clown with: nix build .#dev" >&2
            echo ">> $DEFAULT is stopped; restart it with: nix run .#dev-tent-machine-down" >&2
          '';
        };

        devTentMachineDown = pkgs.writeShellApplication {
          name = "dev-tent-machine-down";
          runtimeInputs = [ pkgs.podman ];
          text = ''
            NAME=${devTentMachineName}
            DEFAULT=${devTentMachineDefault}
            PID_FILE=${devTentSSHForwardPidFile}
            # 1. Kill the SSH agent forwarder if it's still running.
            #    Best-effort; the ssh -N process exits on its own when
            #    the machine stops, but we tear it down explicitly to
            #    avoid stale pidfile noise.
            if [[ -f "$PID_FILE" ]]; then
              pid="$(cat "$PID_FILE")"
              if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
                echo ">> stopping ssh-agent forwarder (pid $pid)" >&2
                kill "$pid" 2>/dev/null || true
              fi
              rm -f "$PID_FILE"
            fi
            # 2. Stop and remove clown-dev. Idempotent.
            podman machine stop  "$NAME" >/dev/null 2>&1 || true
            podman machine rm -f "$NAME" >/dev/null 2>&1 || true
            echo ">> removed $NAME (if it existed)" >&2
            # 3. Restart the eng-managed default. Only if it exists;
            #    fresh-clone hosts may not have one yet.
            if podman machine inspect "$DEFAULT" >/dev/null 2>&1; then
              echo ">> restarting $DEFAULT" >&2
              podman machine start "$DEFAULT" 2>/dev/null || true
            else
              echo ">> $DEFAULT not present on this host; skipping restart" >&2
            fi
          '';
        };

        devTentMachineStatus = pkgs.writeShellApplication {
          name = "dev-tent-machine-status";
          runtimeInputs = [ pkgs.podman ];
          text = ''
            NAME=${devTentMachineName}
            DEFAULT=${devTentMachineDefault}
            # Show both machines so the user can see which one
            # currently owns the VM slot.
            for m in "$NAME" "$DEFAULT"; do
              if ! podman machine inspect "$m" --format '{{.Name}}: {{.State}}' 2>/dev/null; then
                echo "$m: not present"
              fi
            done
          '';
        };

        # The in-VM path the ssh-forwarder publishes the host's SSH
        # agent at. Mirrors the Docker Desktop / OrbStack / Lima
        # convention so containers know where to look.
        devTentSSHSockInVM = "/run/host-services/ssh-auth.sock";

        # `dev-tent-ssh-forward` proxies the host's $SSH_AUTH_SOCK into
        # the running podman-machine VM via OpenSSH `-R`. This is the
        # standard workaround for podman-machine on darwin not being
        # able to bind-mount AF_UNIX sockets through its virtiofs/9p
        # layer (containers/podman#23245, #23785 — RFE open). The
        # forwarder re-publishes the socket at devTentSSHSockInVM
        # inside the VM, where tent's `podman run --volume` can
        # bind-mount it into the container at the same path.
        #
        # *Why we call ssh directly, not `podman machine ssh`:*
        # `podman machine ssh [args]` passes args as a COMMAND TO
        # EXECUTE inside the VM (documented at
        # docs.podman.io/.../podman-machine-ssh.1.html — `[command
        # [arg ...]]`), not as flags to the local ssh client. So
        # `-R` ends up as a bash flag inside the VM, which fails
        # with "bash: -R: invalid option". We extract the VM's SSH
        # coordinates from `podman machine inspect` and invoke ssh
        # ourselves with the `-R` flag in the right spot.
        #
        # Usage: `nix run .#dev-tent-ssh-forward -- <machine-name>`.
        # `dev-tent-machine-up` starts this automatically pointing at
        # clown-dev; production tent runs can target
        # podman-machine-default the same way. Holds a foreground
        # `ssh -N` connection — Ctrl-C tears down the forward.
        devTentSSHForward = pkgs.writeShellApplication {
          name = "dev-tent-ssh-forward";
          runtimeInputs = [
            pkgs.podman
            pkgs.openssh
          ];
          text = ''
            NAME="''${1:-${devTentMachineName}}"
            GUEST_SOCK=${devTentSSHSockInVM}
            HOST_SOCK="''${SSH_AUTH_SOCK:-}"
            if [[ -z "$HOST_SOCK" ]]; then
              echo "FAIL: SSH_AUTH_SOCK is empty; nothing to forward" >&2
              exit 1
            fi
            if [[ ! -S "$HOST_SOCK" ]]; then
              echo "FAIL: $HOST_SOCK is not a socket" >&2
              exit 1
            fi
            if ! podman machine inspect "$NAME" --format '{{.State}}' 2>/dev/null | grep -qx running; then
              echo "FAIL: podman machine $NAME is not running" >&2
              exit 1
            fi
            # Extract SSH coordinates for the machine. podman publishes
            # these via `machine inspect`; using them directly lets us
            # invoke ssh with `-R` correctly placed.
            SSH_PORT="$(podman machine inspect "$NAME" --format '{{.SSHConfig.Port}}')"
            SSH_USER="$(podman machine inspect "$NAME" --format '{{.SSHConfig.RemoteUsername}}')"
            SSH_KEY="$(podman machine inspect "$NAME"  --format '{{.SSHConfig.IdentityPath}}')"
            # Ensure the parent directory of the guest socket exists.
            # ssh -R will create the socket file but not its parent
            # dir; /run/host-services/ is not a default directory
            # inside the Fedora-CoreOS machine image.
            podman machine ssh "$NAME" -- \
              sudo install -d -m 0755 -o "$SSH_USER" "$(dirname "$GUEST_SOCK")"
            echo ">> forwarding $HOST_SOCK -> $NAME:$GUEST_SOCK" >&2
            # -R publishes a fresh AF_UNIX endpoint inside the VM that
            # proxies bytes back to HOST_SOCK over the SSH transport.
            # -N: no remote command; just hold the forward open.
            # StreamLocalBindUnlink: pre-clean the in-VM socket path
            #   so reconnects after an unclean exit don't fail with
            #   "address already in use".
            # StrictHostKeyChecking=no: the VM is ephemeral; we don't
            #   maintain a known_hosts entry for it. -o flags here
            #   mirror what `podman machine ssh` passes internally.
            exec ssh \
              -i "$SSH_KEY" \
              -p "$SSH_PORT" \
              -o StrictHostKeyChecking=no \
              -o UserKnownHostsFile=/dev/null \
              -o StreamLocalBindUnlink=yes \
              -o ExitOnForwardFailure=yes \
              -R "$GUEST_SOCK:$HOST_SOCK" \
              -N \
              "$SSH_USER@127.0.0.1"
          '';
        };

        # PID file for the background forwarder spawned by
        # dev-tent-machine-up. Per-worktree path under .tmp keeps
        # multiple worktrees on the same host from stomping each
        # other's forwarders.
        devTentSSHForwardPidFile = ".tmp/podman/${devTentMachineName}-ssh-forward.pid";
      in
      {
        packages = {
          default = mkClownPkg { pluginMeta = emptyPluginMeta; };
          # dev: clown built with podmanMachineName baked in, targeting
          # the local `clown-dev` podman-machine created by
          # `nix run .#dev-tent-machine-up`. Use this when iterating on
          # tent mount logic without touching the user's eng-managed
          # podman-machine-default. See AGENTS.md § Dev loop for tent.
          dev = mkClownPkg {
            pluginMeta = emptyPluginMeta;
            podmanMachineName = devTentMachineName;
          };
          # dev-lima: clown built with the Lima tent backend, targeting
          # a Lima instance (default name "clown-tent" matching
          # services.tent-backend-lima's default). Use this when
          # iterating against Lima instead of podman-machine. The Lima
          # spike at zz-pocs/tent-lima/ proved the backend works; this
          # package puts that backend behind the standard
          # `clown --tent` UX. See clown#99 and AGENTS.md § Tent
          # backend lever.
          dev-lima = mkClownPkg {
            pluginMeta = emptyPluginMeta;
            podmanMachineName = "clown-tent";
            tentBackend = "lima";
          };
          clown-manpages = clown-manpages;
          clown-race = clown-go-race;
          clown-cover = clown-cover;
          mock-stdio-mcp = mock-stdio-mcp;
          synthetic-plugin = synthetic-plugin;
          # ringmaster: standalone build of the control-plane daemon.
          # The home-manager module at homeManagerModules.ringmaster
          # consumes this via its `package` option. Also bundled into
          # the default symlinkJoin so `nix build` ships the binary.
          ringmaster = ringmaster-go;
          # bats-libs surfaces the amarbel-llc/bats helper bundle
          # (bats-support, bats-assert, bats-emo, bats-island) under
          # share/bats. Already used by the sandboxed batsLane via
          # BATS_LIB_PATH; exposed here so host-side recipes that run
          # bats outside the nix sandbox (eg. `just test-tent-smoke`)
          # can resolve `bats_load_library` without going through the
          # external bats flake (which would re-pin and drift).
          bats-libs = batsLibs;
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
            pkgs-llm-agents.claude-code
            pkgs-codex.codex
            pkgs.opencode
            pkgs-llm-agents.crush
            pkgs.bun
            pkgs.mitmproxy
            pkgs.gomod2nix
          ];
        };

        lib.mkCircus = mkCircus;

        # Dev-loop apps. `nix run .#dev-tent-machine-{up,down,status}`
        # manages the isolated podman-machine that `nix build .#dev`
        # targets. See devTentMachineName / devTentVolumes above and
        # AGENTS.md § Dev loop for tent.
        apps.dev-tent-machine-up = {
          type = "app";
          program = "${devTentMachineUp}/bin/dev-tent-machine-up";
        };
        apps.dev-tent-machine-down = {
          type = "app";
          program = "${devTentMachineDown}/bin/dev-tent-machine-down";
        };
        apps.dev-tent-machine-status = {
          type = "app";
          program = "${devTentMachineStatus}/bin/dev-tent-machine-status";
        };
        # Foreground SSH-agent forwarder. Bypasses dev-tent-machine-up's
        # background spawn — useful for `nix run .#dev-tent-ssh-forward
        # podman-machine-default` to attach a forwarder to the eng
        # default machine, or for debugging when the background
        # forwarder isn't behaving.
        apps.dev-tent-ssh-forward = {
          type = "app";
          program = "${devTentSSHForward}/bin/dev-tent-ssh-forward";
        };

        formatter = treefmtEval.config.build.wrapper;
      }
    ))
    // {
      # Cross-system outputs (modules, library) live outside the
      # eachDefaultSystem block because they're system-independent.
      #
      # programs.ringmaster home-manager module — see FDR-0010 and
      # docs/plans/2026-05-18-ringmaster-control-plane.md § Task 16/17.
      # Consumes packages.<system>.ringmaster as the daemon binary;
      # users wire it in their home-manager config via
      #   programs.ringmaster.enable = true;
      #   programs.ringmaster.package = clown.packages.${pkgs.system}.ringmaster;
      homeManagerModules.ringmaster = import ./nix/hm/ringmaster.nix;

      # services.tent-backend-lima home-manager module — see
      # nix/hm/tent-backend-lima.nix for the full design. Manages a
      # darwin Lima VM via a LaunchAgent so clown --tent (and other
      # consumers) have a containerd-capable VM available at login.
      # Lima supports parallel VMs on darwin, unlike podman-machine
      # (clown#99 spike). Downstream consumers (notably eng) can
      # adopt this alongside or in place of programs.podman-darwin.
      #   services.tent-backend-lima.enable = true;
      #   services.tent-backend-lima.machineName = "clown-tent";
      # Defaults to the mount set and ssh.forwardAgent shape that
      # clown's tent expects; see the module's option docs for the
      # full surface.
      homeManagerModules.tent-backend-lima = import ./nix/hm/tent-backend-lima.nix;
    };
}
