{
  description = "zz-pocs/0003: mitmproxy as egress broker — validation POC";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs =
    { self, nixpkgs, ... }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
          probeScript = ./probe.sh;

          # Pull broker connection info from the host via nix-eval env.
          # Requires --impure at `nix build` time.
          brokerHost   = builtins.getEnv "CLOWN_BROKER_HOST";
          brokerPort   = builtins.getEnv "CLOWN_BROKER_PORT";
          brokerCaHost = builtins.getEnv "CLOWN_BROKER_CA_PEM";

          # The Nix builder sandbox denies reads outside /nix/store, so we
          # can't bind-mount the host-side CA. Read its contents at
          # eval-time (via --impure readFile) and write a store path the
          # builder can read.
          brokerCa =
            if brokerCaHost == "" then
              null
            else
              pkgs.writeText "broker-ca.pem" (builtins.readFile brokerCaHost);
        in
        {
          default = pkgs.stdenv.mkDerivation {
            name = "broker-probe";

            # __impure: inherits host netns so we can reach the broker on
            # 127.0.0.1. Matches the design ADR-0005 locks in.
            __impure = true;

            CLOWN_BROKER_HOST   = brokerHost;
            CLOWN_BROKER_PORT   = brokerPort;
            CLOWN_BROKER_CA_PEM = if brokerCa != null then brokerCa else "";

            nativeBuildInputs = [
              pkgs.coreutils
              pkgs.bash
              pkgs.curl
              pkgs.cacert  # keeps a system CA bundle available; we override via env
            ];

            dontUnpack = true;

            buildPhase = ''
              runHook preBuild
              mkdir -p "$out"
              bash ${probeScript} "$out"
              runHook postBuild
            '';

            dontInstall = true;
            dontFixup = true;
          };
        }
      );
    };
}
