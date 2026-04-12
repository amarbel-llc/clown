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
      {
        packages.default = pkgs.writeShellScriptBin "clown" ''
          exec ${pkgs-master.claude-code}/bin/claude "$@"
        '';

        devShells.default = pkgs.mkShell {
          packages = [
            pkgs-master.just
          ];
        };
      }
    );
}
