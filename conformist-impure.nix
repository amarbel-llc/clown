# Overlay for conformistImpureEval (flake.nix), merged with
# conformist.lib.presets.eng-impure. That preset's git-state / sweatfile /
# agents-md checks are what `just lint-worktree` runs against the working
# tree; see conformist.nix for the pure-eval overlay used by `nix fmt` /
# checks.formatting instead.
{ lib, ... }:
{
  # presets.eng-impure's gomod2nix.toml drift check has nothing to check:
  # dependencies live in go.nix (igloo FDR 0008), with no go.mod or
  # gomod2nix.toml in the checkout (the linter would no-op on the missing
  # go.mod anyway; disabled explicitly, as spinclass does).
  linters.gomod2nix.enable = lib.mkForce false;
}
