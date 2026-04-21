{
  description = "clown — coding-agent wrapper";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";
    nixpkgs-master.url = "github:NixOS/nixpkgs/9b53530a5f6887b6903cffeb8a418f3079d6698d";
    utils.url = "https://flakehub.com/f/numtide/flake-utils/0.1.102";
    nixpkgs-claude-code.url = "github:NixOS/nixpkgs/b2b9662ffe1e9a5702e7bfbd983595dd56147dbf";
    nixpkgs-codex.url = "github:NixOS/nixpkgs/e2dde111aea2c0699531dc616112a96cd55ab8b5";
  };

  outputs =
    {
      self,
      nixpkgs,
      nixpkgs-master,
      nixpkgs-claude-code,
      nixpkgs-codex,
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
          map (f: parseAgent (./subagents + "/${f}")) (
            builtins.filter (f: lib.hasSuffix ".md" f) agentFiles
          )
        );
        agents-json = builtins.toJSON agents;
        agents-file = pkgs.writeText "clown-agents.json" agents-json;

        clownVersion = lib.trim (builtins.readFile ./version.txt);
        clownRev = self.rev or self.dirtyRev or "dirty";
        clownShortRev = self.shortRev or self.dirtyShortRev or "dirty";
        claudeCodeVersion = pkgs-claude-code.claude-code.version;
        claudeCodeRev = nixpkgs-claude-code.rev or "dirty";
        codexVersion = pkgs-codex.codex.version;
        codexRev = nixpkgs-codex.rev or "dirty";

        clown-plugin-host = pkgs.buildGoModule {
          pname = "clown-plugin-host";
          version = clownVersion;
          src = lib.fileset.toSource {
            root = ./.;
            fileset = lib.fileset.unions [
              ./go.mod
              ./cmd
              ./internal
            ];
          };
          subPackages = [ "cmd/clown-plugin-host" ];
          vendorHash = null;
          ldflags = [
            "-s" "-w"
            "-X main.version=${clownVersion}"
            "-X main.commit=${clownRev}"
          ];
        };

        sharedPromptLogic = ''
          # Walk from PWD up to HOME, collecting .circus/ directories.
          walkup_dirs=()
          d=$(pwd)
          while true; do
            walkup_dirs+=("$d")
            if [[ "$d" == "$HOME" ]] || [[ "$d" == "/" ]]; then
              break
            fi
            d=$(dirname "$d")
          done

          # Reverse to shallowest-first order.
          reversed=()
          for (( i=''${#walkup_dirs[@]}-1; i>=0; i-- )); do
            reversed+=("''${walkup_dirs[$i]}")
          done

          # Builtin system prompt append (always included, before user fragments).
          append_fragments=""
          for f in $(find ${./system-prompt-append.d} -maxdepth 1 -name '*.md' -type f | sort); do
            content=$(<"$f")
            if [[ -n "$content" ]]; then
              append_fragments+="$content"
              append_fragments+=$'\n\n'
            fi
          done

          # Collect .circus/system-prompt.d fragments (shallowest first, sorted within each dir).
          for dir in "''${reversed[@]}"; do
            prompt_d="$dir/.circus/system-prompt.d"
            if [[ -d "$prompt_d" ]]; then
              for f in $(find "$prompt_d" -maxdepth 1 -name '*.md' -type f | sort); do
                content=$(<"$f")
                if [[ -n "$content" ]]; then
                  append_fragments+="$content"
                  append_fragments+=$'\n\n'
                fi
              done
            fi
          done

          # Find nearest (deepest) .circus/system-prompt file for replace mode.
          system_prompt_file=""
          d=$(pwd)
          while true; do
            if [[ -f "$d/.circus/system-prompt" ]]; then
              system_prompt_file="$d/.circus/system-prompt"
              break
            fi
            if [[ "$d" == "$HOME" ]] || [[ "$d" == "/" ]]; then
              break
            fi
            d=$(dirname "$d")
          done
        '';

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

        mkClownBin = pluginMeta: pkgs.writeShellScriptBin "clown" ''
          set -euo pipefail

          # --- Parse and consume clown flags ---
          provider="''${CLOWN_PROVIDER:-claude}"
          clean=false
          forwarded_args=()
          while [[ $# -gt 0 ]]; do
            case "$1" in
              version|--version|-v)
                {
                  printf '%-20s %-12s %s\n' COMPONENT VERSION REV
                  printf '%-20s %-12s %s\n' claude-code '${claudeCodeVersion}' '${claudeCodeRev}'
                  printf '%-20s %-12s %s\n' clown '${clownVersion}' '${clownRev}'
                  printf '%-20s %-12s %s\n' codex '${codexVersion}' '${codexRev}'
                  if [[ -f "${pluginMeta}/version-info" ]]; then
                    cat "${pluginMeta}/version-info"
                  fi
                } | (IFS= read -r header; printf '%s\n' "$header"; sort)
                exit 0
                ;;
              --provider)
                provider="$2"
                shift 2
                ;;
              --provider=*)
                provider="''${1#--provider=}"
                shift
                ;;
              --clean)
                clean=true
                shift
                ;;
              *)
                forwarded_args+=("$1")
                shift
                ;;
            esac
          done
          set -- "''${forwarded_args[@]}"

          # --- Resolve provider CLI ---
          case "$provider" in
            claude)
              cli="${claudeCliPath}"
              ;;
            codex)
              cli="${codexCliPath}"
              ;;
            *)
              echo "clown: unknown provider '$provider'" >&2
              exit 1
              ;;
          esac

          if [[ "$clean" == true ]]; then
            exec "$cli" "$@"
          fi

          ${sharedPromptLogic}

          # --- Provider-specific flag injection ---
          extra_args=()

          case "$provider" in
            claude)
              extra_args+=(--disallowed-tools 'Bash(*)' --disallowed-tools 'Agent(Explore)')
              extra_args+=(--agents "$(<"${agents-file}")")
              plugin_host_args=()
              if [[ -f "${pluginMeta}/plugin-dirs" ]]; then
                while IFS= read -r dir; do
                  extra_args+=(--plugin-dir "$dir")
                  plugin_host_args+=(--plugin-dir "$dir")
                done < "${pluginMeta}/plugin-dirs"
              fi

              if [[ -n "$system_prompt_file" ]]; then
                extra_args+=(--system-prompt-file "$system_prompt_file")
              fi

              if [[ -n "$append_fragments" ]]; then
                tmpfile=$(mktemp /tmp/clown-prompt.XXXXXX)
                trap 'rm -f "$tmpfile"' EXIT
                printf '%s' "$append_fragments" > "$tmpfile"
                extra_args+=(--append-system-prompt-file "$tmpfile")
              fi
              ;;

            codex)
              extra_args+=(--sandbox workspace-write)

              if [[ -n "$system_prompt_file" || -n "$append_fragments" ]]; then
                tmpfile=$(mktemp /tmp/clown-prompt.XXXXXX)
                trap 'rm -f "$tmpfile"' EXIT

                if [[ -n "$system_prompt_file" ]]; then
                  cat "$system_prompt_file" > "$tmpfile"
                  if [[ -n "$append_fragments" ]]; then
                    printf '\n\n%s' "$append_fragments" >> "$tmpfile"
                  fi
                else
                  printf '%s' "$append_fragments" > "$tmpfile"
                fi

                extra_args+=(--config "experimental_instructions_file=$tmpfile")
              fi
              ;;
          esac

          # --- Future: moxin auto-loading ---
          # When implemented, this section will:
          # 1. Run `moxy list-moxins` to discover available moxins
          # 2. Generate --mcp-config entries for each discoverable moxin
          # 3. Append to extra_args
          # Blocked on: defining which moxins should auto-load vs remain manual.

          # Route through clown-plugin-host for claude provider. It scans
          # --plugin-dir paths for clown.json, launches HTTP MCP servers,
          # and execs claude (or runs it as a child if servers are active).
          # For codex, invoke directly (no plugin-host support).
          if [[ "$provider" == "claude" ]]; then
            "${clown-plugin-host}/bin/clown-plugin-host" \
              "''${plugin_host_args[@]}" \
              -- "$cli" "''${extra_args[@]}" "$@"
          else
            "$cli" "''${extra_args[@]}" "$@"
          fi
          status=$?
          printf '\e[?2004l\e[?1l\e[?25h\e[J'
          exit $status
        '';

        clown-sessions = pkgs.writeScriptBin "clown-sessions" ''
          #!${pkgs.python3}/bin/python3
          ${builtins.readFile ./bin/clown-sessions}
        '';

        clown-completions = pkgs.runCommand "clown-completions" { } ''
          mkdir -p $out/share/fish/vendor_completions.d
          cp ${./completions/clown.fish} $out/share/fish/vendor_completions.d/clown.fish
        '';

        clown-manpages = pkgs.runCommand "clown-manpages" { } ''
          for section in 1 5 7; do
            mkdir -p $out/share/man/man$section
          done
          cp ${./man/man1}/*.1 $out/share/man/man1/
          cp ${./man/man5}/*.5 $out/share/man/man5/
          cp ${./man/man7}/*.7 $out/share/man/man7/
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
              ];
            };
            checks = {
              managedSettingsRead = managedSettingsReadTest;
            };
          };
      in
      {
        packages.default = mkClownPkg emptyPluginMeta;

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
          ];
        };

        lib.mkCircus = mkCircus;
      }
    );
}
