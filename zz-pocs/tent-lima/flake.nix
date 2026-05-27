{
  description = "zz-pocs/tent-lima: spike running clown --tent against Lima instead of podman-machine";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    # Pull the parent clown flake so we can reach its tent-image
    # output. `path:../..` avoids any github-fetch round-trip and
    # always reflects whatever's currently on disk in the worktree.
    clown.url = "path:../..";
  };

  outputs =
    { self, nixpkgs, clown, ... }:
    let
      systems = [
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
      # The tent-image is built for linux/aarch64 (the in-VM
      # architecture), but consumed from darwin (the host running
      # Lima). Map host → linux variant the same way clown's main
      # flake does for tentClaudeCliPath.
      hostToLinux = {
        "aarch64-darwin" = "aarch64-linux";
        "x86_64-darwin" = "x86_64-linux";
      };
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
          linuxSystem = hostToLinux.${system};
          # The parent's tent-image (clown-tent.tar.gz). This requires
          # nix-darwin's linux-builder to be enabled, since the host
          # is darwin and we need a linux-arch derivation.
          tentImageTarball = clown.packages.${linuxSystem}.tent-image;

          # The in-tent claude binary path. The parent's flake bakes
          # llm-agents.packages.<linuxSystem>.claude-code into the
          # tent image at build time, but doesn't expose it as a
          # flake output we can query. Reach into its input directly.
          # /nix/store is bind-mounted into the VM, so this same path
          # is reachable inside the container.
          tentClaudeBin = "${clown.inputs.llm-agents.packages.${linuxSystem}.claude-code}/bin/claude";
          smoke = pkgs.writeShellApplication {
            name = "tent-lima-smoke";
            runtimeInputs = [
              pkgs.lima
              pkgs.coreutils
              pkgs.gnused
              pkgs.gnugrep
              pkgs.jq
            ];
            text = ''
              # The runner is parameterized via env so callers can
              # override (e.g. for cleanup runs). Default to the
              # path-baked store paths from this derivation. Export
              # so the probe.sh subprocess sees them.
              export LIMA_INSTANCE="''${LIMA_INSTANCE:-clown-tent-lima}"
              export LIMA_YAML="''${LIMA_YAML:-${./lima.yaml}}"
              export TENT_IMAGE="''${TENT_IMAGE:-${tentImageTarball}}"
              export TENT_CLAUDE_BIN="''${TENT_CLAUDE_BIN:-${tentClaudeBin}}"

              echo ">> instance:  $LIMA_INSTANCE"
              echo ">> yaml:      $LIMA_YAML"
              echo ">> tent img:  $TENT_IMAGE"
              echo ">> claude:    $TENT_CLAUDE_BIN"
              echo

              bash ${./probe.sh}
            '';
          };
        in
        {
          # The smoke runner. Invoked via `nix run .#default`.
          default = smoke;
          # Same script, but exposed under a clearer name.
          tent-lima-smoke = smoke;

          # Bundle: lima binary + yaml + probe script in a tree under
          # share/tent-lima/. Useful for `nix shell .#tools` followed
          # by manual `limactl ...` invocations.
          tools = pkgs.symlinkJoin {
            name = "tent-lima-tools";
            paths = [
              pkgs.lima
              pkgs.coreutils
            ];
            postBuild = ''
              mkdir -p $out/share/tent-lima
              cp ${./lima.yaml} $out/share/tent-lima/lima.yaml
              cp ${./probe.sh} $out/share/tent-lima/probe.sh
              chmod +x $out/share/tent-lima/probe.sh
              ln -s ${tentImageTarball} $out/share/tent-lima/tent-image
            '';
          };
        }
      );

      apps = forAllSystems (system: {
        default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/tent-lima-smoke";
        };
      });
    };
}
