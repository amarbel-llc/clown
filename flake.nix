{
  description = "clown — coding-agent wrapper";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";
    nixpkgs-master.url = "github:NixOS/nixpkgs/9b53530a5f6887b6903cffeb8a418f3079d6698d";
    utils.url = "https://flakehub.com/f/numtide/flake-utils/0.1.102";
    moxy.url = "github:amarbel-llc/moxy";
    moxy.inputs.nixpkgs.follows = "nixpkgs";
    moxy.inputs.nixpkgs-master.follows = "nixpkgs-master";
    moxy.inputs.bob.follows = "bob";
    bob.url = "github:amarbel-llc/bob";
    bob.inputs.nixpkgs.follows = "nixpkgs";
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
      moxy,
      bob,
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

        moxyPluginDir = "${moxy.packages.${system}.default}/share/purse-first/moxy";
        bobPluginDir = "${bob.packages.${system}.default}/share/purse-first/bob";

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

        # Unified wrapper dispatching to Claude (default) or Codex via
        # --provider flag. Provider-specific flags (tool policy, prompt
        # injection, subagents) are injected per-provider after shared
        # prompt discovery runs.
        clown-bin = pkgs.writeShellScriptBin "clown" ''
          set -euo pipefail

          # --- Parse and consume clown flags ---
          provider="''${CLOWN_PROVIDER:-claude}"
          clean=false
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
              extra_args+=(--plugin-dir "${moxyPluginDir}" --plugin-dir "${bobPluginDir}")

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

          # Run without exec so we can clean up TTY state after exit.
          # Workaround for anthropics/claude-code#39272 (dirty terminal on exit).
          "$cli" "''${extra_args[@]}" "$@"
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

        # Runtime proof that the managed-settings patch is live: bake a
        # writable test path into a sibling claude-code, write invalid JSON
        # there, and confirm claude logs a parse error to the diagnostics
        # stream. If the patch were broken, claude would read the unpatched
        # /etc/claude-code path, find nothing, and log zero errors.
        managedSettingsTestPath = "/build/clown-test-managed-settings";
        badSettingsClaude = patchClaudeCodeManagedPath managedSettingsTestPath;

        managedSettingsReadTest = pkgs.runCommand "clown-managed-settings-read-test" { } ''
          mkdir -p ${managedSettingsTestPath}
          printf '%s' 'NOT VALID JSON {{{' > ${managedSettingsTestPath}/managed-settings.json
          export HOME=$TMPDIR/home
          mkdir -p "$HOME"
          export CLAUDE_CODE_DIAGNOSTICS_FILE=$TMPDIR/diag.jsonl
          ${badSettingsClaude}/bin/claude mcp list > /dev/null 2>&1 || true
          if [[ ! -s $TMPDIR/diag.jsonl ]]; then
            echo "FAIL: no diagnostics produced — claude may not have launched" >&2
            exit 1
          fi
          if ! grep -q '"error_count":[1-9]' $TMPDIR/diag.jsonl; then
            echo "FAIL: no settings parse errors logged — claude did not read ${managedSettingsTestPath}" >&2
            cat $TMPDIR/diag.jsonl >&2
            exit 1
          fi
          touch $out
        '';
      in
      {
        packages.default = (pkgs.symlinkJoin {
          name = "clown";
          paths = [
            clown-bin
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

        packages.moxy = moxy.packages.${system}.default;

        checks = {
          managedSettingsRead = managedSettingsReadTest;
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
