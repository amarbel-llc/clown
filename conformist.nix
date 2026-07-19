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

  settings.excludes = [
    "vendor/**"
    "flake.lock"
    "*.md"
    "result"
    "result-*"
    ".tmp/**"
  ];
}
