# gomod.nix — clown's Nix-side view of its cross-flake Go module deps.
#
# Consumer half of the igloo flake-input-go_mod protocol (igloo RFC-0001).
# clown is a pure consumer here: it bridges the extracted ringmaster module
# (code.linenisgreat.com/ringmaster, which carries the jobwake + jobmcp
# packages) onto the ringmaster flake input's `go-pkgs` output. The bridge
# injects a Nix-store `replace` at eval time, so `go build` / the devshell
# resolve jobwake/jobmcp from the store instead of fetching the forge-hosted,
# proxy-unreachable module. go.mod keeps a decorative `require
# code.linenisgreat.com/ringmaster v0.1.0` line (Go's parser needs a version;
# the replace shadows it) and gomod2nix.toml carries no ringmaster entry (the
# bridge supplies the source).
#
# The single source of truth for the ringmaster rev is flake.lock; the
# require version above is intentionally frozen and MUST NOT be hand-bumped
# (see igloo RFC-0001 § Consumer interface, "Staleness of the organic
# version is the designed state").
#
# This value MUST be threaded identically into every buildGoApplication and
# mkGoEnv call in flake.nix (via `inherit goFlakeInputs;`) — build and
# devshell diverging silently reintroduces lockstep drift.
{
  ringmaster,
  purse-first,
  system,
}:
{
  "code.linenisgreat.com/ringmaster" = {
    src = ringmaster.packages.${system}.go-pkgs;
  };
  "code.linenisgreat.com/purse-first/libs/dewey" = {
    src = purse-first.packages.${system}.go-pkgs;
    subPath = "libs/dewey";
  };
}
