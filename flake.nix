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
    nixpkgs-master.url = "github:amarbel-llc/nixpkgs/9b53530a5f6887b6903cffeb8a418f3079d6698d";
    utils.url = "https://flakehub.com/f/numtide/flake-utils/0.1.102";
    nixpkgs-claude-code.url = "github:amarbel-llc/nixpkgs/b2b9662ffe1e9a5702e7bfbd983595dd56147dbf";
    nixpkgs-codex.url = "github:amarbel-llc/nixpkgs/e2dde111aea2c0699531dc616112a96cd55ab8b5";
    # llama-cpp with Anthropic Messages API (/v1/messages) support — requires
    # PR #17570 (merged 2025-11-28). Build 6981 in nixos-25.11 predates it.
    nixpkgs-llama.url = "github:amarbel-llc/nixpkgs/3b5a614454bd054dd960f1ff7a888dc5dfaf7bb4";
    # numtide/claudebox source — patched in-tree to add `--` arg
    # passthrough so clown's BuildClaudeArgs flags reach the inner claude.
    # Pinned by commit SHA (tag v0.2.0) so version-tag retags don't
    # silently change the build.
    claudebox-src = {
      url = "github:numtide/claudebox/33a7705a6232acfe77397e20c8710456221277a1";
      flake = false;
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      nixpkgs-master,
      nixpkgs-claude-code,
      nixpkgs-codex,
      nixpkgs-llama,
      utils,
      claudebox-src,
    }:
    utils.lib.eachDefaultSystem (
      system:
      let
        # Apply the fork's overlay so pkgs gets buildGoApplication,
        # mkGoEnv, gomod2nix (CLI), fetchGgufModel, etc. The overlay
        # also pins claude-code at the package level — we route
        # claude-code through pkgs-claude-code (separate input, no
        # overlay) so that pin doesn't override our chosen version.
        pkgs = import nixpkgs {
          inherit system;
          overlays = [ nixpkgs.overlays.default ];
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
      in
      let
        lib = pkgs.lib;

        # GGUF model fetches via the fork's `fetchGgufModel` wrapper.
        # Same content as `pkgs.fetchurl`, but takes hex sha256
        # directly (matching what HuggingFace surfaces) and names
        # the output `<name>.gguf` so the store path is intelligible.
        gemma3-270m-model = pkgs.fetchGgufModel {
          name = "gemma-3-270m-it-Q8_0";
          url = "https://huggingface.co/ggml-org/gemma-3-270m-it-GGUF/resolve/main/gemma-3-270m-it-Q8_0.gguf";
          sha256 = "sha256-DvV9LIOEWKGVJmQmDcujjlvdo3SU869zLwbkrdJAaOM=";
        };

        qwen3-06b-model = pkgs.fetchGgufModel {
          name = "Qwen3-0.6B-Q8_0";
          url = "https://huggingface.co/Qwen/Qwen3-0.6B-GGUF/resolve/main/Qwen3-0.6B-Q8_0.gguf";
          sha256 = "sha256-lGXmOiKt1TVNm7S5npARcEPHEkAHZkkHJZvRbQQ7sDE=";
        };

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
          map (f: parseAgent (./subagents + "/${f}")) (
            builtins.filter (f: lib.hasSuffix ".md" f) agentFiles
          )
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
          "01" = "January";   "02" = "February"; "03" = "March";
          "04" = "April";     "05" = "May";      "06" = "June";
          "07" = "July";      "08" = "August";   "09" = "September";
          "10" = "October";   "11" = "November"; "12" = "December";
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
            "-s" "-w"
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
            "-s" "-w"
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
          ldflags = [ "-s" "-w" ];
        };

        mock-stdio-mcp = pkgs.runCommand "mock-stdio-mcp" { } ''
          mkdir -p $out/bin
          cp ${mock-stdio-mcp-go}/bin/mockstdiomcp $out/bin/mock-stdio-mcp
        '';

        # Compiled binary that the synthetic-plugin derivation embeds.
        # Not exposed as a top-level package — consumers should use
        # synthetic-plugin instead, which lays out the full plugin dir.
        mock-mcp-server-go = buildGoApplication {
          pname = "mock-mcp-server";
          version = clownVersion;
          src = goSrc;
          subPackages = [ "internal/pluginhost/testdata/mockserver" ];
          modules = ./gomod2nix.toml;
          ldflags = [ "-s" "-w" ];
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
          ldflags = [ "-s" "-w" ];
        };

        # Build-time defaults baked into the standalone packages.default
        # and into mkClownPkg / mkCircus when no override is given. Match
        # the historical hardcoded "claude" provider and the builtin
        # claude-anthropic profile (provider=claude, backend=anthropic).
        defaultDefaultProvider = "claude";
        defaultDefaultProfile = "claude-anthropic";

        mkClownGo =
          { defaultProvider ? defaultDefaultProvider
          , defaultProfile ? defaultDefaultProfile
          }: buildGoApplication {
          pname = "clown";
          version = clownVersion;
          src = goSrc;
          subPackages = [ "cmd/clown" ];
          modules = ./gomod2nix.toml;
          ldflags = [
            "-s" "-w"
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
            "-X github.com/amarbel-llc/clown/internal/buildcfg.ClownboxCliPath=${clownboxCliPath}"
            "-X github.com/amarbel-llc/clown/internal/buildcfg.StdioBridgePath=${clown-stdio-bridge}/bin/clown-stdio-bridge"
            "-X github.com/amarbel-llc/clown/internal/buildcfg.DefaultProvider=${defaultProvider}"
            "-X github.com/amarbel-llc/clown/internal/buildcfg.DefaultProfile=${defaultProfile}"
          ];
        };

        circus-go = buildGoApplication {
          pname = "circus";
          version = clownVersion;
          src = goSrc;
          subPackages = [ "cmd/circus" ];
          modules = ./gomod2nix.toml;
          ldflags = [
            "-s" "-w"
            "-X github.com/amarbel-llc/clown/internal/buildcfg.DefaultModelPath=${gemma3-270m-model}"
            "-X github.com/amarbel-llc/clown/internal/buildcfg.CircusModelName=${gemma3-270m-model}"
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
        mkClownManagedSettings = { allowBypass ? false }: pkgs.writeText "clown-managed-settings.json" (
          builtins.toJSON {
            permissions = {
              # Block auto-mode (no prompts, classifier-gated tool calls).
              # Orthogonal to sandboxing — kept on for both variants.
              disableAutoMode = "disable";
            } // lib.optionalAttrs (!allowBypass) {
              # Block --dangerously-skip-permissions and bypassPermissions
              # mode. Sandboxed clownbox omits this so its inner claude can
              # actually run in YOLO mode within the sandbox.
              disableBypassPermissionsMode = "disable";
            } // {
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
        clownManagedSettingsSandboxed = mkClownManagedSettings { allowBypass = true; };

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
            postPatch =
              (old.postPatch or "")
              + ''
                substituteInPlace cli.js \
                  --replace-fail '/etc/claude-code' "${replacement}"
              '';
          });

        # Apply the shipped managed-settings file to a path-patched
        # claude-code derivation. Parameterized by which managed-settings
        # JSON to ship — strict for naked clown, permissive for clownbox.
        mkPatchedClaudeCode = managedSettings:
          (patchClaudeCodeManagedPath "$out/etc/claude").overrideAttrs
            (old: {
              postInstall =
                (old.postInstall or "")
                + ''
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
        # Sandboxed variant: same patched cli.js, different managed
        # settings (allowBypass = true). Used only by the clownbox
        # provider; the bubblewrap sandbox bounds what bypass actually
        # buys an attacker.
        patchedClaudeCodeSandboxed = mkPatchedClaudeCode clownManagedSettingsSandboxed;

        claudeCliPath = "${patchedClaudeCode}/bin/claude";
        codexCliPath = "${pkgs-codex.codex}/bin/codex";
        llamaServerPath = "${pkgs-llama.llama-cpp}/bin/llama-server";

        # clownbox: a fork of numtide/claudebox patched to forward args
        # after `--` to the inner claude invocation, and to bake the
        # absolute path of the inner claude binary into the bwrap'd shell
        # script. Upstream hardcodes `exec claude --dangerously-skip-
        # permissions` and relies on the host's $PATH being inherited
        # into the sandbox (claudebox.js captures process.env.PATH and
        # forwards via --setenv PATH), which silently fails when
        # claudebox is launched from a shell that doesn't have `claude`
        # on PATH. The substituted absolute path removes that dependency.
        # See nix/patches/claudebox-arg-passthrough.patch for the diff.
        patchedClownboxSrc = pkgs.applyPatches {
          name = "clownbox-src";
          src = claudebox-src;
          patches = [ ./nix/patches/claudebox-arg-passthrough.patch ];
          postPatch = ''
            substituteInPlace src/claudebox.js \
              --replace-fail '@CLOWNBOX_CLAUDE_BINARY@' '${patchedClaudeCodeSandboxed}/bin/claude'
          '';
        };

        clownbox = import "${claudebox-src}/package.nix" {
          inherit pkgs;
          claude-code = patchedClaudeCodeSandboxed;
          sourceDir = "${patchedClownboxSrc}/src";
        };

        clownboxCliPath = "${clownbox}/bin/claudebox";

        # Thin wrapper: sets CLOWN_PLUGIN_META (varies per mkCircus) then
        # execs the Go binary. All flag parsing, provider routing, and
        # plugin-host orchestration live in cmd/clown. The wrapped Go
        # binary varies by (defaultProvider, defaultProfile) — those
        # flags are linker-baked, so each combination is its own
        # derivation.
        mkClownBin =
          { pluginMeta
          , clownGoBin
          }: pkgs.writeShellScriptBin "clown" ''
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
        clown-manpages = pkgs.runCommand "clown-manpages" {
          inherit mdocDate;
        } ''
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
          { pluginMeta
          , defaultProvider ? defaultDefaultProvider
          , defaultProfile ? defaultDefaultProfile
          }:
          let
            clownGoBin = mkClownGo { inherit defaultProvider defaultProfile; };
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
          }).overrideAttrs (old: {
            passthru = (old.passthru or { }) // {
              tests = {
                managedSettingsRead = managedSettingsReadTest;
              };
            };
          });

        # Bats integration lane via the fork's pkgs.testers.batsLane.
        # Stages tests/bats/ into the build sandbox, exports binaries
        # under stable env-var names, and runs `bats --jobs N
        # [--filter-tags <filter>] *.bats`. The base derivation is the
        # default mkClownPkg purely for naming — the lane consumes the
        # individual subpackages by store path so it doesn't rebuild
        # Go on filter changes.
        # The inspect-compiled helper uses `#!/usr/bin/env bash` for
        # devshell portability, but the nix build sandbox has no
        # /usr/bin/env. Stage a shebang-patched copy via patchShebangs
        # so clown-plugin-host can exec it directly inside the lane.
        inspectCompiledPatched = pkgs.runCommand "inspect-compiled" { } ''
          cp ${./tests/scripts/inspect-compiled} $out
          chmod +x $out
          patchShebangs $out
        '';

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

        # Naming anchor for the lane derivation — only consulted for
        # `${base.pname}-bats-${suffix}`. Use the underlying
        # buildGoApplication (which has `pname = "clown"`) rather
        # than the symlinkJoin'd mkClownPkg, which has only `name`.
        # The actual binaries the tests invoke are exported via the
        # `binaries` attrset below.
        clownBatsBase =
          mkClownGo {
            defaultProvider = defaultDefaultProvider;
            defaultProfile = defaultDefaultProfile;
          };

        mkClownBatsLane =
          { filter ? "" }:
          pkgs.testers.batsLane {
            inherit filter;
            base = clownBatsBase;
            batsSrc = ./tests/bats;
            binaries = {
              CLOWN_STDIO_BRIDGE_BIN = {
                base = clown-stdio-bridge;
                name = "clown-stdio-bridge";
              };
              CLOWN_PLUGIN_HOST_BIN = {
                base = clown-plugin-host;
                name = "clown-plugin-host";
              };
              MOCK_STDIO_MCP_BIN = {
                base = mock-stdio-mcp;
                name = "mock-stdio-mcp";
              };
            };
            extraEnv = {
              SYNTHETIC_PLUGIN_DIR = "${synthetic-plugin}";
            };
            # plugin_host.bats invokes the inspect-compiled helper as
            # a downstream of clown-plugin-host. Stage it next to the
            # *.bats files so $BATS_TEST_DIRNAME/inspect-compiled
            # resolves it inside the sandbox.
            extraStagedFiles = [
              {
                src = inspectCompiledPatched;
                dest = "zz-tests_bats/inspect-compiled";
              }
            ];
            nativeBuildInputs = with pkgs; [ curl jq coreutils ];
          };

        # Auto-discover `# bats file_tags=...` directives across
        # tests/bats/*.bats and produce one lane per unique tag plus
        # an unfiltered `bats-default` lane. Lifted from
        # amarbel-llc/madder/go/default.nix.
        batsLaneOutputs =
          let
            batsFiles = builtins.filter
              (f: lib.hasSuffix ".bats" f)
              (builtins.attrNames (builtins.readDir ./tests/bats));
            extractFileTags = file:
              let
                content = builtins.readFile (./tests/bats + "/${file}");
                tagLines = builtins.filter
                  (l: lib.hasPrefix "# bats file_tags=" l)
                  (lib.splitString "\n" content);
              in
                if tagLines == [ ] then [ ]
                else lib.splitString ","
                  (lib.removePrefix "# bats file_tags="
                    (builtins.head tagLines));
            allFileTags = lib.unique (lib.concatMap extractFileTags batsFiles);
          in
            lib.listToAttrs (map
              (tag: lib.nameValuePair "bats-${tag}"
                (mkClownBatsLane { filter = tag; }))
              allFileTags) // {
              bats-default = mkClownBatsLane { };
            };

        mkCircus =
          { plugins ? [ ]
          , defaultProvider ? defaultDefaultProvider
          , defaultProfile ? defaultDefaultProfile
          }:
          let
            pluginMeta =
              if plugins == [ ] then emptyPluginMeta else resolvePlugins plugins;
          in
          {
            packages.default = mkClownPkg {
              inherit pluginMeta defaultProvider defaultProfile;
            };
            devShells.default = pkgs.mkShell {
              packages = [
                (pkgs.mkGoEnv { pwd = ./.; })
                pkgs-master.just
                pkgs.fish
                pkgs-claude-code.claude-code
                pkgs-codex.codex
                pkgs.opencode
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
          mock-stdio-mcp = mock-stdio-mcp;
          synthetic-plugin = synthetic-plugin;
        } // batsLaneOutputs;

        checks = {
          managedSettingsRead = managedSettingsReadTest;
          # bats-default runs every *.bats whose file_tags do NOT
          # exclude it (filter is empty → no exclusion). If a test
          # needs network/loopback the sandbox doesn't grant, tag it
          # with `# bats file_tags=net_cap` and run via
          # `nix build .#bats-net_cap` from a less-restricted env.
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
            pkgs.bun
            pkgs.mitmproxy
            pkgs.gomod2nix
          ];
        };

        lib.mkCircus = mkCircus;
      }
    );
}
