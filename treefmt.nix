# treefmt-nix configuration. Run via `nix fmt`.
{
  projectRootFile = "flake.nix";
  programs.nixfmt.enable = true;
  # Vendored upstream code keeps its own style.
  settings.global.excludes = [
    "vendor/**"
    "flake.lock"
  ];
}
