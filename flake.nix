{
  description = "clown — claude-code wrapper";

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
        pkgs-master = import nixpkgs-master {
          inherit system;
          config.allowUnfreePredicate =
            pkg: (pkgs.lib.getName pkg) == "claude-code";
        };
      in
      let
        clown-bin = pkgs.writeShellScriptBin "clown" ''
          set -euo pipefail

          claude=${pkgs-master.claude-code}/bin/claude

          # Walk from PWD up to HOME, collecting .circus/ directories
          walkup_dirs=()
          d=$(pwd)
          while true; do
            walkup_dirs+=("$d")
            if [[ "$d" == "$HOME" ]] || [[ "$d" == "/" ]]; then
              break
            fi
            d=$(dirname "$d")
          done

          # Reverse to shallowest-first order
          reversed=()
          for (( i=''${#walkup_dirs[@]}-1; i>=0; i-- )); do
            reversed+=("''${walkup_dirs[$i]}")
          done

          # Collect system-prompt.d fragments (shallowest first, sorted within each dir)
          append_fragments=""
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

          # Find nearest (deepest) .circus/system-prompt file for replace mode
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

          # Build claude args
          extra_args=()

          if [[ -n "$system_prompt_file" ]]; then
            extra_args+=(--system-prompt-file "$system_prompt_file")
          fi

          if [[ -n "$append_fragments" ]]; then
            tmpfile=$(mktemp /tmp/clown-prompt.XXXXXX)
            trap 'rm -f "$tmpfile"' EXIT
            printf '%s' "$append_fragments" > "$tmpfile"
            extra_args+=(--append-system-prompt-file "$tmpfile")
          fi

          exec "$claude" "''${extra_args[@]}" "$@"
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
            clown-completions
          ];
        };

        devShells.default = pkgs.mkShell {
          packages = [
            pkgs-master.just
          ];
        };
      }
    );
}
