{
  description = "clown — coding-agent wrapper";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";
    nixpkgs-master.url = "github:NixOS/nixpkgs/5b471d29a84be70e8f5577258721b89865660493";
    utils.url = "https://flakehub.com/f/numtide/flake-utils/0.1.102";
  };

  outputs =
    {
      self,
      nixpkgs,
      nixpkgs-master,
      utils,
    }:
    utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs { inherit system; };
        pkgs-master = import nixpkgs-master { inherit system; };

        # Inline fetch — not a flake input, so cannot be overridden via follows.
        pkgs-claude-code-pinned = import
          (builtins.fetchTarball {
            url = "https://github.com/NixOS/nixpkgs/archive/5b471d29a84be70e8f5577258721b89865660493.tar.gz";
            sha256 = "1s420i7savy8njafgkh3qq4xwx9nw1h648g7jlpwig37ndlnfk7k";
          })
          {
            inherit system;
            config.allowUnfreePredicate =
              pkg: (pkgs.lib.getName pkg) == "claude-code";
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

        # Provider-specific wrappers share prompt discovery but diverge at the
        # final CLI contract. Claude supports prompt files and custom agents
        # natively. Codex support is sketched as a separate wrapper/output so
        # the flake shape is ready even if the exact Codex CLI flags evolve.
        mkProviderWrapper =
          {
            name,
            cliPath,
            preExec ? "",
            extraArgs ? "",
            commandName ? name,
          }:
          pkgs.writeShellScriptBin commandName ''
            set -euo pipefail

            cli=${cliPath}

            ${sharedPromptLogic}

            extra_args=()
            ${extraArgs}

            ${preExec}

            exec "$cli" "''${extra_args[@]}" "$@"
          '';

        clown-bin-claude = mkProviderWrapper {
          name = "claude";
          commandName = "clown";
          cliPath = "${pkgs-claude-code-pinned.claude-code}/bin/claude";
          extraArgs = ''
            extra_args+=(--disallowed-tools 'Bash(*)' --disallowed-tools 'Agent(Explore)')
            extra_args+=(--agents "$(<"${agents-file}")")

            if [[ -n "$system_prompt_file" ]]; then
              extra_args+=(--system-prompt-file "$system_prompt_file")
            fi

            if [[ -n "$append_fragments" ]]; then
              tmpfile=$(mktemp /tmp/clown-prompt.XXXXXX)
              trap 'rm -f "$tmpfile"' EXIT
              printf '%s' "$append_fragments" > "$tmpfile"
              extra_args+=(--append-system-prompt-file "$tmpfile")
            fi
          '';
        };

        clown-bin-codex = mkProviderWrapper {
          name = "codex";
          commandName = "clown-codex";
          cliPath = "\${CODEX_CLI:-$(command -v codex || true)}";
          preExec = ''
            if [[ -z "$cli" ]]; then
              echo "clown-codex: no Codex CLI found; set \$CODEX_CLI or install \`codex\` on PATH" >&2
              exit 1
            fi

            if [[ "''${1:-}" == "resume" || "''${1:-}" == "fork" ]]; then
              exec "$cli" "$@"
            fi

            # Sketch for Codex support:
            # - AGENTS.md is already the repo-level instruction file.
            # - Reuse the same hierarchical prompt fragments by concatenating them.
            # - The exact Codex flags for injecting prompt files, tool limits, and
            #   custom subagents should be filled in once the CLI contract is pinned.
            if [[ -n "$system_prompt_file" || -n "$append_fragments" ]]; then
              tmpfile=$(mktemp /tmp/clown-codex-prompt.XXXXXX)
              trap 'rm -f "$tmpfile"' EXIT

              if [[ -n "$system_prompt_file" ]]; then
                cat "$system_prompt_file" > "$tmpfile"
                if [[ -n "$append_fragments" ]]; then
                  printf '\n\n%s' "$append_fragments" >> "$tmpfile"
                fi
              else
                printf '%s' "$append_fragments" > "$tmpfile"
              fi

              echo "clown-codex: prompt bundle prepared at $tmpfile; wire this into the Codex CLI flags once selected" >&2
            fi
          '';
        };

        clown-sessions = pkgs.writeScriptBin "clown-sessions" ''
          #!${pkgs.python3}/bin/python3
          ${builtins.readFile ./bin/clown-sessions}
        '';

        clown-codex-sessions = pkgs.writeScriptBin "clown-codex-sessions" ''
          #!${pkgs.python3}/bin/python3
          ${builtins.readFile ./bin/clown-codex-sessions}
        '';

        clown-completions = pkgs.runCommand "clown-completions" { } ''
          mkdir -p $out/share/fish/vendor_completions.d
          cp ${./completions/clown.fish} $out/share/fish/vendor_completions.d/clown.fish
        '';

        clown-codex-completions = pkgs.runCommand "clown-codex-completions" { } ''
          mkdir -p $out/share/fish/vendor_completions.d
          cp ${./completions/clown-codex.fish} $out/share/fish/vendor_completions.d/clown-codex.fish
        '';
      in
      {
        packages = rec {
          default = dual;

          claude = pkgs.symlinkJoin {
            name = "clown";
            paths = [
              clown-bin-claude
              clown-sessions
              clown-completions
            ];
          };

          codex = pkgs.symlinkJoin {
            name = "clown-codex";
            paths = [
              clown-bin-codex
              clown-codex-sessions
              clown-codex-completions
            ];
          };

          dual = pkgs.symlinkJoin {
            name = "clown-dual";
            paths = [
              claude
              codex
            ];
          };
        };

        devShells.default = pkgs.mkShell {
          packages = [
            pkgs-master.just
            pkgs.fish
          ];
        };
      }
    );
}
