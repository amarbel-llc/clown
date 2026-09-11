# Bats integration lanes via amarbel-llc/bats's batsLane builder.
# Stages zz-tests_bats/ into the build sandbox, exports binaries under
# stable env-var names, plumbs the bats-libs helper bundle onto
# BATS_LIB_PATH, and runs `bats --jobs N [--filter-tags <filter>]
# *.bats`. The binaries map comes from flake.nix's batsBinaries, shared
# with the clown-cover lane so the two can't drift apart (clown#237); the
# lane consumes each binary by store path so it doesn't rebuild Go on
# filter changes.
#
# Returns an attrset with one lane per unique `# bats file_tags=...`
# directive plus a `bats-default` lane that runs everything except the
# host-only tags (see hostOnlyTags below).
#
# Two tags are in use: `tent_smoke` (excluded from bats-default — the nix
# builder can't run rootless podman) and `provider_mcp` (clown#203, the
# opencode/crush MCP delivery lane; included in bats-default, addressable
# on its own via `nix build .#bats-provider_mcp` since it drives two real
# provider binaries and is slower than the rest).
# ADR docs/adrs/0007-drop-net-cap-bats-file-tag.md records why there is no
# `net_cap` tag — loopback binds need no capability escalation.
{
  pkgs,
  lib,
  batsLane,
  bats-libs,
  # The env var -> { base; name; } binaries map every lane exports
  # (flake.nix batsBinaries).
  batsBinaries,
  synthetic-plugin,
  # Shebang-patched copy of the inspect-compiled helper, lifted to
  # flake.nix so clown-cover's coverIntegrationCommand can stage the
  # same artifact this lane stages.
  inspectCompiledPatched,
}:
let

  mkClownBatsLane =
    {
      filter ? "",
    }:
    batsLane {
      inherit filter;
      # Naming anchor only (`${base.pname}-bats-<suffix>`): the mkClownGo
      # build behind CLOWN_BIN, whose pname is "clown" (the symlinkJoin'd
      # mkClownPkg has only `name`).
      base = batsBinaries.CLOWN_BIN.base;
      batsSrc = ./zz-tests_bats;
      binaries = batsBinaries;
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

  # Tags whose files require host capabilities (rootless podman,
  # userns mappings, a loaded clown-tent image) that the nix builder
  # sandbox doesn't provide. Excluded from `bats-default` via
  # `bats --filter-tags !<tag>`; still exposed as their own lane
  # (`bats-tent_smoke`) for tooling that wants to address them
  # explicitly. The host-side `just test-tent-smoke` recipe runs them
  # outside any nix lane.
  hostOnlyTags = [ "tent_smoke" ];
  defaultExclusionFilter = lib.concatStringsSep "," (map (t: "!${t}") hostOnlyTags);
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
  bats-default = mkClownBatsLane {
    filter = defaultExclusionFilter;
  };
}
