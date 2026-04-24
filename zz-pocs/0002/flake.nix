{
  description = "zz-pocs/0002: ringmaster MCP server + synthetic sandbox-agent flake";

  # TEMPLATE NOTE: in v1, this flake will be rendered per invocation (or per
  # subagent-catalog refresh) by ringmaster. The rendered output MUST be
  # correct in its final form:
  #   - inputs fully resolved (workspace url, reference repos, ...)
  #   - no dangling placeholders
  #   - valid flake schema
  #   - deterministic for the same inputs (so nix eval-cache works)
  # The hand-written POC version below is the worked example the v1 generator
  # should match.

  inputs = {
    # Pinned to amarbel-llc/nixpkgs for the buildZxScriptFromFile helper.
    nixpkgs.url = "github:amarbel-llc/nixpkgs/9bad1e489bd4c713da002618bd825372d35430af";

    # The workspace the sandbox-agent derivation operates on. Defaulted here
    # to the clown worktree root so `nix build .#sandbox-agent` works from
    # inside this directory for smoke-testing. Ringmaster ALWAYS overrides
    # this per invocation via `--override-input workspace path:<ref>`.
    workspace.url = "path:../..";
    workspace.flake = false;
  };

  outputs =
    { self, nixpkgs, workspace, ... }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = import nixpkgs {
            inherit system;
            overlays = [ nixpkgs.overlays.amarbelPackages ];
          };

          ringmaster = pkgs.buildZxScriptFromFile {
            pname = "ringmaster";
            version = "0.0.1";
            script = ./ringmaster.ts;
            runtimeInputs = [ pkgs.nix ];
          };

          sandbox-agent = pkgs.stdenv.mkDerivation {
            name = "sandbox-agent-out";

            # NOTE: sandcastle is still stubbed; the agent runs unconfined.
            # Blocked on amarbel-llc/eng#41 (daemon config for
            # impure-derivations + allowed-impure-host-deps).

            nativeBuildInputs = [ pkgs.coreutils pkgs.bash ];

            dontUnpack = true;

            buildPhase = ''
              runHook preBuild
              mkdir -p "$out"
              bash ${./agent.sh} "${workspace}" "$out"
              runHook postBuild
            '';

            dontInstall = true;
            dontFixup = true;
          };
        in
        {
          default = ringmaster;
          inherit sandbox-agent;
        }
      );
    };
}
