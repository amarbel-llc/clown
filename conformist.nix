# clown's conformist overlay, merged with conformist.lib.presets.{eng,eng-go}
# in flake.nix (conformist.lib.evalModule). presets.eng enables the
# eng-convention linters (eng-versioning, flake-outputs/lock, justfile-*
# roster); presets.eng-go carries the canonical goimports -> gofumpt chain.
# Here live the repo-specific formatters, the shellcheck linter, and excludes.
{ pkgs, lib, ... }:
{
  programs.nixfmt.enable = true;

  settings.formatter.shfmt = {
    command = "${pkgs.shfmt}/bin/shfmt";
    options = [
      "-w"
      "-i"
      "2"
      "-s"
      "-ci"
    ];
    includes = [
      "*.sh"
      "*.bash"
      "*.bats"
    ];
  };

  linters.shellcheck.enable = true;
  linters.shellcheck.severity = "warning";
  linters.shellcheck.includes = [
    "*.sh"
    "*.bash"
    "*.bats"
  ];

  linters.eng-versioning.key = "CLOWN_VERSION";

  # Upstream bug: this linter extracts a recipe's "verb" by splitting on the
  # first '-', but never strips just's own module-qualification syntax first.
  # Any recipe in a `mod`-imported justfile (zz-explore/justfile, mounted as
  # `explore::*`) is misparsed as verb `explore::debug` etc., which can never
  # match the known-verb allowlist regardless of the recipe's actual name —
  # confirmed by reading conformist's nix/linters/justfile-recipe-names.nix.
  # settings.excludes doesn't help either: this linter runs as a whole-tree
  # check, not per-file. Re-enable once conformist strips `modname::` before
  # verb-checking (tracked upstream as the module-qualification issue).
  linters.justfile-recipe-names.enable = lib.mkForce false;

  settings.excludes = [
    "vendor/**"
    "flake.lock"
    "*.md"
    "result"
    "result-*"
    ".tmp/**"
  ];
}
