# Bats integration lanes via amarbel-llc/bats's batsLane builder.
# Stages zz-tests_bats/ into the build sandbox, exports binaries under
# stable env-var names, plumbs the bats-libs helper bundle onto
# BATS_LIB_PATH, and runs `bats --jobs N [--filter-tags <filter>]
# *.bats`. The base derivation is mkClownGo purely for naming — the lane
# consumes the individual subpackages by store path so it doesn't rebuild
# Go on filter changes.
#
# Returns an attrset with one lane per unique `# bats file_tags=...`
# directive plus an unfiltered `bats-default` lane. No tags are in
# use today (see ADR docs/adrs/0007-drop-net-cap-bats-file-tag.md),
# so the tag-derived lanes are empty and `bats-default` runs every
# file.
{
  pkgs,
  lib,
  batsLane,
  bats-libs,
  mkClownGo,
  defaultDefaultProvider,
  defaultDefaultProfile,
  clown-stdio-bridge,
  clown-plugin-host,
  mock-stdio-mcp,
  synthetic-plugin,
  # Shebang-patched copy of the inspect-compiled helper, lifted to
  # flake.nix so clown-cover's coverIntegrationCommand can stage the
  # same artifact this lane stages.
  inspectCompiledPatched,
  # Ringmaster e2e lane fixtures: the daemon, its CLI client, and a
  # http stand-in for llama-server. Plumbed into the binaries map
  # below as RINGMASTER_BIN / CIRCUS_BIN / FAKE_LLAMA_SERVER_BIN.
  ringmaster,
  circus,
  fake-llama-server,
}:
let
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
    batsLane {
      inherit filter;
      base = clownBatsBase;
      batsSrc = ./zz-tests_bats;
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
        RINGMASTER_BIN = {
          base = ringmaster;
          name = "ringmaster";
        };
        CIRCUS_BIN = {
          base = circus;
          name = "circus";
        };
        FAKE_LLAMA_SERVER_BIN = {
          base = fake-llama-server;
          name = "fake-llama-server";
        };
      };
      # bats-libs ships bats-support, bats-assert, bats-emo, bats-island
      # under share/bats; surfacing batsLibPath here lets common.bash
      # call `bats_load_library bats-island` etc. from inside the lane.
      batsLibPath = [ bats-libs.batsLibPath ];
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
        # bats-island's setup_test_home shells out to `git config`
        # while configuring GIT_CONFIG_GLOBAL. The nix builder PATH
        # doesn't include git by default, so provide it explicitly.
        git
      ];
    };

  # Auto-discover `# bats file_tags=...` directives across
  # zz-tests_bats/*.bats and produce one lane per unique tag plus
  # an unfiltered `bats-default` lane. Lifted from
  # amarbel-llc/madder/go/default.nix.
  batsFiles = builtins.filter (f: lib.hasSuffix ".bats" f) (
    builtins.attrNames (builtins.readDir ./zz-tests_bats)
  );
  extractFileTags =
    file:
    let
      content = builtins.readFile (./zz-tests_bats + "/${file}");
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
