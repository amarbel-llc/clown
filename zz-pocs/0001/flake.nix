{
  description = "zz-pocs/0001: probe what __impure does NOT permit (Nix sandbox boundaries)";

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
        in
        {
          default = pkgs.stdenv.mkDerivation {
            name = "sandbox-probe";

            # Question this derivation answers: with __impure = true, does the
            # Nix builder sandbox still confine filesystem access? Or does
            # __impure relax that too?
            #
            # If filesystem confinement stays, Option D (drop sandcastle, rely
            # on Nix sandbox + egress broker + closure narrowing) is viable.
            # If filesystem confinement vanishes, we need something more.
            __impure = true;

            # Pull host env through nix-eval (needs --impure) and bake them
            # into the builder as derivation attrs. impureEnvVars didn't
            # propagate reliably on the darwin dev host, so we do it this way.
            CLOWN_PROBE_LOOPBACK_PORT   = builtins.getEnv "CLOWN_PROBE_LOOPBACK_PORT";
            CLOWN_PROBE_LOOPBACK_BANNER = builtins.getEnv "CLOWN_PROBE_LOOPBACK_BANNER";

            nativeBuildInputs = [ pkgs.coreutils pkgs.bash pkgs.curl pkgs.netcat ];

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
