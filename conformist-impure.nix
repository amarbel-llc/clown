# Overlay for conformistImpureEval (flake.nix), merged with
# conformist.lib.presets.eng-impure. That preset's git-state / sweatfile /
# agents-md checks are what `just lint-worktree` runs against the working
# tree; see conformist.nix for the pure-eval overlay used by `nix fmt` /
# checks.formatting instead.
{ lib, ... }:
{
  # Upstream: presets.eng-impure enables a gomod2nix.toml drift check that
  # regenerates gomod2nix.toml via `gomod2nix --dir . --outdir <tmp>` and
  # diffs it (nix/linters/gomod2nix.nix) — no --impure/GOFLAGS/GOPROXY
  # override, so it shells straight out to `go mod download`. clown
  # consumes code.linenisgreat.com/ringmaster via a Nix-injected `replace`
  # (igloo's goFlakeInputs bridge, gomod.nix) that only exists inside `nix
  # build`/the mkGoEnv devShell — outside that, `go mod download` tries to
  # resolve the bridged module over the network and fails ("unknown
  # revision"). Same root cause as the `build-go`/`update-gomod2nix`
  # unreliability documented in AGENTS.md (clown#174); no check-only/vendor
  # knob exists on this linter to route around it. `just build` (the real
  # nix build, where the replace is live) remains the authoritative gomod2nix
  # consistency check.
  linters.gomod2nix.enable = lib.mkForce false;
}
