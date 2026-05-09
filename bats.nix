# Bats integration lanes via the fork's pkgs.testers.batsLane.
# Stages tests/bats/ into the build sandbox, exports binaries under
# stable env-var names, and runs `bats --jobs N [--filter-tags <filter>]
# *.bats`. The base derivation is mkClownGo purely for naming — the lane
# consumes the individual subpackages by store path so it doesn't rebuild
# Go on filter changes.
#
# Returns an attrset with one lane per unique `# bats file_tags=...`
# directive plus an unfiltered `bats-default` lane. No tags are in
# use today (see ADR docs/decisions/0001-net-cap-tag.md), so the
# tag-derived lanes are empty and `bats-default` runs every file.
{
  pkgs,
  lib,
  mkClownGo,
  defaultDefaultProvider,
  defaultDefaultProfile,
  clown-stdio-bridge,
  clown-plugin-host,
  mock-stdio-mcp,
  synthetic-plugin,
}:
let
  # The inspect-compiled helper uses `#!/usr/bin/env bash` for
  # devshell portability, but the nix build sandbox has no
  # /usr/bin/env. Stage a shebang-patched copy via patchShebangs
  # so clown-plugin-host can exec it directly inside the lane.
  inspectCompiledPatched = pkgs.runCommand "inspect-compiled" { } ''
    cp ${./tests/scripts/inspect-compiled} $out
    chmod +x $out
    patchShebangs $out
  '';

  # Naming anchor for the lane derivation — only consulted for
  # `${base.pname}-bats-${suffix}`. Use the underlying
  # buildGoApplication (which has `pname = "clown"`) rather
  # than the symlinkJoin'd mkClownPkg, which has only `name`.
  # The actual binaries the tests invoke are exported via the
  # `binaries` attrset below.
  clownBatsBase = mkClownGo {
    defaultProvider = defaultDefaultProvider;
    defaultProfile = defaultDefaultProfile;
  };

  mkClownBatsLane =
    {
      filter ? "",
    }:
    pkgs.testers.batsLane {
      inherit filter;
      base = clownBatsBase;
      batsSrc = ./tests/bats;
      binaries = {
        CLOWN_STDIO_BRIDGE_BIN = {
          base = clown-stdio-bridge;
          name = "clown-stdio-bridge";
        };
        CLOWN_PLUGIN_HOST_BIN = {
          base = clown-plugin-host;
          name = "clown-plugin-host";
        };
        MOCK_STDIO_MCP_BIN = {
          base = mock-stdio-mcp;
          name = "mock-stdio-mcp";
        };
      };
      extraEnv = {
        SYNTHETIC_PLUGIN_DIR = "${synthetic-plugin}";
      };
      # plugin_host.bats invokes the inspect-compiled helper as
      # a downstream of clown-plugin-host. Stage it next to the
      # *.bats files so $BATS_TEST_DIRNAME/inspect-compiled
      # resolves it inside the sandbox.
      extraStagedFiles = [
        {
          src = inspectCompiledPatched;
          dest = "zz-tests_bats/inspect-compiled";
        }
      ];
      nativeBuildInputs = with pkgs; [
        curl
        jq
        coreutils
      ];
    };

  # Auto-discover `# bats file_tags=...` directives across
  # tests/bats/*.bats and produce one lane per unique tag plus
  # an unfiltered `bats-default` lane. Lifted from
  # amarbel-llc/madder/go/default.nix.
  batsFiles = builtins.filter (f: lib.hasSuffix ".bats" f) (
    builtins.attrNames (builtins.readDir ./tests/bats)
  );
  extractFileTags =
    file:
    let
      content = builtins.readFile (./tests/bats + "/${file}");
      tagLines = builtins.filter (l: lib.hasPrefix "# bats file_tags=" l) (lib.splitString "\n" content);
    in
    if tagLines == [ ] then
      [ ]
    else
      lib.splitString "," (lib.removePrefix "# bats file_tags=" (builtins.head tagLines));
  allFileTags = lib.unique (lib.concatMap extractFileTags batsFiles);
in
lib.listToAttrs (
  map (
    tag:
    lib.nameValuePair "bats-${tag}" (mkClownBatsLane {
      filter = tag;
    })
  ) allFileTags
)
// {
  bats-default = mkClownBatsLane { };
}
