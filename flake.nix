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

        claudeCliPath = "${pkgs-claude-code-pinned.claude-code}/bin/claude";

        # Unified wrapper dispatching to Claude (default) or Codex via
        # --provider flag. Provider-specific flags (tool policy, prompt
        # injection, subagents) are injected per-provider after shared
        # prompt discovery runs.
        clown-bin = pkgs.writeShellScriptBin "clown" ''
          set -euo pipefail

          # --- Parse and consume --provider flag ---
          provider="''${CLOWN_PROVIDER:-claude}"
          forwarded_args=()
          while [[ $# -gt 0 ]]; do
            case "$1" in
              --provider)
                provider="$2"
                shift 2
                ;;
              --provider=*)
                provider="''${1#--provider=}"
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
              cli="''${CODEX_CLI:-$(command -v codex || true)}"
              if [[ -z "$cli" ]]; then
                echo "clown: no Codex CLI found; set \$CODEX_CLI or install codex on PATH" >&2
                exit 1
              fi
              ;;
            *)
              echo "clown: unknown provider '$provider'" >&2
              exit 1
              ;;
          esac

          ${sharedPromptLogic}

          # --- Provider-specific flag injection ---
          extra_args=()

          case "$provider" in
            claude)
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

          exec "$cli" "''${extra_args[@]}" "$@"
        '';

        clown-sessions = pkgs.writeScriptBin "clown-sessions" ''
          #!${pkgs.python3}/bin/python3
          ${builtins.readFile ./bin/clown-sessions}
        '';

        clown-completions = pkgs.runCommand "clown-completions" { } ''
          mkdir -p $out/share/fish/vendor_completions.d
          cp ${./completions/clown.fish} $out/share/fish/vendor_completions.d/clown.fish
        '';
      in
      {
        packages.default = pkgs.symlinkJoin {
          name = "clown";
          paths = [
            clown-bin
            clown-sessions
            clown-completions
          ];
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
