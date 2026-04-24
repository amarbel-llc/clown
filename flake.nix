{
  description = "clown — coding-agent wrapper";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";
    nixpkgs-master.url = "github:NixOS/nixpkgs/9b53530a5f6887b6903cffeb8a418f3079d6698d";
    utils.url = "https://flakehub.com/f/numtide/flake-utils/0.1.102";
    nixpkgs-claude-code.url = "github:NixOS/nixpkgs/b2b9662ffe1e9a5702e7bfbd983595dd56147dbf";
    nixpkgs-codex.url = "github:NixOS/nixpkgs/e2dde111aea2c0699531dc616112a96cd55ab8b5";
    # llama-cpp with Anthropic Messages API (/v1/messages) support — requires
    # PR #17570 (merged 2025-11-28). Build 6981 in nixos-25.11 predates it.
    nixpkgs-llama.url = "github:NixOS/nixpkgs/3b5a614454bd054dd960f1ff7a888dc5dfaf7bb4";
    gomod2nix.url = "github:amarbel-llc/gomod2nix";
    gomod2nix.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs =
    {
      self,
      nixpkgs,
      nixpkgs-master,
      nixpkgs-claude-code,
      nixpkgs-codex,
      nixpkgs-llama,
      gomod2nix,
      utils,
    }:
    utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs { inherit system; };
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

        gemma3-270m-model = pkgs.fetchurl {
          url = "https://huggingface.co/ggml-org/gemma-3-270m-it-GGUF/resolve/main/gemma-3-270m-it-Q8_0.gguf";
          hash = "sha256-DvV9LIOEWKGVJmQmDcujjlvdo3SU869zLwbkrdJAaOM=";
        };

        qwen3-06b-model = pkgs.fetchurl {
          url = "https://huggingface.co/Qwen/Qwen3-0.6B-GGUF/resolve/main/Qwen3-0.6B-Q8_0.gguf";
          hash = "sha256-lGXmOiKt1TVNm7S5npARcEPHEkAHZkkHJZvRbQQ7sDE=";
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
        mdocDate = "${monthNames.${flakeMonth}} ${toString (lib.toInt flakeDay)}, ${flakeYear}";

        buildGoApplication = gomod2nix.legacyPackages.${system}.buildGoApplication;

        goSrc = lib.fileset.toSource {
          root = ./.;
          fileset = lib.fileset.unions [
            ./go.mod
            ./gomod2nix.toml
            ./cmd
            ./internal
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
          ];
        };

        clown-go = buildGoApplication {
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
        clownManagedSettings = pkgs.writeText "clown-managed-settings.json" (
          builtins.toJSON {
            permissions = {
              # Block auto-mode (no prompts, classifier-gated tool calls).
              disableAutoMode = "disable";
              # Block --dangerously-skip-permissions and bypassPermissions mode.
              disableBypassPermissionsMode = "disable";
              # Hard denylist of destructive Bash patterns. Redundant with
              # clown-bin's --disallowed-tools 'Bash(*)' today, but keeps
              # guardrails intact if that ever narrows.
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
          }
        );

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

        patchedClaudeCode =
          (patchClaudeCodeManagedPath "$out/etc/claude").overrideAttrs
            (old: {
              postInstall =
                (old.postInstall or "")
                + ''
                  mkdir -p "$out/etc/claude"
                  cp ${clownManagedSettings} "$out/etc/claude/managed-settings.json"
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

        claudeCliPath = "${patchedClaudeCode}/bin/claude";
        codexCliPath = "${pkgs-codex.codex}/bin/codex";
        llamaServerPath = "${pkgs-llama.llama-cpp}/bin/llama-server";

        # Thin wrapper: sets CLOWN_PLUGIN_META (varies per mkCircus) then
        # execs the Go binary. All flag parsing, provider routing, and
        # plugin-host orchestration live in cmd/clown.
        mkClownBin = pluginMeta: pkgs.writeShellScriptBin "clown" ''
          export CLOWN_PLUGIN_META="${pluginMeta}"
          exec "${clown-go}/bin/clown" "$@"
        '';

        clown-sessions = pkgs.writeScriptBin "clown-sessions" ''
          #!${pkgs.python3}/bin/python3
          ${builtins.readFile ./bin/clown-sessions}
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
                    ''
                ) plugin.dirs;
              in
              dirBlocks
            ) plugins;
          in
          pkgs.runCommand "clown-plugin-meta" { } ''
            mkdir -p $out
            touch $out/plugin-dirs
            touch $out/version-info
            ${pluginBlocks}
          '';

        mkClownPkg =
          pluginMeta:
          (pkgs.symlinkJoin {
            name = "clown";
            paths = [
              (mkClownBin pluginMeta)
              clown-plugin-host
              circus-go
              clown-sessions
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

        mkCircus =
          { plugins ? [ ] }:
          let
            pluginMeta =
              if plugins == [ ] then emptyPluginMeta else resolvePlugins plugins;
          in
          {
            packages.default = mkClownPkg pluginMeta;
            devShells.default = pkgs.mkShell {
              packages = [
                pkgs-master.just
                pkgs.fish
                pkgs-claude-code.claude-code
                pkgs-codex.codex
                pkgs.opencode
                pkgs.bun
                pkgs.mitmproxy
                gomod2nix.packages.${system}.default
              ];
            };
            checks = {
              managedSettingsRead = managedSettingsReadTest;
            };
          };
      in
      {
        packages.default = mkClownPkg emptyPluginMeta;
        packages.clown-manpages = clown-manpages;

        checks = {
          managedSettingsRead = managedSettingsReadTest;
        };

        devShells.default = pkgs.mkShell {
          packages = [
            pkgs-master.just
            pkgs.fish
            pkgs.go
            pkgs-claude-code.claude-code
            pkgs-codex.codex
            pkgs.opencode
            pkgs.bun
            pkgs.mitmproxy
            gomod2nix.packages.${system}.default
          ];
        };

        lib.mkCircus = mkCircus;
      }
    );
}
